package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/urfave/cli/v3"
	"golang.org/x/sync/errgroup"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/azurearm"
	"github.com/investerra/lazure/internal/errs"
)

// StatusFlags returns the status-specific flag list for main.go to wire.
func StatusFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:  "format",
			Usage: "output format: table|json",
			Value: "table",
		},
		&cli.BoolFlag{Name: "no-color", Usage: "disable ANSI colors (also honored via NO_COLOR env)"},
	}
}

// Status implements `lazure status <env>`. Single GET of the deployed
// Container App; table or JSON output.
//
// Table shape:
//
//	App:         api-server
//	Env:         dev
//	Location:    switzerlandnorth
//	Ingress:     https://api-server.dev-env.switzerlandnorth.azurecontainerapps.io
//	Revision:    api-server--abc123
//	State:       Succeeded
//	Traffic:
//	  100% → latest
//	Replicas:    min=1 max=3
//
// JSON shape is the raw ARM body — callers can pipe to jq. Nothing is
// filtered; read-only fields are included because they're what you
// actually want to see in "status."
func Status(ctx context.Context, c *cli.Command) error {
	format := c.String("format")
	color := shouldColor(c.Bool("no-color"))
	if format != "" && format != "table" && format != "json" {
		return errs.Usage(errs.Errorf("status: invalid --format %q (want table|json)", format))
	}

	t, err := loadAzureTarget(c, "status")
	if err != nil {
		return err
	}
	slog.Debug("status: start", "env", t.Env, "dir", t.Dir, "format", format)

	view, err := loadStatusView(ctx, t)
	if err != nil {
		return err
	}
	if format == "json" {
		return printStatusJSON(view.App)
	}
	return printStatusTable(view, color)
}

type statusView struct {
	Env      string
	App      *azurearm.ContainerApp
	Revs     []azurearm.Revision
	Replicas map[string][]azurearm.Replica
}

func loadStatusView(ctx context.Context, t *azureTarget) (statusView, error) {
	// Fetch the resource and its revisions in parallel — they're
	// independent GETs and the table view needs both.
	g, gctx := errgroup.WithContext(ctx)
	var (
		app  *azurearm.ContainerApp
		revs []azurearm.Revision
	)
	g.Go(func() error {
		slog.Debug("status: fetching container app", "subscription", t.Sub, "rg", t.RG, "app", t.Name)
		got, err := t.CA.Get(gctx, t.Sub, t.RG, t.Name)
		if err != nil {
			return err
		}
		app = got
		return nil
	})
	g.Go(func() error {
		slog.Debug("status: fetching revisions", "subscription", t.Sub, "rg", t.RG, "app", t.Name)
		got, err := t.CA.ListRevisions(gctx, t.Sub, t.RG, t.Name)
		if err != nil {
			return errs.Wrap(err, "list revisions")
		}
		revs = got
		return nil
	})
	if err := g.Wait(); err != nil {
		if errors.Is(err, azureapi.ErrContainerAppNotFound) {
			return statusView{}, errs.Usage(errs.Errorf("status: app %q not found in %s (not deployed yet?)", t.Name, t.RG))
		}
		return statusView{}, errs.System(errs.Wrap(err, "status"))
	}

	// Replica fan-out — first error cancels the rest via gctx, and the
	// wrapped error includes the offending revision name so triage
	// doesn't have to guess which call site failed.
	names := revisionsForReplicaStatus(app, revs)
	replicas := make(map[string][]azurearm.Replica, len(names))
	var mu sync.Mutex

	rg, rgctx := errgroup.WithContext(ctx)
	for _, name := range names {
		name := name
		rg.Go(func() error {
			rs, err := t.CA.ListReplicas(rgctx, t.Sub, t.RG, t.Name, name)
			if err != nil {
				return errs.Wrapf(err, "list replicas for revision %q", name)
			}
			mu.Lock()
			replicas[name] = rs
			mu.Unlock()
			return nil
		})
	}
	if err := rg.Wait(); err != nil {
		return statusView{}, errs.System(errs.Wrap(err, "status"))
	}
	return statusView{Env: t.Env, App: app, Revs: revs, Replicas: replicas}, nil
}

func revisionsForReplicaStatus(app *azurearm.ContainerApp, revs []azurearm.Revision) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if app != nil {
		add(app.Properties.LatestRevisionName)
		add(app.Properties.LatestReadyRevisionName)
	}
	for _, rev := range revs {
		if rev.Properties.Active || rev.Properties.TrafficWeight > 0 {
			add(rev.Name)
		}
	}
	return out
}

// printStatusTable emits a compact human-oriented summary.
func printStatusTable(view statusView, color bool) error {
	app := view.App
	if app == nil {
		return nil
	}
	printStatusSection("App")
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Name:\t%s\n", app.Name)
	fmt.Fprintf(tw, "Env:\t%s\n", view.Env)
	fmt.Fprintf(tw, "Location:\t%s\n", app.Location)
	if rev := app.Properties.LatestRevisionName; rev != "" {
		fmt.Fprintf(tw, "Latest revision:\t%s\n", rev)
	}
	if rev := app.Properties.LatestReadyRevisionName; rev != "" {
		fmt.Fprintf(tw, "Ready revision:\t%s\n", rev)
	}
	if ps := app.Properties.ProvisioningState; ps != "" {
		fmt.Fprintf(tw, "Provisioning:\t%s\n", colorStatus(ps, color))
	}
	if rs := app.Properties.RunningStatus; rs != "" {
		fmt.Fprintf(tw, "Running:\t%s\n", colorStatus(rs, color))
	}
	if mode := app.Properties.Configuration.ActiveRevisionsMode; mode != "" {
		fmt.Fprintf(tw, "RevisionsMode:\t%s\n", mode)
	}
	if err := tw.Flush(); err != nil {
		return errs.System(errs.Wrap(err, "status: flush"))
	}

	printNetworkSection(app)
	printReplicasSection(app, view.Revs, view.Replicas, color)
	printVolumesSection(app)
	printRegistrySection(app)

	if cs := app.Properties.Template.Containers; len(cs) > 0 {
		fmt.Println()
		fmt.Println("Containers:")
		for _, c := range cs {
			printContainerSummary(c, "  ")
		}
	}
	if ics := app.Properties.Template.InitContainers; len(ics) > 0 {
		fmt.Println()
		fmt.Println("Init containers:")
		for _, c := range ics {
			printContainerSummary(c, "  ")
		}
	}
	return nil
}

func printStatusSection(name string) {
	fmt.Println()
	fmt.Println(name + ":")
}

func printNetworkSection(app *azurearm.ContainerApp) {
	ing := app.Properties.Configuration.Ingress
	if ing == nil {
		// Worker-style apps with no ingress: drop the whole section
		// rather than emit "Ingress: none" noise.
		return
	}
	printStatusSection("Network")
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	visibility := "internal"
	if ing.External {
		visibility = "external"
	}
	fmt.Fprintf(tw, "  Ingress:\t%s\n", visibility)
	if ing.FQDN != "" {
		fmt.Fprintf(tw, "  URL:\t%s://%s\n", ingressScheme(ing), ing.FQDN)
	}
	if ing.TargetPort > 0 || ing.Transport != "" {
		fmt.Fprintf(tw, "  Target:\tport=%d transport=%s\n", ing.TargetPort, stringOr(ing.Transport, "-"))
	}
	fmt.Fprintf(tw, "  Insecure HTTP:\t%s\n", boolWord(ing.AllowInsecure))
	if len(ing.IPSecurityRestrictions) > 0 {
		fmt.Fprintf(tw, "  IP restrictions:\t%d\n", len(ing.IPSecurityRestrictions))
	}
	if ing.CorsPolicy != nil {
		fmt.Fprintf(tw, "  CORS:\t%s\n", formatCORS(ing.CorsPolicy))
	}
	_ = tw.Flush()
	if len(ing.IPSecurityRestrictions) > 0 {
		fmt.Println("  IP rules:")
		for _, r := range ing.IPSecurityRestrictions {
			fmt.Printf("    %s %s %s\n", displayName(r.Name), r.Action, r.IPAddressRange)
		}
	}
	if len(ing.Traffic) > 0 {
		fmt.Println("  Traffic:")
		for _, t := range ing.Traffic {
			target := t.RevisionName
			if t.LatestRevision {
				target = "latest"
			}
			label := ""
			if t.Label != "" {
				label = fmt.Sprintf(" (%s)", t.Label)
			}
			fmt.Printf("    %3d%% -> %s%s\n", t.Weight, target, label)
		}
	}
}

func printReplicasSection(app *azurearm.ContainerApp, revs []azurearm.Revision, replicas map[string][]azurearm.Replica, color bool) {
	printStatusSection("Replicas")
	if s := app.Properties.Template.Scale; s != nil {
		fmt.Printf("  Scale: min=%d max=%d", s.MinReplicas, s.MaxReplicas)
		if s.CooldownPeriod > 0 {
			fmt.Printf(" cooldown=%ds", s.CooldownPeriod)
		}
		if s.PollingInterval > 0 {
			fmt.Printf(" polling=%ds", s.PollingInterval)
		}
		if len(s.Rules) > 0 {
			fmt.Printf(" rules=%d", len(s.Rules))
		}
		fmt.Println()
	}
	if app.Properties.LatestRevisionName != "" && app.Properties.LatestReadyRevisionName != "" &&
		app.Properties.LatestRevisionName != app.Properties.LatestReadyRevisionName {
		fmt.Printf("  Rollout: latest %s is not ready; serving %s\n",
			app.Properties.LatestRevisionName, app.Properties.LatestReadyRevisionName)
	}
	if len(revs) == 0 {
		fmt.Println("  Revisions: none")
		return
	}
	fmt.Println("  Revisions:")
	for _, rev := range revs {
		if !revisionWorthShowing(app, rev) {
			continue
		}
		rs := replicas[rev.Name]
		fmt.Printf("    %s  traffic=%s desired=%d observed=%d ready=%d/%d state=%s health=%s provisioning=%s\n",
			rev.Name,
			formatTrafficWeight(rev.Properties.TrafficWeight),
			rev.Properties.Replicas,
			len(rs),
			readyReplicaCount(rs), len(rs),
			stringOr(rev.Properties.RunningState, "-"),
			stringOr(rev.Properties.HealthState, "-"),
			stringOr(rev.Properties.ProvisioningState, "-"),
		)
		for _, replica := range rs {
			fmt.Printf("      - %s running=%s containers=%s\n",
				replica.Name,
				stringOr(replica.Properties.RunningState, "-"),
				formatReplicaContainers(replica.Properties.Containers))
		}
	}
}

func printVolumesSection(app *azurearm.ContainerApp) {
	vols := app.Properties.Template.Volumes
	if len(vols) == 0 {
		return
	}
	printStatusSection("Volumes")
	mounts := volumeMountIndex(app)
	for _, v := range vols {
		storage := ""
		if v.StorageName != "" {
			storage = " storage=" + v.StorageName
		}
		fmt.Printf("  %s  %s%s\n", displayName(v.Name), stringOr(v.StorageType, "-"), storage)
		if len(mounts[v.Name]) == 0 {
			fmt.Println("    unmounted")
			continue
		}
		for _, m := range mounts[v.Name] {
			fmt.Printf("    %s: %s\n", m.Container, m.Path)
		}
	}
}

func printRegistrySection(app *azurearm.ContainerApp) {
	if len(app.Properties.Configuration.Registries) == 0 {
		return
	}
	printStatusSection("Registry")
	for _, r := range app.Properties.Configuration.Registries {
		auth := "anonymous"
		switch {
		case r.Identity != "":
			auth = "identity=" + r.Identity
		case r.Username != "":
			auth = "username=" + r.Username
		}
		fmt.Printf("  %s  %s\n", r.Server, auth)
	}
}

func ingressScheme(ing *azurearm.Ingress) string {
	if ing != nil && !ing.External && ing.AllowInsecure {
		return "http"
	}
	return "https"
}

func boolWord(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func formatCORS(c *azurearm.CorsPolicy) string {
	if c == nil {
		return "none"
	}
	return fmt.Sprintf("origins=%s methods=%s credentials=%s",
		sliceSummary(c.AllowedOrigins),
		sliceSummary(c.AllowedMethods),
		boolWord(c.AllowCredentials))
}

func sliceSummary(values []string) string {
	switch len(values) {
	case 0:
		return "-"
	case 1:
		return values[0]
	default:
		return fmt.Sprintf("%d", len(values))
	}
}

func revisionWorthShowing(app *azurearm.ContainerApp, rev azurearm.Revision) bool {
	return rev.Name == app.Properties.LatestRevisionName ||
		rev.Name == app.Properties.LatestReadyRevisionName ||
		rev.Properties.Active ||
		rev.Properties.TrafficWeight > 0
}

func formatTrafficWeight(weight int) string {
	if weight <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", weight)
}

func readyReplicaCount(replicas []azurearm.Replica) int {
	ready := 0
	for _, r := range replicas {
		if replicaAllContainersReady(r) {
			ready++
		}
	}
	return ready
}

func formatReplicaContainers(containers []azurearm.ReplicaContainer) string {
	if len(containers) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(containers))
	for _, c := range containers {
		state := "not-ready"
		if c.Ready {
			state = "ready"
		}
		if c.RestartCount > 0 {
			state += fmt.Sprintf("/restarts=%d", c.RestartCount)
		}
		parts = append(parts, displayName(c.Name)+":"+state)
	}
	return strings.Join(parts, ", ")
}

type statusVolumeMount struct {
	Container string
	Path      string
}

func volumeMountIndex(app *azurearm.ContainerApp) map[string][]statusVolumeMount {
	out := map[string][]statusVolumeMount{}
	add := func(container string, mounts []azurearm.VolumeMount) {
		for _, m := range mounts {
			path := m.MountPath
			if m.SubPath != "" {
				path += " subPath=" + m.SubPath
			}
			out[m.VolumeName] = append(out[m.VolumeName], statusVolumeMount{
				Container: displayName(container),
				Path:      path,
			})
		}
	}
	for _, c := range app.Properties.Template.InitContainers {
		add(c.Name+" (init)", c.VolumeMounts)
	}
	for _, c := range app.Properties.Template.Containers {
		add(c.Name, c.VolumeMounts)
	}
	return out
}

func colorStatus(s string, color bool) string {
	switch strings.ToLower(s) {
	case "succeeded", "running", "ready", "healthy", "true":
		return colorize(s, stylePass, color)
	case "failed", "error", "unhealthy", "canceled", "cancelled", "false":
		return colorize(s, styleFail, color)
	case "inprogress", "in progress", "pending", "accepted", "starting", "processing", "updating":
		return colorize(s, styleWarn, color)
	case "inactive", "stopped", "-", "unknown":
		return colorize(s, styleSkip, color)
	default:
		return s
	}
}

// printContainerSummary renders a per-container block for the status
// table. Indented with `prefix` to allow consistent rendering under
// either the Containers: or Init containers: header. Designed to
// answer "what's actually running in this app right now?":
//
//   - image (the load-bearing answer to most status questions)
//   - resource budget
//   - env-var surface — counts by kind so secret refs are visible
//     without dumping values
//   - probe summary (type + endpoint shape)
//   - mount points
//   - command/args overrides if set (image defaults are interesting
//     by their absence, but explicit overrides change behavior)
func printContainerSummary(c azurearm.Container, prefix string) {
	fmt.Printf("%s%s\n", prefix, displayName(c.Name))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	indent := prefix + "  "
	fmt.Fprintf(tw, "%sImage:\t%s\n", indent, fallbackString(c.Image, "—"))
	if r := c.Resources; r != nil {
		fmt.Fprintf(tw, "%sResources:\t%s\n", indent, formatResources(r))
	}
	if env := c.Env; len(env) > 0 {
		fmt.Fprintf(tw, "%sEnv:\t%s\n", indent, formatEnvCounts(env))
	}
	if mounts := c.VolumeMounts; len(mounts) > 0 {
		fmt.Fprintf(tw, "%sMounts:\t%s\n", indent, formatMounts(mounts))
	}
	if probes := c.Probes; len(probes) > 0 {
		fmt.Fprintf(tw, "%sProbes:\t%s\n", indent, formatProbes(probes))
	}
	if len(c.Command) > 0 {
		fmt.Fprintf(tw, "%sCommand:\t%s\n", indent, strings.Join(c.Command, " "))
	}
	if len(c.Args) > 0 {
		fmt.Fprintf(tw, "%sArgs:\t%s\n", indent, strings.Join(c.Args, " "))
	}
	if c.WorkingDir != "" {
		fmt.Fprintf(tw, "%sWorkingDir:\t%s\n", indent, c.WorkingDir)
	}
	_ = tw.Flush()
}

func displayName(name string) string {
	if name == "" {
		return "<unnamed>"
	}
	return name
}

func fallbackString(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// formatResources collapses a Resources block into "0.5 CPU / 1Gi".
func formatResources(r *azurearm.Resources) string {
	cpu := fmt.Sprintf("%g", r.CPU)
	mem := r.Memory
	if mem == "" {
		mem = "—"
	}
	return cpu + " CPU / " + mem
}

// formatEnvCounts breaks env vars into "N plain, M secret refs" so
// callers see at a glance how many secrets are wired in. Values are
// never printed (would leak plaintext-config or, via secretRef, leak
// the Key Vault secret name to anyone reading status).
func formatEnvCounts(env []azurearm.EnvVar) string {
	plain, secret := 0, 0
	for _, e := range env {
		if e.SecretRef != "" {
			secret++
		} else {
			plain++
		}
	}
	switch {
	case plain > 0 && secret > 0:
		return fmt.Sprintf("%d plain, %d secret ref(s)", plain, secret)
	case secret > 0:
		return fmt.Sprintf("%d secret ref(s)", secret)
	case plain > 0:
		return fmt.Sprintf("%d plain", plain)
	default:
		return "—"
	}
}

// formatMounts renders volumes as "path (volumeName)[, path (volumeName)]".
func formatMounts(mounts []azurearm.VolumeMount) string {
	parts := make([]string, 0, len(mounts))
	for _, m := range mounts {
		parts = append(parts, fmt.Sprintf("%s (%s)", m.MountPath, m.VolumeName))
	}
	return strings.Join(parts, ", ")
}

// formatProbes summarises probes per type with endpoint + port. The
// probe slice can include up to three (Liveness, Readiness, Startup);
// rendering each as a short clause is more useful than a count.
func formatProbes(probes []azurearm.Probe) string {
	parts := make([]string, 0, len(probes))
	for _, p := range probes {
		var endpoint string
		switch {
		case p.HTTPGet != nil:
			endpoint = fmt.Sprintf("GET :%d%s", p.HTTPGet.Port, p.HTTPGet.Path)
		case p.TCPSocket != nil:
			endpoint = fmt.Sprintf("TCP :%d", p.TCPSocket.Port)
		default:
			endpoint = "(unknown action)"
		}
		typ := p.Type
		if typ == "" {
			typ = "Probe"
		}
		parts = append(parts, fmt.Sprintf("%s %s", typ, endpoint))
	}
	return strings.Join(parts, "; ")
}

// printStatusJSON emits the raw ARM body as pretty-printed JSON. We
// intentionally don't filter read-only fields here — the whole point
// of status is to see what Azure reports.
func printStatusJSON(app *azurearm.ContainerApp) error {
	out, err := json.MarshalIndent(app, "", "  ")
	if err != nil {
		return errs.System(errs.Wrap(err, "status: json marshal"))
	}
	fmt.Println(string(out))
	return nil
}

package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/errs"
)

// streamLogsOptions configures a streamContainerLogs invocation. All
// fields except Out are optional; reasonable defaults match the `lazure
// logs` command's defaults.
type streamLogsOptions struct {
	Container string    // "" → first non-init container of Replica
	Replica   string    // "" → first replica returned
	Follow    bool      // keep connection open for live output
	Tail      int       // historical lines before live (0 = none)
	Raw       bool      // print lines verbatim (no JSON parsing / color)
	Color     bool      // apply ANSI colors when Raw=false
	Out       io.Writer // destination; callers usually pass os.Stdout
}

// streamContainerLogs resolves the target replica + container, fetches
// an auth token, and pumps formatted lines to opts.Out. Used by both
// `lazure logs` (one-shot or follow) and `lazure restart --wait --logs`
// (follow-only, pinned to a specific replica).
//
// On ctx cancellation StreamLogs returns context.Canceled — the caller
// decides whether to treat that as clean shutdown (the usual case) or
// bubble up.
func streamContainerLogs(ctx context.Context, ca *azureapi.ContainerAppsClient,
	sub, rg, name, revision string, opts streamLogsOptions) error {
	if opts.Out == nil {
		return errs.Errorf("streamContainerLogs: opts.Out is required")
	}

	replicas, err := ca.ListReplicas(ctx, sub, rg, name, revision)
	if err != nil {
		return errs.Wrap(err, "logstream: list replicas")
	}
	r, err := pickReplica(replicas, opts.Replica)
	if err != nil {
		return err
	}
	cnt, err := pickContainer(r, opts.Container)
	if err != nil {
		return err
	}
	if cnt.LogStreamEndpoint == "" {
		return errs.Errorf("logstream: container %q on replica %q has no logStreamEndpoint", cnt.Name, r.Name)
	}

	token, _, err := ca.GetAuthToken(ctx, sub, rg, name)
	if err != nil {
		return errs.Wrap(err, "logstream: getAuthToken")
	}

	return azureapi.StreamLogs(ctx, cnt.LogStreamEndpoint, token,
		azureapi.LogStreamOptions{Follow: opts.Follow, Tail: opts.Tail},
		func(line string) { fmt.Fprintln(opts.Out, formatLogLine(line, opts.Raw, opts.Color)) })
}

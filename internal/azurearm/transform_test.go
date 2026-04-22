package azurearm

import (
	"testing"

	"github.com/investerra/lazure/internal/lazurecfg"
)

func baseManifest() *lazurecfg.Manifest {
	return &lazurecfg.Manifest{
		App: lazurecfg.App{
			Name:                 "api-server",
			Location:             "switzerlandnorth",
			ResourceGroup:        "dev-rg",
			ManagedEnvironmentID: "/subs/x/managedEnvironments/env",
			Identity:             "/subs/x/rg/y/identities/api-server",
		},
		Containers: []lazurecfg.Container{
			{
				Name:      "app",
				Image:     "acr.io/app:v1",
				Resources: &lazurecfg.Resources{CPU: 0.5, Memory: "1Gi"},
			},
		},
	}
}

func TestTransform_MinimalApp(t *testing.T) {
	m := baseManifest()
	app, err := Transform(m, TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if app.Type != "Microsoft.App/containerApps" {
		t.Errorf("type = %q", app.Type)
	}
	if app.Location != "switzerlandnorth" {
		t.Errorf("location = %q", app.Location)
	}
	if app.Name != "api-server" {
		t.Errorf("name = %q", app.Name)
	}
	if app.Properties.ManagedEnvironmentID != "/subs/x/managedEnvironments/env" {
		t.Errorf("managedEnvironmentId = %q", app.Properties.ManagedEnvironmentID)
	}
}

// ---------- identity ----------

func TestTransform_Identity(t *testing.T) {
	m := baseManifest()
	app, _ := Transform(m, TransformOptions{})
	if app.Identity == nil {
		t.Fatal("identity nil")
	}
	if app.Identity.Type != "UserAssigned" {
		t.Errorf("identity.type = %q", app.Identity.Type)
	}
	if _, has := app.Identity.UserAssignedIdentities["/subs/x/rg/y/identities/api-server"]; !has {
		t.Errorf("identity map missing expected key: %+v", app.Identity.UserAssignedIdentities)
	}
}

func TestTransform_Identity_Empty(t *testing.T) {
	m := baseManifest()
	m.App.Identity = ""
	app, _ := Transform(m, TransformOptions{})
	if app.Identity != nil {
		t.Errorf("expected nil identity, got %+v", app.Identity)
	}
}

// ---------- scale defaults ----------

func TestTransform_Scale_OmittedDefaultsToOneOne(t *testing.T) {
	m := baseManifest() // no scale
	app, _ := Transform(m, TransformOptions{})
	s := app.Properties.Template.Scale
	if s == nil {
		t.Fatal("scale nil")
	}
	if s.MinReplicas != 1 || s.MaxReplicas != 1 {
		t.Errorf("scale defaults = {%d,%d}, want {1,1}", s.MinReplicas, s.MaxReplicas)
	}
}

func TestTransform_Scale_Explicit(t *testing.T) {
	m := baseManifest()
	m.Scale = &lazurecfg.Scale{Min: 1, Max: 3, CooldownPeriod: 300, PollingInterval: 30}
	app, _ := Transform(m, TransformOptions{})
	s := app.Properties.Template.Scale
	if s.MinReplicas != 1 || s.MaxReplicas != 3 {
		t.Errorf("min/max = %d/%d", s.MinReplicas, s.MaxReplicas)
	}
	if s.CooldownPeriod != 300 || s.PollingInterval != 30 {
		t.Errorf("cooldown/polling = %d/%d", s.CooldownPeriod, s.PollingInterval)
	}
}

// ---------- scale rules ----------

func TestTransform_Scale_HTTPRule(t *testing.T) {
	m := baseManifest()
	m.Scale = &lazurecfg.Scale{Min: 1, Max: 10, Rules: []lazurecfg.ScaleRule{{
		Name: "http-r",
		Http: &lazurecfg.HTTPScaler{ConcurrentRequests: 10},
	}}}
	app, _ := Transform(m, TransformOptions{})
	rule := app.Properties.Template.Scale.Rules[0]
	if rule.Name != "http-r" {
		t.Errorf("name = %q", rule.Name)
	}
	if rule.HTTP == nil || rule.HTTP.Metadata["concurrentRequests"] != "10" {
		t.Errorf("http rule = %+v", rule.HTTP)
	}
}

func TestTransform_Scale_HTTPRule_OmitsZeroMetadata(t *testing.T) {
	// Zero concurrentRequests = unspecified; emit empty metadata rather
	// than "concurrentRequests: 0" which ACA rejects.
	m := baseManifest()
	m.Scale = &lazurecfg.Scale{Min: 1, Max: 10, Rules: []lazurecfg.ScaleRule{{
		Name: "http-r",
		Http: &lazurecfg.HTTPScaler{ConcurrentRequests: 0},
	}}}
	app, _ := Transform(m, TransformOptions{})
	rule := app.Properties.Template.Scale.Rules[0]
	if _, has := rule.HTTP.Metadata["concurrentRequests"]; has {
		t.Errorf("zero ConcurrentRequests should not emit metadata key, got %+v", rule.HTTP.Metadata)
	}
}

func TestTransform_Scale_CPURuleBecomesCustom(t *testing.T) {
	m := baseManifest()
	m.Scale = &lazurecfg.Scale{Min: 1, Max: 10, Rules: []lazurecfg.ScaleRule{{
		Name: "cpu-r",
		CPU:  &lazurecfg.MetricScaler{Type: "Utilization", Value: 70},
	}}}
	app, _ := Transform(m, TransformOptions{})
	rule := app.Properties.Template.Scale.Rules[0]
	if rule.Custom == nil {
		t.Fatalf("cpu rule not emitted as custom: %+v", rule)
	}
	if rule.Custom.Type != "cpu" {
		t.Errorf("custom.type = %q", rule.Custom.Type)
	}
	if rule.Custom.Metadata["value"] != "70" {
		t.Errorf("custom.metadata.value = %q", rule.Custom.Metadata["value"])
	}
}

func TestTransform_Scale_AzureQueueAutoIdentity(t *testing.T) {
	m := baseManifest()
	m.Scale = &lazurecfg.Scale{Min: 1, Max: 10, Rules: []lazurecfg.ScaleRule{{
		Name: "q-r",
		AzureQueue: &lazurecfg.AzureQueueScaler{
			QueueName: "tasks", QueueLength: 5, AccountName: "storage",
		},
	}}}
	app, _ := Transform(m, TransformOptions{})
	rule := app.Properties.Template.Scale.Rules[0]
	if rule.AzureQueue == nil {
		t.Fatal("azureQueue rule missing")
	}
	if rule.AzureQueue.Identity != "/subs/x/rg/y/identities/api-server" {
		t.Errorf("azureQueue.identity = %q, want app.identity default", rule.AzureQueue.Identity)
	}
}

func TestTransform_Scale_RuleAuth_IdentityKeyword(t *testing.T) {
	m := baseManifest()
	m.Scale = &lazurecfg.Scale{Min: 1, Max: 10, Rules: []lazurecfg.ScaleRule{{
		Name: "q-r",
		AzureQueue: &lazurecfg.AzureQueueScaler{
			QueueName: "tasks", QueueLength: 5, AccountName: "storage",
		},
		Auth: &lazurecfg.RuleAuth{Identity: "app"}, // keyword → app.identity
	}}}
	app, _ := Transform(m, TransformOptions{})
	if got := app.Properties.Template.Scale.Rules[0].AzureQueue.Identity; got != "/subs/x/rg/y/identities/api-server" {
		t.Errorf("identity 'app' should resolve to app.identity, got %q", got)
	}
}

func TestTransform_Scale_RuleAuth_Secrets(t *testing.T) {
	m := baseManifest()
	m.Scale = &lazurecfg.Scale{Min: 1, Max: 10, Rules: []lazurecfg.ScaleRule{{
		Name: "custom-r",
		Custom: &lazurecfg.CustomScaler{Type: "redis"},
		Auth: &lazurecfg.RuleAuth{
			Secrets: []lazurecfg.RuleSecretAuth{
				{Param: "password", Secret: "redis-pw"},
			},
		},
	}}}
	app, _ := Transform(m, TransformOptions{})
	rule := app.Properties.Template.Scale.Rules[0]
	if len(rule.Custom.Auth) != 1 {
		t.Fatalf("auth = %+v", rule.Custom.Auth)
	}
	auth := rule.Custom.Auth[0]
	if auth.TriggerParameter != "password" || auth.SecretRef != "redis-pw" {
		t.Errorf("auth = %+v", auth)
	}
}

// ---------- env resolution + ARM array shape ----------

func TestTransform_EnvVars_SortedAlphabetical(t *testing.T) {
	m := baseManifest()
	m.Env = map[string]*lazurecfg.EnvValue{
		"ZZZ":  {Value: "z"},
		"AAA":  {Value: "a"},
		"MMMM": {Value: "m"},
	}
	app, _ := Transform(m, TransformOptions{})
	env := app.Properties.Template.Containers[0].Env
	if len(env) != 3 {
		t.Fatalf("env len = %d", len(env))
	}
	if env[0].Name != "AAA" || env[1].Name != "MMMM" || env[2].Name != "ZZZ" {
		t.Errorf("env not alphabetical: %+v", env)
	}
}

func TestTransform_EnvVars_ValueAndSecretRef(t *testing.T) {
	m := baseManifest()
	m.Env = map[string]*lazurecfg.EnvValue{
		"PLAIN":       {Value: "v1"},
		"DB":          {SecretRef: "db-url"},
	}
	app, _ := Transform(m, TransformOptions{})
	env := app.Properties.Template.Containers[0].Env
	var plain, db EnvVar
	for _, e := range env {
		if e.Name == "PLAIN" {
			plain = e
		}
		if e.Name == "DB" {
			db = e
		}
	}
	if plain.Value != "v1" || plain.SecretRef != "" {
		t.Errorf("plain = %+v", plain)
	}
	if db.SecretRef != "db-url" || db.Value != "" {
		t.Errorf("db = %+v", db)
	}
}

func TestTransform_Env_MergeBehavior(t *testing.T) {
	// Exercises ResolveEnv through the transform boundary: shared defaults
	// flow into the container, per-container merge_env overrides.
	m := baseManifest()
	m.Env = map[string]*lazurecfg.EnvValue{
		"LOG_LEVEL":  {Value: "info"},
		"SHARED_KEY": {Value: "shared"},
	}
	m.Containers[0].MergeEnv = map[string]*lazurecfg.EnvValue{
		"LOG_LEVEL":   {Value: "debug"}, // override
		"EXTRA":       {Value: "x"},      // add
	}
	app, _ := Transform(m, TransformOptions{})
	env := app.Properties.Template.Containers[0].Env
	got := make(map[string]string, len(env))
	for _, e := range env {
		got[e.Name] = e.Value
	}
	if got["LOG_LEVEL"] != "debug" {
		t.Errorf("LOG_LEVEL = %q, want 'debug' from merge", got["LOG_LEVEL"])
	}
	if got["SHARED_KEY"] != "shared" {
		t.Errorf("SHARED_KEY = %q, want inherited from shared", got["SHARED_KEY"])
	}
	if got["EXTRA"] != "x" {
		t.Errorf("EXTRA = %q, want 'x'", got["EXTRA"])
	}
}

// ---------- secrets stanza auto-gen ----------

func TestTransform_Secrets_AutoGeneratedFromEnvRefs(t *testing.T) {
	m := baseManifest()
	m.Env = map[string]*lazurecfg.EnvValue{
		"DB":    {SecretRef: "db-url"},
		"REDIS": {SecretRef: "redis-url"},
	}
	m.Containers[0].MergeEnv = map[string]*lazurecfg.EnvValue{
		"TOKEN": {SecretRef: "api-token"},
	}
	app, _ := Transform(m, TransformOptions{
		VaultURL: "https://kv-test.vault.azure.net",
	})
	names := make([]string, 0, len(app.Properties.Configuration.Secrets))
	for _, s := range app.Properties.Configuration.Secrets {
		names = append(names, s.Name)
	}
	// alphabetical
	want := []string{"api-token", "db-url", "redis-url"}
	if !equalSlice(names, want) {
		t.Errorf("secrets = %v, want %v", names, want)
	}
	for _, s := range app.Properties.Configuration.Secrets {
		if s.KeyVaultURL != "https://kv-test.vault.azure.net/secrets/"+s.Name {
			t.Errorf("keyVaultUrl wrong for %s: %q", s.Name, s.KeyVaultURL)
		}
		if s.Identity != "/subs/x/rg/y/identities/api-server" {
			t.Errorf("identity wrong for %s: %q", s.Name, s.Identity)
		}
	}
}

func TestTransform_Secrets_IncludesRegistryAndScaleRefs(t *testing.T) {
	m := baseManifest()
	m.Registries = []lazurecfg.Registry{
		{Server: "ghcr.io", Username: "bot", PasswordSecret: "ghcr-pull-token"},
	}
	m.Scale = &lazurecfg.Scale{Min: 1, Max: 1, Rules: []lazurecfg.ScaleRule{{
		Name: "r",
		Custom: &lazurecfg.CustomScaler{Type: "redis"},
		Auth: &lazurecfg.RuleAuth{
			Secrets: []lazurecfg.RuleSecretAuth{
				{Param: "password", Secret: "redis-password"},
			},
		},
	}}}
	app, _ := Transform(m, TransformOptions{VaultURL: "https://kv.vault.azure.net"})
	names := make(map[string]bool)
	for _, s := range app.Properties.Configuration.Secrets {
		names[s.Name] = true
	}
	if !names["ghcr-pull-token"] {
		t.Errorf("ghcr-pull-token not in secrets: %v", names)
	}
	if !names["redis-password"] {
		t.Errorf("redis-password not in secrets: %v", names)
	}
}

func TestTransform_Secrets_Deduplicated(t *testing.T) {
	m := baseManifest()
	m.Env = map[string]*lazurecfg.EnvValue{"A": {SecretRef: "shared"}}
	m.Containers[0].MergeEnv = map[string]*lazurecfg.EnvValue{"B": {SecretRef: "shared"}}
	app, _ := Transform(m, TransformOptions{VaultURL: "https://kv"})
	if len(app.Properties.Configuration.Secrets) != 1 {
		t.Errorf("secrets = %+v, want 1 (deduplicated)", app.Properties.Configuration.Secrets)
	}
}

func TestTransform_Secrets_NoReferences(t *testing.T) {
	m := baseManifest()
	app, _ := Transform(m, TransformOptions{VaultURL: "https://kv"})
	if app.Properties.Configuration.Secrets != nil {
		t.Errorf("expected nil secrets, got %v", app.Properties.Configuration.Secrets)
	}
}

// ---------- registries ----------

func TestTransform_Registries_ACRAutoIdentity(t *testing.T) {
	m := baseManifest()
	m.Registries = []lazurecfg.Registry{{Server: "acr.io"}}
	app, _ := Transform(m, TransformOptions{})
	r := app.Properties.Configuration.Registries[0]
	if r.Identity != "/subs/x/rg/y/identities/api-server" {
		t.Errorf("ACR identity not auto-filled: %q", r.Identity)
	}
}

func TestTransform_Registries_BasicAuth(t *testing.T) {
	m := baseManifest()
	m.Registries = []lazurecfg.Registry{
		{Server: "ghcr.io", Username: "bot", PasswordSecret: "pw"},
	}
	app, _ := Transform(m, TransformOptions{})
	r := app.Properties.Configuration.Registries[0]
	if r.Username != "bot" || r.PasswordSecretRef != "pw" {
		t.Errorf("basic auth = %+v", r)
	}
	if r.Identity != "" {
		t.Errorf("basic-auth registry should not set identity, got %q", r.Identity)
	}
}

// ---------- ingress ----------

func TestTransform_Ingress_Basic(t *testing.T) {
	m := baseManifest()
	m.Ingress = &lazurecfg.Ingress{
		External: true, TargetPort: 8000, Transport: "auto",
	}
	app, _ := Transform(m, TransformOptions{})
	i := app.Properties.Configuration.Ingress
	if i.Transport != "auto" {
		t.Errorf("transport = %q, want lowercase 'auto'", i.Transport)
	}
}

func TestTransform_Ingress_IPRestrictions_CapitalizedAction(t *testing.T) {
	m := baseManifest()
	m.Ingress = &lazurecfg.Ingress{
		IPRestrictions: []lazurecfg.IPRestriction{
			{Name: "a", CIDR: "10.0.0.0/8", Action: "allow"},
		},
	}
	app, _ := Transform(m, TransformOptions{})
	ip := app.Properties.Configuration.Ingress.IPSecurityRestrictions[0]
	if ip.Action != "Allow" {
		t.Errorf("action = %q, want 'Allow' (ARM PascalCase)", ip.Action)
	}
}

func TestTransform_Ingress_Traffic_ShorthandEmpty(t *testing.T) {
	m := baseManifest()
	m.Ingress = &lazurecfg.Ingress{Traffic: &lazurecfg.Traffic{}}
	app, _ := Transform(m, TransformOptions{})
	tr := app.Properties.Configuration.Ingress.Traffic
	if len(tr) != 1 {
		t.Fatalf("traffic = %+v, want 1 entry", tr)
	}
	if !tr[0].LatestRevision || tr[0].Weight != 100 {
		t.Errorf("traffic[0] = %+v, want 100%% latest", tr[0])
	}
	if m := app.Properties.Configuration.ActiveRevisionsMode; m != "Single" {
		t.Errorf("activeRevisionsMode = %q, want Single", m)
	}
}

func TestTransform_Ingress_Traffic_BlueGreen(t *testing.T) {
	m := baseManifest()
	m.Ingress = &lazurecfg.Ingress{Traffic: &lazurecfg.Traffic{
		Latest:   &lazurecfg.TrafficEntry{Weight: 10, Label: "canary"},
		Previous: &lazurecfg.TrafficEntry{Weight: 90, Label: "stable"},
	}}
	app, _ := Transform(m, TransformOptions{PreviousRevision: "api-server--abc123"})
	tr := app.Properties.Configuration.Ingress.Traffic
	if len(tr) != 2 {
		t.Fatalf("traffic = %+v, want 2 entries", tr)
	}
	if !tr[0].LatestRevision || tr[0].Weight != 10 || tr[0].Label != "canary" {
		t.Errorf("latest entry = %+v", tr[0])
	}
	if tr[1].RevisionName != "api-server--abc123" || tr[1].Weight != 90 {
		t.Errorf("previous entry = %+v", tr[1])
	}
	if m := app.Properties.Configuration.ActiveRevisionsMode; m != "Multiple" {
		t.Errorf("activeRevisionsMode = %q, want Multiple", m)
	}
}

func TestTransform_Ingress_Traffic_FirstDeploy_DropsPrevious(t *testing.T) {
	// previous is set in manifest but no deployed revision yet →
	// drop previous, latest gets 100%, mode stays Single.
	m := baseManifest()
	m.Ingress = &lazurecfg.Ingress{Traffic: &lazurecfg.Traffic{
		Latest:   &lazurecfg.TrafficEntry{Weight: 10},
		Previous: &lazurecfg.TrafficEntry{Weight: 90},
	}}
	app, _ := Transform(m, TransformOptions{PreviousRevision: ""})
	tr := app.Properties.Configuration.Ingress.Traffic
	if len(tr) != 1 {
		t.Fatalf("expected 1 entry on first deploy, got %+v", tr)
	}
	if tr[0].Weight != 100 {
		t.Errorf("weight = %d, want 100 on first deploy", tr[0].Weight)
	}
	if m := app.Properties.Configuration.ActiveRevisionsMode; m != "Single" {
		t.Errorf("mode = %q, want Single", m)
	}
}

// ---------- volumes ----------

func TestTransform_Volumes_TypeCapitalization(t *testing.T) {
	m := baseManifest()
	m.Volumes = []lazurecfg.Volume{
		{Name: "cache", Type: "empty_dir"},
		{Name: "up", Type: "azure_file", StorageName: "store"},
		{Name: "nfs", Type: "nfs_azure_file", StorageName: "nfsstore"},
		{Name: "sec", Type: "secret"},
	}
	app, _ := Transform(m, TransformOptions{})
	vols := app.Properties.Template.Volumes
	want := map[string]string{
		"cache": "EmptyDir",
		"up":    "AzureFile",
		"nfs":   "NfsAzureFile",
		"sec":   "Secret",
	}
	for _, v := range vols {
		if w, ok := want[v.Name]; !ok || v.StorageType != w {
			t.Errorf("volume %q: storageType=%q want %q", v.Name, v.StorageType, w)
		}
	}
}

func TestTransform_VolumeMounts(t *testing.T) {
	m := baseManifest()
	m.Volumes = []lazurecfg.Volume{{Name: "cache", Type: "empty_dir"}}
	m.Containers[0].VolumeMounts = []lazurecfg.VolumeMount{
		{Name: "cache", Path: "/var/cache", SubPath: "tenant-a"},
	}
	app, _ := Transform(m, TransformOptions{})
	mnt := app.Properties.Template.Containers[0].VolumeMounts[0]
	if mnt.VolumeName != "cache" || mnt.MountPath != "/var/cache" || mnt.SubPath != "tenant-a" {
		t.Errorf("mount = %+v", mnt)
	}
}

// ---------- probes ----------

func TestTransform_Probes_ArrayWithType(t *testing.T) {
	m := baseManifest()
	m.Containers[0].Probes = &lazurecfg.Probes{
		Liveness: &lazurecfg.Probe{
			HTTP:         &lazurecfg.HTTPProbe{Path: "/health", Port: 8000, Scheme: "http"},
			InitialDelay: 10, Period: 30,
		},
		Readiness: &lazurecfg.Probe{TCP: &lazurecfg.TCPProbe{Port: 8000}},
	}
	app, _ := Transform(m, TransformOptions{})
	probes := app.Properties.Template.Containers[0].Probes
	if len(probes) != 2 {
		t.Fatalf("probes = %+v", probes)
	}
	var live, ready Probe
	for _, p := range probes {
		switch p.Type {
		case "Liveness":
			live = p
		case "Readiness":
			ready = p
		}
	}
	if live.HTTPGet == nil || live.HTTPGet.Scheme != "HTTP" {
		t.Errorf("liveness scheme uppercase expected: %+v", live.HTTPGet)
	}
	if live.InitialDelaySeconds != 10 || live.PeriodSeconds != 30 {
		t.Errorf("liveness timings = %+v", live)
	}
	if ready.TCPSocket == nil || ready.TCPSocket.Port != 8000 {
		t.Errorf("readiness = %+v", ready)
	}
}

func TestTransform_InitContainers_StripProbes(t *testing.T) {
	m := baseManifest()
	m.InitContainers = []lazurecfg.Container{{
		Name: "init", Image: "x",
		Probes: &lazurecfg.Probes{Liveness: &lazurecfg.Probe{HTTP: &lazurecfg.HTTPProbe{Path: "/", Port: 80}}},
	}}
	app, _ := Transform(m, TransformOptions{})
	if len(app.Properties.Template.InitContainers) != 1 {
		t.Fatal("init container lost")
	}
	if app.Properties.Template.InitContainers[0].Probes != nil {
		t.Errorf("init container probes not stripped: %+v", app.Properties.Template.InitContainers[0].Probes)
	}
}

// ---------- helpers ----------

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

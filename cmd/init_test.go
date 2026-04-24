package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- parseEnvsCSV ----------

func TestParseEnvsCSV_Basic(t *testing.T) {
	got, err := parseEnvsCSV("dev,uat,prd")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dev", "uat", "prd"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseEnvsCSV_TrimsWhitespace(t *testing.T) {
	got, err := parseEnvsCSV(" dev , uat ,prd ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "dev,uat,prd" {
		t.Errorf("got %v, want trimmed", got)
	}
}

func TestParseEnvsCSV_RejectsDuplicates(t *testing.T) {
	_, err := parseEnvsCSV("dev,dev,prd")
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("want duplicate error, got %v", err)
	}
}

func TestParseEnvsCSV_Empty(t *testing.T) {
	for _, in := range []string{"", " ", ",,", ",  ,"} {
		if _, err := parseEnvsCSV(in); err == nil {
			t.Errorf("%q should be rejected as empty", in)
		}
	}
}

// ---------- validateInitConfig ----------

func TestValidateInitConfig_Happy(t *testing.T) {
	cfg := initConfig{Name: "app", Location: "loc", ResourceGroup: "rg", Envs: []string{"dev"}}
	if err := validateInitConfig(cfg); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateInitConfig_EachMissingField(t *testing.T) {
	base := initConfig{Name: "app", Location: "loc", ResourceGroup: "rg", Envs: []string{"dev"}}

	cases := map[string]initConfig{
		"name":     withName(base, ""),
		"location": withLocation(base, ""),
		"rg":       withRG(base, ""),
		"envs":     withEnvs(base, nil),
	}
	for label, cfg := range cases {
		t.Run(label, func(t *testing.T) {
			if err := validateInitConfig(cfg); err == nil {
				t.Errorf("expected error for missing %s", label)
			}
		})
	}
}

func withName(c initConfig, v string) initConfig     { c.Name = v; return c }
func withLocation(c initConfig, v string) initConfig { c.Location = v; return c }
func withRG(c initConfig, v string) initConfig       { c.ResourceGroup = v; return c }
func withEnvs(c initConfig, v []string) initConfig   { c.Envs = v; return c }

// ---------- renderers ----------

func TestRenderDeployYml_ContainsValues(t *testing.T) {
	cfg := initConfig{Name: "api-server", Location: "switzerlandnorth"}
	got := renderDeployYml(cfg)
	for _, want := range []string{
		"name: api-server", "location: switzerlandnorth",
		"$schema=./deploy.schema.json", // modeline uses local path
		"# ingress:",                   // commented-in example
		"# scale:",                     // same
		"containers:",                  // uncommented
		"{{ .Vars.docker_image }}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderVarsYml_PerEnv_NoInferences(t *testing.T) {
	cfg := initConfig{Name: "api-server", ResourceGroup: "dbx"}
	got := renderVarsYml("dev", cfg, envInference{}, "")
	for _, want := range []string{
		"app_env: dev",
		"resource_group: rg-example-dev", // <env>-<rg> pattern (matches user's real config)
		"TODO",                    // placeholders present
		"yourregistry.azurecr.io", // placeholder ACR
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderVarsYml_WithInferences_InlinesValues(t *testing.T) {
	cfg := initConfig{Name: "example-service", ResourceGroup: "dbx"}
	inf := envInference{
		ManagedEnv: "/subscriptions/SUB/resourceGroups/rg-example-dev/providers/Microsoft.App/managedEnvironments/me-dev",
		Identity:   "/subscriptions/SUB/resourceGroups/rg-example-dev/providers/Microsoft.ManagedIdentity/userAssignedIdentities/mi-dev",
		ACRServer:  "example.azurecr.io",
	}
	got := renderVarsYml("dev", cfg, inf, "investerra")

	for _, want := range []string{
		"managedEnvironments/me-dev",
		"userAssignedIdentities/mi-dev",
		"acr_server: example.azurecr.io",
		`docker_image: "example.azurecr.io/investerra/example-service:{{ .Vars.git_commit }}"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// No TODO markers on the fully-inferred fields.
	for _, forbidden := range []string{
		"TODO: full resource ID",
		"TODO: set your ACR",
		"TODO: image path",
	} {
		if strings.Contains(got, forbidden) {
			t.Errorf("unexpected TODO %q in fully-inferred output:\n%s", forbidden, got)
		}
	}
}

func TestRenderVarsYml_PartialInferences(t *testing.T) {
	// Only ACR known — image should still compose (without org since
	// gitOrg is empty), but managed env + identity keep their TODOs.
	cfg := initConfig{Name: "example-service", ResourceGroup: "dbx"}
	inf := envInference{ACRServer: "someacr.azurecr.io"}
	got := renderVarsYml("dev", cfg, inf, "")

	if !strings.Contains(got, `docker_image: "someacr.azurecr.io/example-service:{{ .Vars.git_commit }}"`) {
		t.Errorf("expected acr+app image composition (no org) in:\n%s", got)
	}
	if !strings.Contains(got, "TODO: full resource ID") {
		t.Errorf("managed env TODO should remain when inference missing")
	}
}

// ---------- composeDockerImage ----------

func TestComposeDockerImage_ACRPlusOrg(t *testing.T) {
	got := composeDockerImage("acr.azurecr.io", "investerra", "example-service")
	want := "acr.azurecr.io/investerra/example-service:{{ .Vars.git_commit }}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComposeDockerImage_ACROnly(t *testing.T) {
	got := composeDockerImage("acr.azurecr.io", "", "app")
	want := "acr.azurecr.io/app:{{ .Vars.git_commit }}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComposeDockerImage_NoACR(t *testing.T) {
	if got := composeDockerImage("", "org", "app"); got != "" {
		t.Errorf("got %q, want empty (TODO placeholder kept by caller)", got)
	}
}

func TestComposeDockerImage_NoApp(t *testing.T) {
	if got := composeDockerImage("acr", "org", ""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// ---------- parseGitRemote ----------

func TestParseGitRemote_HTTPS(t *testing.T) {
	host, org, repo := parseGitRemote("https://github.com/investerra/example-service.git")
	if host != "github.com" || org != "investerra" || repo != "example-service" {
		t.Errorf("host=%q org=%q repo=%q", host, org, repo)
	}
}

func TestParseGitRemote_SSH(t *testing.T) {
	host, org, repo := parseGitRemote("git@github.com:investerra/example-service.git")
	if host != "github.com" || org != "investerra" || repo != "example-service" {
		t.Errorf("host=%q org=%q repo=%q", host, org, repo)
	}
}

func TestParseGitRemote_WithoutGitSuffix(t *testing.T) {
	_, org, repo := parseGitRemote("https://github.com/investerra/example-service")
	if org != "investerra" || repo != "example-service" {
		t.Errorf("org=%q repo=%q", org, repo)
	}
}

func TestParseGitRemote_TrailingSlash(t *testing.T) {
	_, org, repo := parseGitRemote("https://github.com/investerra/example-service/")
	if org != "investerra" || repo != "example-service" {
		t.Errorf("org=%q repo=%q", org, repo)
	}
}

func TestParseGitRemote_GitLab(t *testing.T) {
	host, org, repo := parseGitRemote("git@gitlab.com:acme/service.git")
	if host != "gitlab.com" || org != "acme" || repo != "service" {
		t.Errorf("host=%q org=%q repo=%q", host, org, repo)
	}
}

func TestParseGitRemote_Bogus(t *testing.T) {
	for _, in := range []string{"", "not a url", "ssh://badssh", "just-a-name"} {
		host, org, repo := parseGitRemote(in)
		if host != "" || org != "" || repo != "" {
			t.Errorf("input %q: expected all-empty, got host=%q org=%q repo=%q", in, host, org, repo)
		}
	}
}

func TestRenderSecretsPlain_HintsEditCommand(t *testing.T) {
	got := renderSecretsPlain("dev")
	for _, want := range []string{
		"lazure secrets edit dev",
		"kebab-case",
		"secret: name-of-secret",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// ---------- gitignorePatternsToAppend ----------

func TestGitignorePatternsToAppend_EmptyFile(t *testing.T) {
	got := gitignorePatternsToAppend("", []string{"envs/*.plain.yml", ".lazure/"})
	if len(got) != 2 {
		t.Errorf("empty file should need all patterns appended; got %v", got)
	}
}

func TestGitignorePatternsToAppend_AlreadyPresent(t *testing.T) {
	existing := "node_modules/\nenvs/*.plain.yml\n.lazure/\n"
	got := gitignorePatternsToAppend(existing, []string{"envs/*.plain.yml", ".lazure/"})
	if len(got) != 0 {
		t.Errorf("all present → empty append list; got %v", got)
	}
}

func TestGitignorePatternsToAppend_Partial(t *testing.T) {
	existing := "envs/*.plain.yml\n"
	got := gitignorePatternsToAppend(existing, []string{"envs/*.plain.yml", ".lazure/"})
	if len(got) != 1 || got[0] != ".lazure/" {
		t.Errorf("should append only missing .lazure/; got %v", got)
	}
}

func TestGitignorePatternsToAppend_PreservesOrder(t *testing.T) {
	got := gitignorePatternsToAppend("", []string{"a", "b", "c"})
	if strings.Join(got, ",") != "a,b,c" {
		t.Errorf("order not preserved: %v", got)
	}
}

// ---------- scaffoldProject ----------

func TestScaffoldProject_CreatesExpectedFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := initConfig{
		Name: "myapp", Location: "eastus", ResourceGroup: "rg",
		Envs: []string{"dev", "uat", "prd"},
	}
	if err := scaffoldProject(dir, cfg, projectInferences{byEnv: map[string]envInference{}}); err != nil {
		t.Fatal(err)
	}

	expect := []string{
		"deploy.yml",
		"deploy.schema.json", // schema sits beside deploy.yml for modeline resolution
		"envs/dev.vars.yml",
		"envs/uat.vars.yml",
		"envs/prd.vars.yml",
		"envs/dev.secrets.plain.yml", // first env only
	}
	for _, rel := range expect {
		p := filepath.Join(dir, rel)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing file %s: %v", rel, err)
		}
	}

	// uat/prd must NOT get a secrets.plain.yml (only first env does).
	for _, rel := range []string{"envs/uat.secrets.plain.yml", "envs/prd.secrets.plain.yml"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			t.Errorf("unexpected file created: %s", rel)
		}
	}
}

func TestScaffoldProject_IdempotentUnderForce(t *testing.T) {
	dir := t.TempDir()
	cfg := initConfig{Name: "a", Location: "x", ResourceGroup: "rg", Envs: []string{"dev"}}
	if err := scaffoldProject(dir, cfg, projectInferences{byEnv: map[string]envInference{}}); err != nil {
		t.Fatal(err)
	}
	// Second call overwrites without error (the CLI guards pre-existing
	// manifests separately; the scaffold itself is always force-write).
	if err := scaffoldProject(dir, cfg, projectInferences{byEnv: map[string]envInference{}}); err != nil {
		t.Fatal(err)
	}
}

// ---------- updateGitignore ----------

func TestUpdateGitignore_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := updateGitignore(path, []string{"envs/*.plain.yml", ".lazure/"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)
	for _, want := range []string{"envs/*.plain.yml", ".lazure/", "# lazure"} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q in:\n%s", want, content)
		}
	}
}

func TestUpdateGitignore_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	initial := "node_modules/\n*.log\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := updateGitignore(path, []string{"envs/*.plain.yml", ".lazure/"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	content := string(b)
	if !strings.HasPrefix(content, initial) {
		t.Errorf("existing content not preserved:\n%s", content)
	}
	for _, want := range []string{"envs/*.plain.yml", ".lazure/"} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q in:\n%s", want, content)
		}
	}
}

func TestUpdateGitignore_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	_ = os.WriteFile(path, []byte("envs/*.plain.yml\n.lazure/\n"), 0o644)
	before, _ := os.ReadFile(path)
	if err := updateGitignore(path, []string{"envs/*.plain.yml", ".lazure/"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("file changed when all patterns already present\nbefore:\n%s\nafter:\n%s",
			before, after)
	}
}

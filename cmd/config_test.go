package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

func TestConfig_PrintsContainerAppMappingRulesOnly(t *testing.T) {
	app := &cli.Command{
		Name: "lazure",
		Commands: []*cli.Command{{
			Name:   "config",
			Action: Config,
		}},
	}

	out := captureStdoutRun(t, func() {
		if err := app.Run(context.Background(), []string{"lazure", "config"}); err != nil {
			t.Fatalf("Config run failed: %v", err)
		}
	})

	for _, want := range []string{
		"container_app:",
		"managed:",
		"preserved_external:",
		"ignored:",
		"normalized_defaults:",
		"unsupported_live_state:",
		"/properties/configuration/ingress/customDomains",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	for _, notWant := range []string{"deploy_file:", "envs_dir:", "sops_config:", "schema_file:"} {
		if strings.Contains(out, notWant) {
			t.Fatalf("config should not print doctor-style project diagnostics %q:\n%s", notWant, out)
		}
	}
}

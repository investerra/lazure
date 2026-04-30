package cmd

import (
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

func TestBuildDoc_IncludesPrimeAgentGuide(t *testing.T) {
	root := &cli.Command{
		Name:  "lazure",
		Usage: "Deploy and manage Azure Container Apps",
		Commands: []*cli.Command{
			{Name: "deploy", Usage: "deploy an app"},
			{Name: "prime", Usage: "print agent guide"},
		},
	}

	doc := buildDoc(root)
	guide := strings.Join(doc.AgentGuide, "\n")
	if !strings.Contains(guide, "Run `lazure prime` at the start of a new agent session") {
		t.Fatalf("agent guide does not instruct agents to prime:\n%s", guide)
	}
	for _, want := range []string{"lazure doctor", "lazure validate <env>", "lazure diff <env>", "lazure deploy <env> --wait --logs"} {
		if !strings.Contains(guide, want) {
			t.Errorf("agent guide missing %q:\n%s", want, guide)
		}
	}
	for _, want := range []string{"actions/install", "actions/env_guess", "actions/wait_for_deploy"} {
		if !strings.Contains(guide, want) {
			t.Errorf("agent guide missing action %q:\n%s", want, guide)
		}
	}

	var paths []string
	for _, cat := range doc.Categories {
		for _, cmd := range cat.Commands {
			paths = append(paths, cmd.Path)
		}
	}
	joinedPaths := strings.Join(paths, "\n")
	if !strings.Contains(joinedPaths, "lazure prime") {
		t.Fatalf("prime command not documented; paths:\n%s", joinedPaths)
	}
	if strings.Contains(joinedPaths, "lazure llm") {
		t.Fatalf("llm command should be replaced, paths:\n%s", joinedPaths)
	}
}

func TestWriteFlagMD_FormatsOnlyFlagNamesAsCode(t *testing.T) {
	var b strings.Builder
	writeFlagMD(&b, FlagDoc{
		Names:   []string{"verbose", "v"},
		Usage:   "enable debug logs",
		Default: "false",
		EnvVars: []string{"LAZURE_VERBOSE"},
	})

	got := b.String()
	want := "- `--verbose`, `-v` — enable debug logs (default: false) [env: LAZURE_VERBOSE]\n"
	if got != want {
		t.Fatalf("unexpected flag markdown:\nwant: %q\n got: %q", want, got)
	}
}

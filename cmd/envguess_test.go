package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

func TestGuessEnvironmentFromRef(t *testing.T) {
	tests := []struct {
		name string
		ref  envGuessRef
		want string
	}{
		{name: "input override", ref: envGuessRef{InputEnv: "prd", RefType: "branch", RefName: "dev"}, want: "prd"},
		{name: "main branch", ref: envGuessRef{RefType: "branch", RefName: "main"}, want: "uat"},
		{name: "master branch", ref: envGuessRef{RefType: "branch", RefName: "master"}, want: "uat"},
		{name: "dev branch", ref: envGuessRef{RefType: "branch", RefName: "dev"}, want: "dev"},
		{name: "pre branch", ref: envGuessRef{RefType: "branch", RefName: "pre"}, want: "pre"},
		{name: "uat branch", ref: envGuessRef{RefType: "branch", RefName: "uat"}, want: "uat"},
		{name: "any tag", ref: envGuessRef{RefType: "tag", RefName: "v20260430.1"}, want: "prd"},
		{name: "unknown ref type but tag-shaped fallback", ref: envGuessRef{RefName: "refs/tags/v1.2.3"}, want: "prd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := guessEnvironment(tt.ref)
			if err != nil {
				t.Fatalf("guessEnvironment returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("env = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGuessEnvironmentFromRef_Unsupported(t *testing.T) {
	_, err := guessEnvironment(envGuessRef{RefType: "branch", RefName: "feature/x"})
	if err == nil {
		t.Fatal("expected unsupported branch error")
	}
	if !strings.Contains(err.Error(), "unsupported branch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnvGuessCommand_PrintsEnvironment(t *testing.T) {
	out, err := runEnvGuessCommand(t, []string{"--ref-type", "branch", "--ref-name", "master"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "uat" {
		t.Fatalf("output = %q, want uat", out)
	}
}

func TestEnvGuessCommand_WritesGitHubOutput(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "github-output")
	out, err := runEnvGuessCommand(t,
		[]string{"--ref-type", "tag", "--ref-name", "release-anything", "--github-output"},
		map[string]string{"GITHUB_OUTPUT": outputPath},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "prd" {
		t.Fatalf("stdout = %q, want prd", out)
	}
	b, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "environment=prd\n" {
		t.Fatalf("github output = %q", b)
	}
}

func runEnvGuessCommand(t *testing.T, args []string, env map[string]string) (string, error) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}

	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })

	argv := append([]string{"lazure", "env-guess"}, args...)
	app := &cli.Command{
		Name:     "lazure",
		Commands: []*cli.Command{EnvGuessCommand()},
	}
	runErr := app.Run(context.Background(), argv)
	_ = w.Close()

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), runErr
}

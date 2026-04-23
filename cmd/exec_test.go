package cmd

import (
	"slices"
	"testing"

	"github.com/urfave/cli/v3"
)

func TestBuildAzExecArgs_NoCommand(t *testing.T) {
	got := buildAzExecArgs("myapp", "my-rg", "myapp--abc123", "app", nil)
	want := []string{
		"containerapp", "exec",
		"--name", "myapp",
		"--resource-group", "my-rg",
		"--revision", "myapp--abc123",
		"--container", "app",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	if slices.Contains(got, "--command") {
		t.Error("--command must not be present when cmdArgs is empty (az defaults to sh)")
	}
}

func TestBuildAzExecArgs_SingleTokenCommand(t *testing.T) {
	got := buildAzExecArgs("a", "rg", "a--r1", "app", []string{"bash"})
	// Last two elements should be "--command", "bash".
	if got[len(got)-2] != "--command" || got[len(got)-1] != "bash" {
		t.Errorf("tail = %v, want --command bash", got[len(got)-2:])
	}
}

func TestBuildAzExecArgs_MultiTokenCommandJoined(t *testing.T) {
	got := buildAzExecArgs("a", "rg", "r", "c", []string{"ls", "-la", "/srv"})
	// The whole command should be one joined arg value, not three.
	wantCmdIdx := -1
	for i, v := range got {
		if v == "--command" {
			wantCmdIdx = i
			break
		}
	}
	if wantCmdIdx == -1 {
		t.Fatalf("--command missing in %v", got)
	}
	if len(got) != wantCmdIdx+2 {
		t.Errorf("command must be a single joined arg; got %v", got[wantCmdIdx:])
	}
	if got[wantCmdIdx+1] != "ls -la /srv" {
		t.Errorf("command value = %q, want %q", got[wantCmdIdx+1], "ls -la /srv")
	}
}

// TestExecArgs_CmdIsVariadic guards the fix for a regression where
// cli.StringArgs defaulted to Max=0, rejecting any tail arg with
// "args cmd has max 0, not parsing argument". ExecArgs must declare
// Max=-1 so the cmd slice accepts any number of tokens including zero.
func TestExecArgs_CmdIsVariadic(t *testing.T) {
	args := ExecArgs()
	if len(args) < 2 {
		t.Fatalf("expected at least 2 args, got %d", len(args))
	}
	cmdArg, ok := args[1].(*cli.StringArgs)
	if !ok {
		t.Fatalf("expected args[1] to be *cli.StringArgs, got %T", args[1])
	}
	if cmdArg.Max != -1 {
		t.Errorf("cmd StringArgs.Max = %d, want -1 (unlimited)", cmdArg.Max)
	}
	if cmdArg.Min != 0 {
		t.Errorf("cmd StringArgs.Min = %d, want 0 (optional)", cmdArg.Min)
	}
}

func TestBuildAzExecArgs_FlagOrder(t *testing.T) {
	// Lock in flag order so we don't accidentally emit `--revision`
	// before `--resource-group` etc., which would still work but hurts
	// diff-readability when inspecting debug logs.
	got := buildAzExecArgs("app-1", "rg-1", "r1", "web", []string{"sh"})
	expect := []string{
		"containerapp", "exec",
		"--name", "app-1",
		"--resource-group", "rg-1",
		"--revision", "r1",
		"--container", "web",
		"--command", "sh",
	}
	if !slices.Equal(got, expect) {
		t.Errorf("got %v\nwant %v", got, expect)
	}
}

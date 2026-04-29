package cmd

import (
	"context"
	"os"
	"os/exec"
)

// runStreamed runs an external command with stdio wired to lazure's
// stdio so the user sees child-process progress in real time.
func runStreamed(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func runCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

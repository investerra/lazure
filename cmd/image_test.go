package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/investerra/lazure/internal/azurearm"
)

func TestParseImageRef_ACRTaggedImage(t *testing.T) {
	ref, ok := parseImageRef("acr.azurecr.io/org/app:abc123")
	if !ok {
		t.Fatal("parseImageRef returned !ok")
	}
	if ref.Server != "acr.azurecr.io" || ref.Repository != "org/app" || ref.Tag != "abc123" || ref.ImageArg() != "org/app:abc123" {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestParseImageRef_DigestImage(t *testing.T) {
	ref, ok := parseImageRef("acr.azurecr.io/org/app@sha256:abc")
	if !ok {
		t.Fatal("parseImageRef returned !ok")
	}
	if ref.ImageArg() != "org/app@sha256:abc" {
		t.Fatalf("image arg = %q", ref.ImageArg())
	}
}

func TestParseImageRef_DefaultsTagToLatest(t *testing.T) {
	ref, ok := parseImageRef("acr.azurecr.io/app")
	if !ok {
		t.Fatal("parseImageRef returned !ok")
	}
	if ref.ImageArg() != "app:latest" {
		t.Fatalf("image arg = %q", ref.ImageArg())
	}
}

func TestCheckDeployImages_UsesACRShowForEachUniqueACRImage(t *testing.T) {
	var commands []string
	app := &azurearm.ContainerApp{}
	app.Properties.Template.Containers = []azurearm.Container{
		{Name: "app", Image: "acr.azurecr.io/org/app:abc"},
		{Name: "worker", Image: "acr.azurecr.io/org/app:abc"},
		{Name: "sidecar", Image: "ghcr.io/org/sidecar:1"},
	}
	app.Properties.Template.InitContainers = []azurearm.Container{
		{Name: "init", Image: "acr.azurecr.io/org/init:def"},
	}

	err := checkDeployImages(context.Background(), app, func(_ context.Context, name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 {
		t.Fatalf("commands = %#v, want 2 unique ACR checks", commands)
	}
	if commands[0] != "az acr repository show --name acr --image org/app:abc --only-show-errors --output none" {
		t.Errorf("command[0] = %q", commands[0])
	}
	if commands[1] != "az acr repository show --name acr --image org/init:def --only-show-errors --output none" {
		t.Errorf("command[1] = %q", commands[1])
	}
}

func TestApplyRuntimeEnvOverrides_UpdatesRuntimeContainersOnly(t *testing.T) {
	app := &azurearm.ContainerApp{}
	app.Properties.Template.Containers = []azurearm.Container{
		{Name: "app", Env: []azurearm.EnvVar{{Name: "EXISTING", Value: "old"}}},
	}
	app.Properties.Template.InitContainers = []azurearm.Container{
		{Name: "init", Env: []azurearm.EnvVar{{Name: "EXISTING", Value: "old"}}},
	}

	applyRuntimeEnvOverrides(app, map[string]string{"EXISTING": "new", "ONCE": "1"})

	if got := envValue(app.Properties.Template.Containers[0].Env, "EXISTING"); got != "new" {
		t.Fatalf("runtime EXISTING = %q", got)
	}
	if got := envValue(app.Properties.Template.Containers[0].Env, "ONCE"); got != "1" {
		t.Fatalf("runtime ONCE = %q", got)
	}
	if got := envValue(app.Properties.Template.InitContainers[0].Env, "EXISTING"); got != "old" {
		t.Fatalf("init EXISTING = %q", got)
	}
}

package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

func TestBuildDockerArgs_OrderingAndFlags(t *testing.T) {
	got := buildDockerArgs(
		"acr.azurecr.io/foo:abc",
		"./ctx",
		true,         // --pull
		"Dockerfile", // -f
		[]string{"GIT_COMMIT=abc", "BUILD_DATE=now"},
		[]string{"FOO=bar"},
		[]string{"id=tok,env=GH_TOKEN"},
	)
	want := []string{
		"build", "--pull",
		"--file", "Dockerfile",
		"--build-arg", "GIT_COMMIT=abc",
		"--build-arg", "BUILD_DATE=now",
		"--build-arg", "FOO=bar",
		"--secret", "id=tok,env=GH_TOKEN",
		"-t", "acr.azurecr.io/foo:abc",
		"./ctx",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("\n got: %v\nwant: %v", got, want)
	}
}

func TestBuildDockerArgs_MinimalNoFlags(t *testing.T) {
	// autoBuildArgs is the caller's job; here we pass nil, expecting
	// only the structural args: subcommand, tag, context.
	got := buildDockerArgs("img:latest", ".", false, "", nil, nil, nil)
	want := []string{"build", "-t", "img:latest", "."}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("\n got: %v\nwant: %v", got, want)
	}
}

func TestAutoBuildArgs_AllVarsPresent(t *testing.T) {
	got := autoBuildArgs(map[string]any{
		"git_commit": "abc123",
		"git_branch": "main",
	})
	// Order: GIT_COMMIT + APP_VERSION (both from git_commit), GIT_BRANCH, BUILD_DATE.
	want := []string{"GIT_COMMIT=abc123", "APP_VERSION=abc123", "GIT_BRANCH=main"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i], w)
		}
	}
	// BUILD_DATE always last and non-empty.
	if !strings.HasPrefix(got[len(got)-1], "BUILD_DATE=") {
		t.Errorf("last arg = %q, want BUILD_DATE=...", got[len(got)-1])
	}
	if got[len(got)-1] == "BUILD_DATE=" {
		t.Errorf("BUILD_DATE is empty")
	}
}

func TestAutoBuildArgs_MissingGitVars(t *testing.T) {
	got := autoBuildArgs(map[string]any{})
	// Only BUILD_DATE present; git_* skipped because empty.
	if len(got) != 1 {
		t.Fatalf("got %d args, want 1: %v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "BUILD_DATE=") {
		t.Errorf("got %q, want BUILD_DATE=...", got[0])
	}
}

func TestACRNameFromServer(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantOK   bool
	}{
		{"exampleacr.azurecr.io", "exampleacr", true},
		{"foo.azurecr.io", "foo", true},
		{"  foo.azurecr.io  ", "foo", true},
		{"", "", false},
		{"foo", "", false},             // no dot
		{"foo.example.com", "", false}, // not azurecr
		{".azurecr.io", "", false},     // empty name
	}
	for _, tc := range cases {
		gotName, gotOK := acrNameFromServer(tc.in)
		if gotName != tc.wantName || gotOK != tc.wantOK {
			t.Errorf("acrNameFromServer(%q) = (%q, %v), want (%q, %v)",
				tc.in, gotName, gotOK, tc.wantName, tc.wantOK)
		}
	}
}

func TestRunImageBuild_PullBuildPushSequence(t *testing.T) {
	var commands []string
	runner := func(_ context.Context, name string, args ...string) error {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil
	}
	lookup := func(string) error { return nil }

	err := runImageBuild(context.Background(), imageBuildOptions{
		Env:        "dev",
		ProjectDir: "deploy",
		Vars: map[string]any{
			"docker_image": "acr.azurecr.io/app:abc",
			"acr_server":   "acr.azurecr.io",
			"git_commit":   "abc",
		},
		Push:   true,
		Pull:   true,
		Runner: runner,
		Lookup: lookup,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(commands) != 3 {
		t.Fatalf("commands = %#v, want docker build, az acr login, docker push", commands)
	}
	if !strings.HasPrefix(commands[0], "docker build --pull ") {
		t.Errorf("build command = %q, want docker build --pull ...", commands[0])
	}
	if !strings.Contains(commands[0], "-t acr.azurecr.io/app:abc") {
		t.Errorf("build command missing image tag: %q", commands[0])
	}
	if commands[1] != "az acr login --name acr" {
		t.Errorf("login command = %q", commands[1])
	}
	if commands[2] != "docker push acr.azurecr.io/app:abc" {
		t.Errorf("push command = %q", commands[2])
	}
}

// urfave/cli v3 splits StringSlice values on commas by default, which
// mangles docker --secret values like "id=tok,env=GH_TOKEN" into two
// flags. The build/deploy subcommands set DisableSliceFlagSeparator to
// disable that split — this test guards the wiring.
func TestSecretFlag_PreservesCommas(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"build", []string{"lazure", "build", "--secret", "id=tok,env=GH_TOKEN", "dev"}},
		{"deploy", []string{"lazure", "deploy", "--secret", "id=tok,env=GH_TOKEN", "dev"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			root := &cli.Command{
				Name: "lazure",
				Commands: []*cli.Command{
					{
						Name:                      tc.name,
						Arguments:                 []cli.Argument{&cli.StringArg{Name: "env"}},
						Flags:                     []cli.Flag{&cli.StringSliceFlag{Name: "secret"}},
						DisableSliceFlagSeparator: true,
						Action: func(_ context.Context, c *cli.Command) error {
							got = c.StringSlice("secret")
							return nil
						},
					},
				},
			}
			if err := root.Run(context.Background(), tc.argv); err != nil {
				t.Fatal(err)
			}
			want := []string{"id=tok,env=GH_TOKEN"}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("got %#v, want %#v", got, want)
			}
		})
	}
}

func TestRunImageBuild_RequiresACRServerForPush(t *testing.T) {
	err := runImageBuild(context.Background(), imageBuildOptions{
		Env:        "dev",
		ProjectDir: "deploy",
		Vars: map[string]any{
			"docker_image": "acr.azurecr.io/app:abc",
		},
		Push:   true,
		Runner: func(context.Context, string, ...string) error { return nil },
		Lookup: func(string) error { return nil },
	})
	if err == nil {
		t.Fatal("expected missing acr_server error")
	}
	if !strings.Contains(err.Error(), "acr_server var is required") {
		t.Fatalf("error = %v", err)
	}
}

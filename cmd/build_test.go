package cmd

import (
	"strings"
	"testing"
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
		{"foo", "", false},               // no dot
		{"foo.example.com", "", false},   // not azurecr
		{".azurecr.io", "", false},       // empty name
	}
	for _, tc := range cases {
		gotName, gotOK := acrNameFromServer(tc.in)
		if gotName != tc.wantName || gotOK != tc.wantOK {
			t.Errorf("acrNameFromServer(%q) = (%q, %v), want (%q, %v)",
				tc.in, gotName, gotOK, tc.wantName, tc.wantOK)
		}
	}
}

package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/imroc/req/v3"
	"github.com/urfave/cli/v3"

	"github.com/investerra/lazure/internal/errs"
)

// SelfUpdateFlags are the flags for `lazure self-update`.
func SelfUpdateFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "check", Usage: "report-only: exit 0 if up to date, 1 if an update is available"},
	}
}

// releaseAPIURL is the GitHub Releases API endpoint for lazure. Kept
// as a package-level var so tests can point it elsewhere if needed;
// defaults to the real repo.
var releaseAPIURL = "https://api.github.com/repos/investerra/lazure/releases/latest"

type releaseAsset struct {
	Name string `json:"name"`
	// URL is the API endpoint for the asset (api.github.com/.../assets/{id}).
	// Works for public AND private repos when combined with
	// Accept: application/octet-stream — GitHub signs a temporary
	// redirect URL for the actual bytes. browser_download_url only
	// works for public repos, so we always prefer URL.
	URL                string `json:"url"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type releaseResponse struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

// SelfUpdate implements `lazure self-update [--check]`. Fetches the
// latest GitHub Release, verifies a sha256 checksum against its
// checksums.txt, and atomically replaces the running binary via
// os.Rename in the same directory.
//
// Windows is rejected up front (atomic-rename over a running exe has
// different semantics on NTFS; defer until we have a tested story).
// Dev builds (Version == "dev") are also rejected — we can't safely
// compare an unset version to a real tag.
//
// Flow is linear with early returns on each failure mode rather than
// a single big try/catch — each network / file / crypto step has its
// own distinct failure message so users can tell "rate limited" from
// "checksum mismatch" from "permission denied on /usr/local/bin".
func SelfUpdate(ctx context.Context, c *cli.Command) error {
	check := c.Bool("check")
	slog.Debug("self-update: start", "check", check, "current_version", mainVersion(), "goos", runtime.GOOS, "goarch", runtime.GOARCH)

	if runtime.GOOS == "windows" {
		return errs.Usage(errs.New("self-update: not supported on Windows; use `go install github.com/investerra/lazure@latest` or download manually"))
	}
	current := mainVersion()
	if current == "dev" || current == "" {
		return errs.Usage(errs.New("self-update: running a dev build (version unset); build with -ldflags '-X main.Version=...' or download a released binary"))
	}

	token := resolveGitHubToken()
	slog.Debug("self-update: auth", "has_token", token != "")

	rel, err := fetchLatestRelease(ctx, token)
	if err != nil {
		return errs.System(errs.Wrap(err, "self-update: fetch release"))
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	slog.Debug("self-update: fetched", "tag", rel.TagName, "assets", len(rel.Assets))

	if versionsEqual(current, latest) {
		fmt.Printf("lazure is up to date (%s)\n", current)
		return nil
	}
	if check {
		fmt.Printf("update available: %s → %s\n", current, latest)
		// Silent so main.go doesn't print a redundant slog.Error over
		// the clean printf above — exit code still signals "update
		// available" (1 = task state, not error condition).
		return errs.Silent(errs.CodeTask, errs.New("update available"))
	}

	tarballURL, checksumsURL, err := matchAsset(rel.Assets, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return errs.System(errs.Wrapf(err, "self-update: no asset for %s/%s in release %s", runtime.GOOS, runtime.GOARCH, rel.TagName))
	}

	slog.Info("self-update: downloading", "tarball", tarballURL)
	tarData, err := downloadBytes(ctx, tarballURL, token)
	if err != nil {
		return errs.System(errs.Wrap(err, "self-update: download tarball"))
	}
	checksumsData, err := downloadBytes(ctx, checksumsURL, token)
	if err != nil {
		return errs.System(errs.Wrap(err, "self-update: download checksums"))
	}

	wantName := assetName(runtime.GOOS, runtime.GOARCH)
	expectedSum, err := extractChecksum(checksumsData, wantName)
	if err != nil {
		return errs.System(errs.Wrap(err, "self-update: parse checksums.txt"))
	}
	gotSum := sha256Hex(tarData)
	if gotSum != expectedSum {
		return errs.System(errs.Errorf("self-update: sha256 mismatch for %s: got %s, want %s", wantName, gotSum, expectedSum))
	}
	slog.Debug("self-update: checksum verified", "sha256", gotSum)

	newBinary, err := extractBinary(tarData, "lazure")
	if err != nil {
		return errs.System(errs.Wrap(err, "self-update: extract binary"))
	}

	if err := atomicReplaceBinary(newBinary); err != nil {
		return errs.System(err)
	}
	fmt.Printf("updated lazure %s → %s\n", current, latest)
	return nil
}

// ---------- version ----------

// mainVersion returns the build-time version embedded via ldflags. Lives
// in its own indirection so tests can stub it without touching main's
// globals. Actual wiring back to main.Version happens in main.go.
var mainVersion = func() string { return "dev" }

// SetVersionGetter lets main.go register its Version variable with the
// selfupdate module. Called once at startup.
func SetVersionGetter(f func() string) {
	if f != nil {
		mainVersion = f
	}
}

// versionsEqual compares two version strings tolerating a leading `v`.
// goreleaser strips the prefix when injecting via ldflags, but GitHub
// API returns it in tag_name; one side always needs normalization.
func versionsEqual(a, b string) bool {
	return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

// ---------- asset matching ----------

// assetName is goreleaser's archive name template rendered for the
// given platform. Kept in sync with .goreleaser.yml's name_template —
// if that ever changes, this must change too.
func assetName(goos, goarch string) string {
	return fmt.Sprintf("lazure_%s_%s.tar.gz", goos, goarch)
}

// matchAsset finds the tarball + checksums URLs in a release's assets
// list for the given GOOS/GOARCH. Returns the API `url` field rather
// than `browser_download_url` so downloads work for private repos too
// (public repos accept both). Errors if either asset is missing — a
// release without one is unusable for self-update.
func matchAsset(assets []releaseAsset, goos, goarch string) (tarball, checksums string, err error) {
	want := assetName(goos, goarch)
	for _, a := range assets {
		switch a.Name {
		case want:
			tarball = a.URL
		case "checksums.txt":
			checksums = a.URL
		}
	}
	if tarball == "" {
		return "", "", errs.Errorf("asset %q not found", want)
	}
	if checksums == "" {
		return "", "", errs.New("checksums.txt not found")
	}
	return tarball, checksums, nil
}

// extractChecksum parses a line of the form "<sha256hex>  <filename>"
// out of goreleaser's checksums.txt and returns the hash for the
// requested filename. Line format matches what sha256sum produces —
// two spaces between hash and name, anchored at line boundaries.
func extractChecksum(data []byte, filename string) (string, error) {
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		if parts[1] == filename {
			return parts[0], nil
		}
	}
	return "", errs.Errorf("no entry for %q in checksums.txt", filename)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ---------- tarball extraction ----------

// extractBinary reads a gzipped tar archive from data and returns the
// bytes of the file named `binaryName`. Skips directory entries,
// LICENSE, README, and anything else — only the binary itself is
// returned. Errors if the archive is unreadable or the binary isn't
// found.
func extractBinary(data []byte, binaryName string) ([]byte, error) {
	gzr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, errs.Wrap(err, "gzip")
	}
	defer func() { _ = gzr.Close() }()
	tr := tar.NewReader(gzr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errs.Wrap(err, "tar header")
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(h.Name) != binaryName {
			continue
		}
		buf, err := io.ReadAll(tr)
		if err != nil {
			return nil, errs.Wrap(err, "tar read")
		}
		return buf, nil
	}
	return nil, errs.Errorf("binary %q not found in archive", binaryName)
}

// ---------- network ----------

// selfUpdateClient is a shared req client for the two GETs this
// command makes. Pinned to req for consistency with the rest of the
// codebase's HTTP usage.
var selfUpdateClient = req.C().SetUserAgent("lazure-self-update").DisableAutoReadResponse()

// resolveGitHubToken tries (1) env GITHUB_TOKEN, then (2) gh CLI's
// stored token via `gh auth token`. Returns "" if neither is
// available — the caller then makes unauthenticated requests which
// succeed on public repos and 404 on private ones.
func resolveGitHubToken() string {
	if t := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); t != "" {
		return t
	}
	// gh is a soft dependency. Many developer machines already have it
	// authenticated; reusing that token means most users never need to
	// set GITHUB_TOKEN explicitly.
	if _, err := exec.LookPath("gh"); err != nil {
		return ""
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func fetchLatestRelease(ctx context.Context, token string) (*releaseResponse, error) {
	var out releaseResponse
	r := selfUpdateClient.R().SetContext(ctx)
	if token != "" {
		r = r.SetHeader("Authorization", "token "+token)
	}
	resp, err := r.Get(releaseAPIURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 404 && token == "" {
		return nil, errs.Errorf("GET %s: 404 (repo may be private — set GITHUB_TOKEN or run `gh auth login`)", releaseAPIURL)
	}
	if !resp.IsSuccessState() {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, errs.Errorf("GET %s: %s: %s", releaseAPIURL, resp.Status, string(body))
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errs.Wrap(err, "read body")
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, errs.Wrap(err, "parse release json")
	}
	return &out, nil
}

// downloadBytes GETs url and returns the body bytes. For private-repo
// asset downloads, the browser_download_url returned by the GitHub API
// still works as long as we pass Authorization — GitHub signs a
// redirect URL on the fly. Accept: application/octet-stream is only
// strictly required for the api.github.com/…/releases/assets/{id}
// form; including it here is harmless and future-proof.
func downloadBytes(ctx context.Context, url, token string) ([]byte, error) {
	r := selfUpdateClient.R().
		SetContext(ctx).
		SetHeader("Accept", "application/octet-stream")
	if token != "" {
		r = r.SetHeader("Authorization", "token "+token)
	}
	resp, err := r.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if !resp.IsSuccessState() {
		return nil, errs.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// ---------- atomic replace ----------

// atomicReplaceBinary writes the new binary bytes to a tempfile in the
// same directory as the running executable, chmods it +x, and renames
// over the live path. The kernel keeps the old inode alive for the
// duration of the current process — the next invocation runs the new
// binary. EvalSymlinks follows symlinks so we replace the actual file,
// not the symlink target (e.g. Homebrew installs are symlinked).
func atomicReplaceBinary(newBinary []byte) error {
	exec, err := os.Executable()
	if err != nil {
		return errs.Wrap(err, "locate running binary")
	}
	exec, err = filepath.EvalSymlinks(exec)
	if err != nil {
		return errs.Wrap(err, "resolve symlinks")
	}
	dir := filepath.Dir(exec)

	tmp, err := os.CreateTemp(dir, "lazure-update-*")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return errs.Errorf("cannot write to %s — try `sudo lazure self-update` or reinstall via your package manager", dir)
		}
		return errs.Wrap(err, "create tempfile")
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if a later step fails. Successful rename
	// removes tmpPath from the fs, so the subsequent Remove is a noop
	// and its error is ignored.
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(newBinary); err != nil {
		_ = tmp.Close()
		return errs.Wrap(err, "write tempfile")
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return errs.Wrap(err, "chmod tempfile")
	}
	if err := tmp.Close(); err != nil {
		return errs.Wrap(err, "close tempfile")
	}
	if err := os.Rename(tmpPath, exec); err != nil {
		return errs.Wrapf(err, "rename %s → %s", tmpPath, exec)
	}
	slog.Debug("self-update: replaced binary", "path", exec)
	return nil
}

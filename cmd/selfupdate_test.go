package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

// ---------- versionsEqual ----------

func TestVersionsEqual(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.0.2", "0.0.2", true},
		{"v0.0.2", "0.0.2", true},       // main.Version may or may not have `v`
		{"v0.0.2", "v0.0.2", true},
		{"0.0.2", "v0.0.2", true},
		{"0.0.1", "0.0.2", false},
		{"v1.0.0", "v1.0.1", false},
		{"", "", true},
	}
	for _, tc := range cases {
		if got := versionsEqual(tc.a, tc.b); got != tc.want {
			t.Errorf("versionsEqual(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// ---------- assetName ----------

func TestAssetName(t *testing.T) {
	cases := []struct {
		goos, goarch, want string
	}{
		{"linux", "amd64", "lazure_linux_amd64.tar.gz"},
		{"linux", "arm64", "lazure_linux_arm64.tar.gz"},
		{"darwin", "amd64", "lazure_darwin_amd64.tar.gz"},
		{"darwin", "arm64", "lazure_darwin_arm64.tar.gz"},
	}
	for _, tc := range cases {
		if got := assetName(tc.goos, tc.goarch); got != tc.want {
			t.Errorf("assetName(%q, %q) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

// ---------- matchAsset ----------

func TestMatchAsset_Happy(t *testing.T) {
	assets := []releaseAsset{
		{Name: "lazure_linux_amd64.tar.gz", URL: "https://api.github.com/assets/1", BrowserDownloadURL: "https://example/lazure_linux_amd64.tar.gz"},
		{Name: "lazure_linux_arm64.tar.gz", URL: "https://api.github.com/assets/2", BrowserDownloadURL: "https://example/lazure_linux_arm64.tar.gz"},
		{Name: "lazure_darwin_amd64.tar.gz", URL: "https://api.github.com/assets/3", BrowserDownloadURL: "https://example/lazure_darwin_amd64.tar.gz"},
		{Name: "lazure_darwin_arm64.tar.gz", URL: "https://api.github.com/assets/4", BrowserDownloadURL: "https://example/lazure_darwin_arm64.tar.gz"},
		{Name: "checksums.txt", URL: "https://api.github.com/assets/5", BrowserDownloadURL: "https://example/checksums.txt"},
	}
	tarball, checksums, err := matchAsset(assets, "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	// Private-repo support: we want the API url, not the browser URL.
	if tarball != "https://api.github.com/assets/1" {
		t.Errorf("tarball URL = %q, want API url (not browser_download_url)", tarball)
	}
	if checksums != "https://api.github.com/assets/5" {
		t.Errorf("checksums URL = %q, want API url", checksums)
	}
}

func TestMatchAsset_TarballMissing(t *testing.T) {
	assets := []releaseAsset{
		{Name: "lazure_darwin_amd64.tar.gz", URL: "https://api/a"},
		{Name: "checksums.txt", URL: "https://api/c"},
	}
	_, _, err := matchAsset(assets, "linux", "amd64")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

func TestMatchAsset_ChecksumsMissing(t *testing.T) {
	assets := []releaseAsset{
		{Name: "lazure_linux_amd64.tar.gz", URL: "https://api/a"},
	}
	_, _, err := matchAsset(assets, "linux", "amd64")
	if err == nil || !strings.Contains(err.Error(), "checksums.txt") {
		t.Errorf("expected checksums-not-found, got %v", err)
	}
}

func TestMatchAsset_EmptyAssets(t *testing.T) {
	_, _, err := matchAsset(nil, "linux", "amd64")
	if err == nil {
		t.Error("empty assets should error")
	}
}

// ---------- extractChecksum ----------

func TestExtractChecksum_Happy(t *testing.T) {
	data := []byte(`
3a4c2f0b  lazure_darwin_amd64.tar.gz
7f8a1d9e  lazure_linux_amd64.tar.gz
b2c3d4e5  lazure_linux_arm64.tar.gz
`)
	got, err := extractChecksum(data, "lazure_linux_amd64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "7f8a1d9e" {
		t.Errorf("got %q, want 7f8a1d9e", got)
	}
}

func TestExtractChecksum_NotFound(t *testing.T) {
	data := []byte("abc  lazure_linux_amd64.tar.gz\n")
	_, err := extractChecksum(data, "lazure_darwin_amd64.tar.gz")
	if err == nil || !strings.Contains(err.Error(), "no entry") {
		t.Errorf("expected not-found, got %v", err)
	}
}

func TestExtractChecksum_Empty(t *testing.T) {
	_, err := extractChecksum(nil, "anything.tar.gz")
	if err == nil {
		t.Error("empty input should error")
	}
	_, err = extractChecksum([]byte("   \n\n  "), "anything.tar.gz")
	if err == nil {
		t.Error("whitespace-only input should error")
	}
}

func TestExtractChecksum_MalformedLinesIgnored(t *testing.T) {
	// Tolerate malformed lines — skip them rather than fail; goreleaser's
	// format is stable, but defensive parsing avoids brittleness.
	data := []byte(`not-valid-line
a line with way too many fields here definitely
7f8a1d9e  lazure_linux_amd64.tar.gz
`)
	got, err := extractChecksum(data, "lazure_linux_amd64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "7f8a1d9e" {
		t.Errorf("got %q, want 7f8a1d9e", got)
	}
}

// ---------- extractBinary ----------

// buildTarGz creates a gzipped tar archive with the given entries (name → content).
// Used by extractBinary tests to produce realistic inputs without a real goreleaser build.
func buildTarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for name, content := range entries {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractBinary_Happy(t *testing.T) {
	// Mimic the real goreleaser archive layout: binary + LICENSE + README.
	data := buildTarGz(t, map[string]string{
		"LICENSE":   "mit text",
		"README.md": "# lazure",
		"lazure":    "\x7fELF-fake-binary",
	})
	got, err := extractBinary(data, "lazure")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\x7fELF-fake-binary" {
		t.Errorf("got %q, want fake binary content", got)
	}
}

func TestExtractBinary_NotInArchive(t *testing.T) {
	data := buildTarGz(t, map[string]string{
		"LICENSE":   "mit",
		"README.md": "#",
	})
	_, err := extractBinary(data, "lazure")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

func TestExtractBinary_BadGzip(t *testing.T) {
	_, err := extractBinary([]byte("not gzipped at all"), "lazure")
	if err == nil {
		t.Error("expected gzip error")
	}
}

func TestExtractBinary_SubdirPathNormalized(t *testing.T) {
	// If an archive ever nests the binary under a subdirectory
	// (e.g. lazure_linux_amd64/lazure), filepath.Base still picks it up.
	data := buildTarGz(t, map[string]string{
		"lazure_linux_amd64/lazure": "bin content",
	})
	got, err := extractBinary(data, "lazure")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "bin content" {
		t.Errorf("got %q", got)
	}
}

// ---------- sha256Hex ----------

func TestSHA256Hex_Deterministic(t *testing.T) {
	data := []byte("hello world")
	// Known sha256 of "hello world": b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9
	got := sha256Hex(data)
	want := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

package azureapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// stubCred returns a static token — mock servers don't validate the
// JWT, just assert the Authorization header is present.
type stubCred struct{ token string }

func (s *stubCred) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: s.token, ExpiresOn: time.Now().Add(time.Hour)}, nil
}

// newMockClient starts a test server with the given handler and returns
// a KeyVaultClient pointed at it with a static bearer token.
func newMockClient(t *testing.T, handler http.Handler) (*KeyVaultClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	tokens := newTokenProviderWith(&stubCred{token: "tok-123"})
	return NewKeyVaultClient(srv.URL, tokens), srv
}

// hasAzureCreds reports whether `az account show` succeeds — the same
// check used by other integration tests in this repo to skip cleanly
// when credentials aren't available.
func hasAzureCreds(t *testing.T) bool {
	t.Helper()
	return exec.Command("az", "account", "show").Run() == nil
}

// requireAuthAndVersion asserts the request has a bearer token and the
// correct api-version query. Returns the parsed body for the caller.
func requireAuthAndVersion(t *testing.T, r *http.Request) {
	t.Helper()
	if auth := r.Header.Get("Authorization"); auth != "Bearer tok-123" {
		t.Errorf("Authorization header = %q", auth)
	}
	if v := r.URL.Query().Get("api-version"); v != kvAPIVersion {
		t.Errorf("api-version query = %q, want %q", v, kvAPIVersion)
	}
}

// ---------- GetSecret ----------

func TestKeyVault_GetSecret_Success(t *testing.T) {
	c, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireAuthAndVersion(t, r)
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/secrets/my-secret") {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"value":"s3cret","id":"https://kv/secrets/my-secret/ver"}`))
	}))

	got, err := c.GetSecret(context.Background(), "my-secret")
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cret" {
		t.Errorf("value = %q", got)
	}
}

func TestKeyVault_GetSecret_NotFound(t *testing.T) {
	c, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"SecretNotFound","message":"no such secret"}}`, http.StatusNotFound)
	}))

	_, err := c.GetSecret(context.Background(), "missing")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("error = %v, want ErrSecretNotFound", err)
	}
}

func TestKeyVault_GetSecret_ServerError(t *testing.T) {
	c, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))

	_, err := c.GetSecret(context.Background(), "any")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrSecretNotFound) {
		t.Errorf("5xx should not be classified as NotFound")
	}
}

// ---------- PutSecret ----------

func TestKeyVault_PutSecret_Success(t *testing.T) {
	var gotBody []byte
	c, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireAuthAndVersion(t, r)
		if r.Method != http.MethodPut {
			t.Errorf("method = %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"value":"v","id":"https://kv/secrets/foo/ver"}`))
	}))

	if err := c.PutSecret(context.Background(), "foo", "v"); err != nil {
		t.Fatal(err)
	}

	var parsed map[string]string
	if err := json.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["value"] != "v" {
		t.Errorf("request body value = %q", parsed["value"])
	}
}

func TestKeyVault_PutSecret_Forbidden(t *testing.T) {
	c, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))

	err := c.PutSecret(context.Background(), "foo", "v")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "403") && !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("error should include status: %v", err)
	}
}

// ---------- DeleteSecret ----------

func TestKeyVault_DeleteSecret_Success(t *testing.T) {
	c, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireAuthAndVersion(t, r)
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"id":"https://kv/secrets/foo","recoveryId":"https://kv/deletedsecrets/foo"}`))
	}))

	if err := c.DeleteSecret(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
}

func TestKeyVault_DeleteSecret_NotFound(t *testing.T) {
	c, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))

	err := c.DeleteSecret(context.Background(), "absent")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("error = %v, want ErrSecretNotFound", err)
	}
}

// ---------- ListSecrets ----------

func TestKeyVault_ListSecrets_SinglePage(t *testing.T) {
	c, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireAuthAndVersion(t, r)
		_, _ = w.Write([]byte(`{
			"value": [
				{"id":"https://kv/secrets/alpha"},
				{"id":"https://kv/secrets/beta"}
			]
		}`))
	}))

	got, err := c.ListSecrets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("names = %v", got)
	}
}

func TestKeyVault_ListSecrets_Paginated(t *testing.T) {
	// A test server that returns page 1 with a NextLink to page 2.
	var page int
	var srvURL string

	mux := http.NewServeMux()
	mux.HandleFunc("/secrets", func(w http.ResponseWriter, r *http.Request) {
		page++
		if page == 1 {
			// NextLink points back at ourselves with a marker query.
			fmt.Fprintf(w, `{"value":[{"id":"https://kv/secrets/a"}],"nextLink":"%s/secrets?api-version=%s&page=2"}`, srvURL, kvAPIVersion)
			return
		}
		_, _ = w.Write([]byte(`{"value":[{"id":"https://kv/secrets/b"}]}`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	srvURL = srv.URL

	tokens := newTokenProviderWith(&stubCred{token: "tok-123"})
	c := NewKeyVaultClient(srv.URL, tokens)

	got, err := c.ListSecrets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("names across pages = %v", got)
	}
	if page != 2 {
		t.Errorf("server hit %d times, want 2", page)
	}
}

func TestKeyVault_ListSecrets_Empty(t *testing.T) {
	c, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))

	got, err := c.ListSecrets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("empty list expected, got %v", got)
	}
}

// ---------- SecretExists ----------

func TestKeyVault_SecretExists(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantExists bool
		wantErr    bool
	}{
		{"present", http.StatusOK, `{"value":"x"}`, true, false},
		{"absent", http.StatusNotFound, `{}`, false, false},
		{"server error", http.StatusInternalServerError, "boom", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newMockClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			got, err := c.SecretExists(context.Background(), "x")
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got != tc.wantExists {
				t.Errorf("exists = %v, want %v", got, tc.wantExists)
			}
		})
	}
}

// ---------- helpers ----------

func TestSecretNameFromID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://kv/secrets/foo", "foo"},
		{"https://kv/secrets/foo/ver123", "ver123"}, // returns last segment
		{"https://kv/secrets/", ""},
		{"", ""},
		{"noslashes", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := secretNameFromID(tc.in)
			if got != tc.want {
				t.Errorf("secretNameFromID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------- integration ----------

// TestKeyVault_Integration hits real integration vault when Azure creds are
// available. Proves the auth chain + URL shape + response decoding
// line up against live Azure.
func TestKeyVault_Integration(t *testing.T) {
	if !hasAzureCreds(t) {
		t.Skip("skipping: no Azure credentials")
	}
	tokens, err := NewTokenProvider()
	if err != nil {
		t.Fatal(err)
	}
	c := NewKeyVaultClient("https://kv-example.vault.azure.net", tokens)

	exists, err := c.SecretExists(context.Background(), "nexus-database-url")
	if err != nil {
		t.Fatalf("SecretExists failed: %v", err)
	}
	if !exists {
		t.Error("nexus-database-url should exist in integration vault — check the fixture is up to date")
	}

	names, err := c.ListSecrets(context.Background())
	if err != nil {
		t.Fatalf("ListSecrets failed: %v", err)
	}
	if len(names) == 0 {
		t.Error("ListSecrets returned empty — integration vault should have at least the nexus-* secrets")
	}
}

func TestValidateSecretName(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"db-url", true},
		{"kyc-database-url", true},
		{"a", true},
		{"abc123-XYZ", true},
		{"", false},
		{"with_underscore", false},
		{"with.dot", false},
		{"with/slash", false},
		{"with space", false},
		{"with-non-ascii-é", false},
	}
	for _, tc := range cases {
		err := ValidateSecretName(tc.name)
		if tc.ok && err != nil {
			t.Errorf("ValidateSecretName(%q) = %v, want nil", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("ValidateSecretName(%q) = nil, want error", tc.name)
		}
	}

	long := make([]byte, SecretNameMaxLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if err := ValidateSecretName(string(long)); err == nil {
		t.Errorf("ValidateSecretName(<%d a's>) = nil, want length error", len(long))
	}
}

func TestSuggestSecretName(t *testing.T) {
	cases := map[string]string{
		"kyc-database_url":         "kyc-database-url",
		"kyc__double":              "kyc-double",
		"_leading":                 "leading",
		"trailing_":                "trailing",
		"a.b.c":                    "a-b-c",
		"already-good":             "already-good",
		"with spaces and-symbols!": "with-spaces-and-symbols",
	}
	for in, want := range cases {
		if got := SuggestSecretName(in); got != want {
			t.Errorf("SuggestSecretName(%q) = %q, want %q", in, got, want)
		}
	}
}

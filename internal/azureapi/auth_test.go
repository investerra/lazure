package azureapi

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// fakeCred is a mock credential that returns scripted tokens, with
// configurable expiry and call counting.
type fakeCred struct {
	mu        sync.Mutex
	calls     int
	callsPer  map[string]int
	token     string
	expiresIn time.Duration
	err       error
}

func (f *fakeCred) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.callsPer == nil {
		f.callsPer = map[string]int{}
	}
	for _, s := range opts.Scopes {
		f.callsPer[s]++
	}
	if f.err != nil {
		return azcore.AccessToken{}, f.err
	}
	return azcore.AccessToken{
		Token:     f.token,
		ExpiresOn: time.Now().Add(f.expiresIn),
	}, nil
}

func TestTokenProvider_Caches(t *testing.T) {
	fc := &fakeCred{token: "tok-1", expiresIn: 5 * time.Minute}
	p := newTokenProviderWith(fc)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		got, err := p.Token(ctx, ScopeManagement)
		if err != nil {
			t.Fatal(err)
		}
		if got != "tok-1" {
			t.Errorf("token = %q, want tok-1", got)
		}
	}
	if fc.calls != 1 {
		t.Errorf("GetToken called %d times, want 1 (cached)", fc.calls)
	}
}

func TestTokenProvider_SeparateCachePerScope(t *testing.T) {
	fc := &fakeCred{token: "any", expiresIn: 5 * time.Minute}
	p := newTokenProviderWith(fc)
	ctx := context.Background()

	_, _ = p.Token(ctx, ScopeManagement)
	_, _ = p.Token(ctx, ScopeKeyVault)
	_, _ = p.Token(ctx, ScopeManagement) // cached
	_, _ = p.Token(ctx, ScopeKeyVault)   // cached

	if fc.calls != 2 {
		t.Errorf("GetToken called %d times, want 2 (per scope)", fc.calls)
	}
	if fc.callsPer[ScopeManagement] != 1 || fc.callsPer[ScopeKeyVault] != 1 {
		t.Errorf("per-scope calls = %+v, want each = 1", fc.callsPer)
	}
}

func TestTokenProvider_RefreshesWhenExpired(t *testing.T) {
	// Token expires in 10s → with 30s buffer, it's always "expired".
	fc := &fakeCred{token: "any", expiresIn: 10 * time.Second}
	p := newTokenProviderWith(fc)
	ctx := context.Background()

	_, _ = p.Token(ctx, ScopeManagement)
	_, _ = p.Token(ctx, ScopeManagement)

	if fc.calls != 2 {
		t.Errorf("GetToken called %d times, want 2 (refreshed every call due to buffer)", fc.calls)
	}
}

func TestTokenProvider_RefreshBufferRespected(t *testing.T) {
	// Token expires far in future → cached the entire test.
	fc := &fakeCred{token: "any", expiresIn: 2 * time.Hour}
	p := newTokenProviderWith(fc)

	// Use an injectable clock that advances below the refresh buffer
	// threshold between calls. After 10 minutes, token still has ~110
	// minutes left → well above 30s buffer → should stay cached.
	start := time.Now()
	p.now = func() time.Time { return start }
	_, _ = p.Token(context.Background(), ScopeManagement)

	p.now = func() time.Time { return start.Add(10 * time.Minute) }
	_, _ = p.Token(context.Background(), ScopeManagement)

	if fc.calls != 1 {
		t.Errorf("GetToken called %d times, want 1", fc.calls)
	}

	// Fast-forward to just past (expiresOn - buffer).
	p.now = func() time.Time { return start.Add(2*time.Hour - 20*time.Second) }
	_, _ = p.Token(context.Background(), ScopeManagement)

	if fc.calls != 2 {
		t.Errorf("GetToken called %d times after crossing buffer, want 2", fc.calls)
	}
}

func TestTokenProvider_ErrorTagged(t *testing.T) {
	fc := &fakeCred{err: errors.New("no credentials")}
	p := newTokenProviderWith(fc)

	_, err := p.Token(context.Background(), ScopeManagement)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no credentials") {
		t.Errorf("error message missing underlying cause: %q", err.Error())
	}
	// Tagged as Auth → code 2
	// (We only test message + shape here; errs.Code classification has
	// its own test coverage in internal/errs.)
}

func TestTokenProvider_ConvenienceMethods(t *testing.T) {
	fc := &fakeCred{token: "any", expiresIn: 5 * time.Minute}
	p := newTokenProviderWith(fc)
	ctx := context.Background()

	if _, err := p.Management(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.KeyVault(ctx); err != nil {
		t.Fatal(err)
	}

	if fc.callsPer[ScopeManagement] != 1 || fc.callsPer[ScopeKeyVault] != 1 {
		t.Errorf("convenience methods hit wrong scopes: %+v", fc.callsPer)
	}
}

func TestTokenProvider_ConcurrentAccess(t *testing.T) {
	// Verify the mutex actually serializes — start many goroutines all
	// requesting the same scope; GetToken should be called exactly once.
	fc := &fakeCred{token: "tok", expiresIn: 5 * time.Minute}
	p := newTokenProviderWith(fc)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Token(context.Background(), ScopeManagement)
		}()
	}
	wg.Wait()

	if fc.calls != 1 {
		t.Errorf("GetToken called %d times under concurrency, want 1", fc.calls)
	}
}

// TestNewTokenProvider_Integration checks the real constructor works
// when the machine has Azure credentials. Skips otherwise.
func TestNewTokenProvider_Integration(t *testing.T) {
	if err := exec.Command("az", "account", "show").Run(); err != nil {
		t.Skip("skipping: no Azure credentials available")
	}
	p, err := NewTokenProvider()
	if err != nil {
		t.Fatal(err)
	}
	tok, err := p.Management(context.Background())
	if err != nil {
		t.Fatalf("Management token fetch failed: %v", err)
	}
	if tok == "" {
		t.Error("Management returned empty token")
	}
	if !strings.HasPrefix(tok, "eyJ") {
		// JWTs start with "eyJ" (base64-encoded '{').
		t.Errorf("token doesn't look like a JWT: %q...", tok[:min(20, len(tok))])
	}
}

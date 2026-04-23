// Package azureapi wraps direct calls to the Azure ARM and Key Vault
// REST APIs. All calls go through a single TokenProvider that uses
// azidentity.DefaultAzureCredential under the hood — same auth chain
// as `az` (env vars → managed identity → az CLI → VS Code).
package azureapi

import (
	"context"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/investerra/lazure/internal/errs"
)

// Common scope strings for the two Azure services lazure calls.
// Tokens are cached per scope; requesting different scopes triggers
// separate token fetches.
const (
	ScopeManagement = "https://management.azure.com/.default"
	ScopeKeyVault   = "https://vault.azure.net/.default"
)

// tokenRefreshBuffer is how far in advance of the real expiry we treat
// a cached token as expired. 30 s absorbs clock skew and any in-flight
// requests that started just before the real expiration.
const tokenRefreshBuffer = 30 * time.Second

// credential is the subset of azcore.TokenCredential TokenProvider needs.
// Declaring it as a local interface lets tests substitute a fake without
// pulling in azidentity.
type credential interface {
	GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error)
}

// TokenProvider fetches and caches Azure bearer tokens. Safe for
// concurrent use — the internal cache is mutex-protected.
type TokenProvider struct {
	cred  credential
	mu    sync.Mutex
	cache map[string]cachedToken
	now   func() time.Time // injectable for tests
}

type cachedToken struct {
	token     string
	expiresOn time.Time
}

// NewTokenProvider returns a TokenProvider backed by
// azidentity.DefaultAzureCredential. That credential walks the standard
// chain: env vars → workload identity → managed identity → Azure CLI
// (az login) → Azure Developer CLI → VS Code.
func NewTokenProvider() (*TokenProvider, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, errs.Auth(errs.Wrap(err, "azureapi: create default credential"))
	}
	return newTokenProviderWith(cred), nil
}

// newTokenProviderWith is the test seam — the constructor that accepts
// any credential implementation.
func newTokenProviderWith(cred credential) *TokenProvider {
	return &TokenProvider{
		cred:  cred,
		cache: map[string]cachedToken{},
		now:   time.Now,
	}
}

// Token returns a bearer token for the given scope. Hits the cache if a
// previous token is still valid (expires_on minus a refresh buffer);
// otherwise fetches a fresh token from the credential chain.
//
// Errors from the credential chain are tagged Auth so main.go maps
// them to exit code 2 (operator-fixable: run `az login` or fix env).
func (p *TokenProvider) Token(ctx context.Context, scope string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if ct, ok := p.cache[scope]; ok {
		if p.now().Before(ct.expiresOn.Add(-tokenRefreshBuffer)) {
			return ct.token, nil
		}
	}

	tok, err := p.cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{scope},
	})
	if err != nil {
		return "", errs.Auth(errs.Wrapf(err, "azureapi: get token for %s", scope))
	}

	p.cache[scope] = cachedToken{
		token:     tok.Token,
		expiresOn: tok.ExpiresOn,
	}
	return tok.Token, nil
}

// Management is a convenience for Token(ctx, ScopeManagement) — the
// most common call site (ARM container apps, diff).
func (p *TokenProvider) Management(ctx context.Context) (string, error) {
	return p.Token(ctx, ScopeManagement)
}

// KeyVault is a convenience for Token(ctx, ScopeKeyVault) — used by
// secrets view/sync/verify against the vault REST API.
func (p *TokenProvider) KeyVault(ctx context.Context) (string, error) {
	return p.Token(ctx, ScopeKeyVault)
}

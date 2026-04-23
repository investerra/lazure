package azureapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/imroc/req/v3"

	"github.com/investerra/lazure/internal/errs"
)

// kvAPIVersion is the Key Vault data-plane REST API version we pin to.
// 7.4 is the current stable release and supports all secrets operations
// lazure needs (get/put/delete/list).
const kvAPIVersion = "7.4"

// ErrSecretNotFound is returned by GetSecret / DeleteSecret when the
// named secret doesn't exist in the vault. Use errors.Is for matching.
var ErrSecretNotFound = errors.New("keyvault: secret not found")

// KeyVaultClient is a thin wrapper around the Azure Key Vault data-plane
// REST API. Zero state beyond the vault URL, an auth provider, and an
// HTTP client.
type KeyVaultClient struct {
	base   string
	tokens *TokenProvider
	client *req.Client
}

// NewKeyVaultClient returns a client bound to vaultURL (e.g.
// "https://kv-example.vault.azure.net"). Trailing slashes are
// trimmed so callers can pass whichever form is convenient.
func NewKeyVaultClient(vaultURL string, tokens *TokenProvider) *KeyVaultClient {
	return &KeyVaultClient{
		base:   strings.TrimRight(vaultURL, "/"),
		tokens: tokens,
		client: req.C(),
	}
}

// SecretMetadata is the subset of an Azure secret descriptor that list
// responses populate (list does NOT return the secret value itself).
type SecretMetadata struct {
	Name string
	ID   string
}

// GetSecret fetches the latest version of a secret's value. Returns
// ErrSecretNotFound if the secret doesn't exist.
func (c *KeyVaultClient) GetSecret(ctx context.Context, name string) (string, error) {
	r, err := c.authedRequest(ctx)
	if err != nil {
		return "", err
	}
	var body struct {
		Value string `json:"value"`
	}
	resp, err := r.SetSuccessResult(&body).Get(c.base + "/secrets/" + name)
	if err != nil {
		return "", errs.Wrapf(err, "keyvault: get secret %q", name)
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrSecretNotFound
	}
	if !resp.IsSuccessState() {
		return "", errs.Errorf("keyvault: get secret %q: %s", name, resp.Status)
	}
	return body.Value, nil
}

// PutSecret creates or updates a secret with the given value. Azure
// versions each PUT as a new secret version — earlier versions remain
// readable under their specific version id.
func (c *KeyVaultClient) PutSecret(ctx context.Context, name, value string) error {
	r, err := c.authedRequest(ctx)
	if err != nil {
		return err
	}
	resp, err := r.SetBody(map[string]string{"value": value}).Put(c.base + "/secrets/" + name)
	if err != nil {
		return errs.Wrapf(err, "keyvault: put secret %q", name)
	}
	if !resp.IsSuccessState() {
		return errs.Errorf("keyvault: put secret %q: %s %s", name, resp.Status, resp.String())
	}
	return nil
}

// DeleteSecret soft-deletes a secret. The secret is retained for the
// vault's soft-delete retention period before permanent purge and can
// be recovered with the Azure data-plane recovery API (out of scope
// for lazure). Returns ErrSecretNotFound if the secret doesn't exist.
func (c *KeyVaultClient) DeleteSecret(ctx context.Context, name string) error {
	r, err := c.authedRequest(ctx)
	if err != nil {
		return err
	}
	resp, err := r.Delete(c.base + "/secrets/" + name)
	if err != nil {
		return errs.Wrapf(err, "keyvault: delete secret %q", name)
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrSecretNotFound
	}
	if !resp.IsSuccessState() {
		return errs.Errorf("keyvault: delete secret %q: %s", name, resp.Status)
	}
	return nil
}

// ListSecrets returns the names of every secret in the vault (not their
// values — Azure's list API omits values by design). Pagination is
// handled internally via the NextLink field.
func (c *KeyVaultClient) ListSecrets(ctx context.Context) ([]string, error) {
	var out []string
	url := c.base + "/secrets"
	firstPage := true
	for {
		r, err := c.authedRequest(ctx)
		if err != nil {
			return nil, err
		}
		// On the first page we add the api-version query; the nextLink
		// URL returned by Azure already includes api-version + skiptoken,
		// so we leave it alone on subsequent iterations.
		if firstPage {
			// nothing to do — authedRequest already set api-version
		} else {
			// Clear the query param set by authedRequest so it doesn't
			// double-append; nextLink is complete.
			r.QueryParams = nil
		}
		var page struct {
			Value []struct {
				ID string `json:"id"`
			} `json:"value"`
			NextLink string `json:"nextLink"`
		}
		resp, err := r.SetSuccessResult(&page).Get(url)
		if err != nil {
			return nil, errs.Wrap(err, "keyvault: list secrets")
		}
		if !resp.IsSuccessState() {
			return nil, errs.Errorf("keyvault: list secrets: %s", resp.Status)
		}
		for _, item := range page.Value {
			name := secretNameFromID(item.ID)
			if name != "" {
				out = append(out, name)
			}
		}
		if page.NextLink == "" {
			break
		}
		url = page.NextLink
		firstPage = false
	}
	return out, nil
}

// SecretExists returns true if a secret with the given name exists in
// the vault. Built on top of GetSecret — any non-NotFound error
// propagates unchanged.
func (c *KeyVaultClient) SecretExists(ctx context.Context, name string) (bool, error) {
	_, err := c.GetSecret(ctx, name)
	if errors.Is(err, ErrSecretNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// authedRequest starts a new req.Request with the bearer token and
// api-version query already attached.
func (c *KeyVaultClient) authedRequest(ctx context.Context) (*req.Request, error) {
	tok, err := c.tokens.KeyVault(ctx)
	if err != nil {
		return nil, err
	}
	r := c.client.R().
		SetContext(ctx).
		SetBearerAuthToken(tok).
		SetQueryParam("api-version", kvAPIVersion).
		SetHeader("Content-Type", "application/json")
	return r, nil
}

// secretNameFromID returns the trailing segment of a Key Vault secret
// id URL. Example: "https://kv.vault.azure.net/secrets/foo" → "foo".
func secretNameFromID(id string) string {
	idx := strings.LastIndex(id, "/")
	if idx < 0 || idx == len(id)-1 {
		return ""
	}
	return id[idx+1:]
}

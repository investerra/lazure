package azureapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/imroc/req/v3"

	"github.com/investerra/lazure/internal/errs"
)

// SecretNameRE is Azure Key Vault's accepted character class for secret
// names: alphanumeric ASCII plus hyphens. Underscores, dots, slashes
// and any non-ASCII rune are rejected by the data-plane API with a
// 400 BadParameter ("The request URI contains an invalid name: …"),
// and the same name flowing into a container app's keyVaultUrl is
// rejected with the (misleading) ContainerAppSecretKeyVaultUrlInvalid
// "different cloud" message at deploy time. Source of truth for both
// the local pre-flight checks and any future manifest-level validation.
var SecretNameRE = regexp.MustCompile(`^[0-9a-zA-Z-]+$`)

// SecretNameMaxLen is Azure Key Vault's documented maximum length for
// a secret name (Azure rejects 128+ chars at the data plane).
const SecretNameMaxLen = 127

// ValidateSecretName returns nil if name is acceptable to Azure Key
// Vault, otherwise an error describing the violation. Cheap regex
// check — safe to call from validation paths and command handlers.
func ValidateSecretName(name string) error {
	if name == "" {
		return errs.Errorf("secret name is empty")
	}
	if len(name) > SecretNameMaxLen {
		return errs.Errorf("secret name %q is %d chars (max %d)", name, len(name), SecretNameMaxLen)
	}
	if !SecretNameRE.MatchString(name) {
		return errs.Errorf("secret name %q is invalid: only alphanumeric and hyphens are allowed (no underscores, dots or other punctuation)", name)
	}
	return nil
}

// SuggestSecretName returns a best-effort hyphenated form of name
// suitable as a rename hint in error messages. Replaces every run of
// invalid characters with a single hyphen and strips leading/trailing
// hyphens. Pure formatting — never returns an empty string for a
// non-empty input that contains at least one ASCII alphanumeric.
func SuggestSecretName(name string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range name {
		switch {
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// kvAPIVersion is the Key Vault data-plane REST API version we pin to.
// 7.6 is the latest stable release and supports all secrets operations
// lazure needs (get/put/delete/list).
const kvAPIVersion = "7.6"

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

// GetSecret fetches the latest version of a secret's value. Returns
// ErrSecretNotFound if the secret doesn't exist.
func (c *KeyVaultClient) GetSecret(ctx context.Context, name string) (string, error) {
	r, err := c.authedRequest(ctx)
	if err != nil {
		return "", err
	}
	slog.Debug("keyvault: GET secret", "name", name)
	var body struct {
		Value string `json:"value"`
	}
	resp, err := r.SetSuccessResult(&body).Get(c.base + "/secrets/" + name)
	if err != nil {
		return "", errs.Wrapf(err, "keyvault: get secret %q", name)
	}
	slog.Debug("keyvault: GET secret response", "name", name, "status", resp.StatusCode)
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrSecretNotFound
	}
	if !resp.IsSuccessState() {
		return "", errs.Errorf("keyvault: get secret %q: %s", name, formatAzureError(name, resp))
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
	slog.Debug("keyvault: PUT secret", "name", name, "value_bytes", len(value))
	resp, err := r.SetBody(map[string]string{"value": value}).Put(c.base + "/secrets/" + name)
	if err != nil {
		return errs.Wrapf(err, "keyvault: put secret %q", name)
	}
	slog.Debug("keyvault: PUT secret response", "name", name, "status", resp.StatusCode)
	if !resp.IsSuccessState() {
		return errs.Errorf("keyvault: put secret %q: %s", name, formatAzureError(name, resp))
	}
	return nil
}

// azureErrorBody mirrors the JSON shape Azure returns on 4xx/5xx for
// data-plane endpoints: {"error": {"code": "...", "message": "..."}}.
type azureErrorBody struct {
	Error struct {
		Code       string `json:"code"`
		Message    string `json:"message"`
		InnerError *struct {
			Code string `json:"code"`
		} `json:"innererror,omitempty"`
	} `json:"error"`
}

// formatAzureError renders a non-success Azure response into a string
// for surfacing to the user: HTTP status, the Azure error code (enum
// like "BadParameter") and the human-readable message Azure returned.
// The full raw body is also logged at debug, where `--verbose` reaches
// it. `secretName` is the call-site secret being mutated (logged, not
// embedded in the returned string) so debug records link back to the
// goroutine that produced the error in concurrent paths.
//
// Caveat: Azure's `message` field can occasionally echo the offending
// value back (e.g. validation errors of the form "value 'X' is …").
// Surfacing it inline is a deliberate trade — operators asked to see
// what Azure actually said, and the alternative (status + code only)
// rendered most failures undebuggable without separately re-running
// under --verbose.
func formatAzureError(secretName string, resp *req.Response) string {
	body := resp.String()
	slog.Debug("keyvault: error response body", "name", secretName, "status", resp.Status, "body", body)

	var ae azureErrorBody
	if err := json.Unmarshal([]byte(body), &ae); err == nil && ae.Error.Code != "" {
		code := ae.Error.Code
		if ae.Error.InnerError != nil && ae.Error.InnerError.Code != "" {
			code += "/" + ae.Error.InnerError.Code
		}
		if msg := strings.TrimSpace(ae.Error.Message); msg != "" {
			return resp.Status + " (" + code + "): " + msg
		}
		return resp.Status + " (" + code + ")"
	}
	return resp.Status
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
	slog.Debug("keyvault: DELETE secret", "name", name)
	resp, err := r.Delete(c.base + "/secrets/" + name)
	if err != nil {
		return errs.Wrapf(err, "keyvault: delete secret %q", name)
	}
	slog.Debug("keyvault: DELETE secret response", "name", name, "status", resp.StatusCode)
	if resp.StatusCode == http.StatusNotFound {
		return ErrSecretNotFound
	}
	if !resp.IsSuccessState() {
		return errs.Errorf("keyvault: delete secret %q: %s", name, formatAzureError(name, resp))
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
	slog.Debug("keyvault: LIST secrets")
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
	slog.Debug("keyvault: LIST secrets done", "count", len(out))
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

package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"text/tabwriter"

	"github.com/urfave/cli/v3"
	"golang.org/x/sync/errgroup"
	"sigs.k8s.io/yaml"

	"github.com/investerra/lazure/internal/azureapi"
	"github.com/investerra/lazure/internal/errs"
	"github.com/investerra/lazure/internal/lazurecfg"
	"github.com/investerra/lazure/internal/sopsio"
	"github.com/investerra/lazure/internal/verify"
)

// SecretsCommand returns the `lazure secrets` subcommand with its four
// actions: view, edit, verify, sync.
func SecretsCommand() *cli.Command {
	return &cli.Command{
		Name:  "secrets",
		Usage: "manage encrypted secrets",
		Commands: []*cli.Command{
			{
				Name:          "new",
				Usage:         "create an empty encrypted secrets file (errors if file already exists)",
				Arguments:     envArgs(),
				Action:        SecretsNew,
				ShellComplete: CompleteEnvs,
			},
			{
				Name:      "view",
				Usage:     "view secrets (redacted by default; --reveal to show full values)",
				Arguments: envArgs(),
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "reveal", Usage: "show full secret values (default: redacted)"},
					&cli.StringFlag{Name: "format", Usage: "output format: table|json", Value: "table"},
				},
				Action:        SecretsView,
				ShellComplete: CompleteEnvs,
			},
			{
				Name:          "edit",
				Usage:         "decrypt, open in $EDITOR, re-encrypt",
				Arguments:     envArgs(),
				Action:        SecretsEdit,
				ShellComplete: CompleteEnvs,
			},
			{
				Name:      "export",
				Usage:     "print decrypted secrets as `export KEY=VAL` lines (only env vars referenced in deploy.yml)",
				Arguments: envArgs(),
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "container", Usage: "container name (default: first non-init container)"},
				},
				Action:        SecretsExport,
				ShellComplete: CompleteEnvs,
			},
			{
				Name:      "decrypt",
				Usage:     "decrypt to envs/<env>.secrets.plain.yml",
				Arguments: envArgs(),
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "force", Usage: "overwrite an existing plain file"},
				},
				Action:        SecretsDecrypt,
				ShellComplete: CompleteEnvs,
			},
			{
				Name:      "encrypt",
				Usage:     "encrypt envs/<env>.secrets.plain.yml back to .secrets.yml",
				Arguments: envArgs(),
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "keep", Usage: "keep the plain file after successful encryption"},
				},
				Action:        SecretsEncrypt,
				ShellComplete: CompleteEnvs,
			},
			{
				Name:      "verify",
				Usage:     "check that every {secret: X} reference in the manifest exists in SOPS",
				Arguments: envArgs(),
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "check-kv", Usage: "also verify each referenced secret exists in Key Vault"},
				},
				Action:        SecretsVerify,
				ShellComplete: CompleteEnvs,
			},
			{
				Name:      "sync",
				Usage:     "upload all SOPS secrets to Key Vault",
				Arguments: envArgs(),
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "dry-run", Usage: "print planned PUT operations without writing to Key Vault"},
					&cli.IntFlag{Name: "concurrency", Usage: "parallel HTTP calls", Value: 10},
					&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "skip the confirmation prompt"},
				},
				Action:        SecretsSync,
				ShellComplete: CompleteEnvs,
			},
		},
	}
}

// envArgs is the positional `env` argument used by every secrets subcommand.
func envArgs() []cli.Argument {
	return []cli.Argument{
		&cli.StringArg{Name: "env", UsageText: "target environment (dev|uat|prd|...)"},
	}
}

// ---------- view ----------

// SecretsView implements `lazure secrets view <env>`.
func SecretsView(ctx context.Context, c *cli.Command) error {
	env, encPath, err := secretsEnvPath(c)
	if err != nil {
		return err
	}
	slog.Debug("secrets view: start", "env", env, "path", encPath)

	slog.Debug("secrets view: decrypting")
	decrypted, err := sopsio.Decrypt(encPath)
	if err != nil {
		return errs.Usage(errs.Wrap(err, "secrets view"))
	}
	slog.Debug("secrets view: decrypted", "count", len(decrypted))

	reveal := c.Bool("reveal")
	format := c.String("format")
	switch format {
	case "json":
		return printSecretsJSON(decrypted, reveal)
	case "table", "":
		return printSecretsTable(env, decrypted, reveal)
	default:
		return errs.Usage(errs.Errorf("invalid format %q (want table|json)", format))
	}
}

func printSecretsTable(env string, secrets map[string]string, reveal bool) error {
	keys := sortedKeys(secrets)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "# secrets for %s\n", env)
	fmt.Fprintln(tw, "NAME\tVALUE")
	for _, k := range keys {
		v := secrets[k]
		if !reveal {
			v = redact(v)
		}
		fmt.Fprintf(tw, "%s\t%s\n", k, v)
	}
	return tw.Flush()
}

func printSecretsJSON(secrets map[string]string, reveal bool) error {
	out := secrets
	if !reveal {
		masked := make(map[string]string, len(secrets))
		for k, v := range secrets {
			masked[k] = redact(v)
		}
		out = masked
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return errs.System(errs.Wrap(err, "secrets view: marshal"))
	}
	fmt.Println(string(data))
	return nil
}

// redact shows the first and last 3 characters of a secret value to
// preserve spot-check ability without leaking the whole value. Short
// values collapse to "***".
func redact(v string) string {
	if len(v) <= 6 {
		return "***"
	}
	return v[:3] + "…" + v[len(v)-3:]
}

// ---------- edit ----------

// SecretsEdit implements `lazure secrets edit <env>`. Decrypts to a
// sidecar .plain.yml file, opens $EDITOR, re-encrypts if changed.
// Refuses if a prior .plain.yml is already present — the user must
// review and delete it before a fresh edit can start.
func SecretsEdit(ctx context.Context, c *cli.Command) error {
	env, encPath, err := secretsEnvPath(c)
	if err != nil {
		return err
	}
	plainPath := strings.TrimSuffix(encPath, ".yml") + ".plain.yml"
	slog.Debug("secrets edit: start", "env", env, "encrypted", encPath, "plain", plainPath)

	if _, err := os.Stat(plainPath); err == nil {
		return errs.Usage(errs.Errorf(
			"plain file %q already exists (previous edit didn't finish cleanly). Review and delete it before a new edit",
			plainPath))
	}

	slog.Debug("secrets edit: decrypting")
	secrets, err := sopsio.Decrypt(encPath)
	if err != nil {
		return errs.Usage(errs.Wrap(err, "secrets edit: decrypt"))
	}
	slog.Debug("secrets edit: decrypted", "count", len(secrets))
	plainContent, err := marshalPlainSecrets(secrets)
	if err != nil {
		return errs.System(errs.Wrap(err, "secrets edit: format plain"))
	}
	if err := os.WriteFile(plainPath, plainContent, 0o600); err != nil {
		return errs.System(errs.Wrapf(err, "secrets edit: write %s", plainPath))
	}
	origHash := sha256.Sum256(plainContent)

	// Open the user's editor. POSIX precedence is $VISUAL → $EDITOR
	// (interactive editors typically prefer VISUAL); fall back to
	// EDITOR for environments that only set the older variable.
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		return errs.System(errs.New("neither $VISUAL nor $EDITOR is set; run e.g. 'export EDITOR=vim' and retry"))
	}
	slog.Info("opening editor", "editor", editor, "path", plainPath)
	// exec.Command (NOT CommandContext) on purpose: SIGINT to lazure
	// would otherwise propagate as ctx.Done → SIGKILL to the editor,
	// destroying any unsaved buffer the user is mid-edit on. With
	// plain Command the editor receives SIGINT directly via the
	// terminal's foreground process group and decides itself how to
	// handle it (vim/nano/emacs all handle this gracefully).
	edit := exec.Command(editor, plainPath)
	edit.Stdin, edit.Stdout, edit.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := edit.Run(); err != nil {
		return errs.System(errs.Wrapf(err, "secrets edit: $EDITOR exited with error"))
	}

	// Detect change; if nothing changed, delete plain and bail.
	newContent, err := os.ReadFile(plainPath)
	if err != nil {
		return errs.System(errs.Wrapf(err, "secrets edit: read %s after edit", plainPath))
	}
	if sha256.Sum256(newContent) == origHash {
		slog.Info("secrets unchanged; nothing to re-encrypt", "env", env)
		_ = os.Remove(plainPath)
		return nil
	}
	slog.Debug("secrets edit: change detected, validating names")

	parsed, err := parsePlainSecrets(newContent)
	if err != nil {
		// Plain file is left in place so the user can fix the YAML
		// without losing their edit. Re-running `lazure secrets edit`
		// would hit the stale-plain guard, so direct them to encrypt
		// once the file is valid.
		return errs.Validation(errs.Wrapf(err,
			"secrets edit: %s isn't valid YAML — fix it and run `lazure secrets encrypt %s`", plainPath, env))
	}
	if err := validateSecretNamesMap(parsed); err != nil {
		return errs.Validation(errs.Wrapf(err,
			"secrets edit: invalid name(s) in %s — fix them and run `lazure secrets encrypt %s`", plainPath, env))
	}

	slog.Debug("secrets edit: re-encrypting")
	if err := sopsio.Encrypt(plainPath, encPath, sopsConfigPath(c.String("dir"))); err != nil {
		return errs.System(errs.Wrap(err, "secrets edit: encrypt"))
	}
	slog.Debug("secrets edit: re-encrypted successfully")
	if err := os.Remove(plainPath); err != nil {
		slog.Warn("secrets edit: encrypted OK but failed to clean up plain file", "path", plainPath, "error", err)
	}

	slog.Info("secrets updated", "env", env)
	return nil
}

// marshalPlainSecrets writes a deterministic YAML representation of the
// decrypted map — alphabetical keys, JSON-escaped values — so diffs
// across edits are minimal.
//
// Values are emitted via encoding/json which guarantees single-line,
// double-quoted output with proper \n / \" / \uXXXX escapes. JSON
// strings are a strict subset of YAML double-quoted strings, so the
// output round-trips through any YAML parser. This avoids
// sigs.k8s.io/yaml's habit of switching to multi-line block scalars
// for values containing newlines — which broke the `key: <inline>`
// shape used here and silently produced invalid YAML on re-decrypt.
func marshalPlainSecrets(secrets map[string]string) ([]byte, error) {
	keys := sortedKeys(secrets)
	var buf strings.Builder
	buf.WriteString("# Decrypted secrets — DO NOT COMMIT. Delete this file when done editing.\n")
	buf.WriteString("# Names must match ^[0-9a-zA-Z-]+$ (alphanumeric + hyphens, no underscores)\n")
	buf.WriteString(fmt.Sprintf("# and be at most %d characters — Azure Key Vault rejects anything else.\n", azureapi.SecretNameMaxLen))
	for _, k := range keys {
		v, err := json.Marshal(secrets[k])
		if err != nil {
			return nil, err
		}
		buf.WriteString(k)
		buf.WriteString(": ")
		buf.Write(v)
		buf.WriteString("\n")
	}
	return []byte(buf.String()), nil
}

// validateSecretNamesMap runs Azure's name rule against every key in
// the given map. Returns a single multi-line error listing each bad
// name with a hyphenated suggestion — designed to be the diff a user
// can apply directly. nil when every name is valid.
func validateSecretNamesMap(secrets map[string]string) error {
	var bad []string
	for name := range secrets {
		if err := azureapi.ValidateSecretName(name); err != nil {
			bad = append(bad, name)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	lines := make([]string, 0, len(bad))
	for _, n := range bad {
		lines = append(lines, fmt.Sprintf("%s → %s", n, azureapi.SuggestSecretName(n)))
	}
	return errs.Errorf(
		"%d invalid secret name(s) — Azure Key Vault requires ^[0-9a-zA-Z-]+$ (alphanumeric + hyphens, no underscores), max %d chars:\n  %s",
		len(bad), azureapi.SecretNameMaxLen, strings.Join(lines, "\n  "),
	)
}

// parsePlainSecrets unmarshals a plain-text secrets file (the format
// emitted by marshalPlainSecrets) back into a map. Used by the post-
// edit validation path — we only need the keys, but the full map is
// the natural unit and trivial to produce.
func parsePlainSecrets(content []byte) (map[string]string, error) {
	out := map[string]string{}
	if err := yaml.Unmarshal(content, &out); err != nil {
		return nil, errs.Wrap(err, "parse plain secrets")
	}
	return out, nil
}

// ---------- decrypt ----------

// SecretsDecrypt implements `lazure secrets decrypt <env>`. Writes
// envs/<env>.secrets.plain.yml from the encrypted file. Refuses to
// overwrite an existing plain file unless --force — silently
// clobbering would lose any in-progress edits the user hasn't
// re-encrypted yet.
func SecretsDecrypt(ctx context.Context, c *cli.Command) error {
	env, encPath, err := secretsEnvPath(c)
	if err != nil {
		return err
	}
	plainPath := strings.TrimSuffix(encPath, ".yml") + ".plain.yml"
	force := c.Bool("force")
	slog.Debug("secrets decrypt: start", "env", env, "encrypted", encPath, "plain", plainPath, "force", force)

	if _, err := os.Stat(plainPath); err == nil && !force {
		return errs.Usage(errs.Errorf(
			"plain file %q already exists; pass --force to overwrite", plainPath))
	}

	secrets, err := sopsio.Decrypt(encPath)
	if err != nil {
		return errs.Usage(errs.Wrap(err, "secrets decrypt"))
	}
	plainContent, err := marshalPlainSecrets(secrets)
	if err != nil {
		return errs.System(errs.Wrap(err, "secrets decrypt: format plain"))
	}
	if err := os.WriteFile(plainPath, plainContent, 0o600); err != nil {
		return errs.System(errs.Wrapf(err, "secrets decrypt: write %s", plainPath))
	}
	slog.Info("secrets decrypted", "env", env, "path", plainPath, "count", len(secrets))
	return nil
}

// ---------- new ----------

// SecretsNew implements `lazure secrets new <env>`. Scaffolds an
// empty encrypted secrets file. Errors if the encrypted file already
// exists — the caller should `lazure secrets edit` instead, which is
// the safe way to mutate existing content.
//
// On success, removes any stale envs/<env>.secrets.plain.yml left
// over from older `lazure init` runs (current init no longer writes
// one, but kyc-style legacy state may still have them).
func SecretsNew(ctx context.Context, c *cli.Command) error {
	env, encPath, err := secretsEnvPath(c)
	if err != nil {
		return err
	}
	slog.Debug("secrets new: start", "env", env, "path", encPath)

	if _, err := os.Stat(encPath); err == nil {
		return errs.Usage(errs.Errorf("secrets new: %s already exists", encPath))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return errs.System(errs.Wrapf(err, "secrets new: stat %s", encPath))
	}

	configPath := sopsConfigPath(c.String("dir"))
	if err := createEmptyEncryptedSecrets(encPath, configPath); err != nil {
		return errs.System(errs.Wrap(err, "secrets new"))
	}

	plainPath := strings.TrimSuffix(encPath, ".yml") + ".plain.yml"
	if err := os.Remove(plainPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("secrets new: encrypted OK but failed to clean up legacy plain sidecar",
			"path", plainPath, "error", err)
	}

	slog.Info("secrets file created", "env", env, "path", encPath)
	return nil
}

// createEmptyEncryptedSecrets writes an empty encrypted SOPS file at
// encPath, using configPath (.sops.yaml) for master keys. Shared by
// `secrets new` and `init`. Errors if encPath already exists — never
// overwrites, because sopsio.Encrypt would otherwise route an
// existing path through the re-encrypt branch and silently replace
// real secrets with `{}`.
//
// Uses `{}` as the seed plaintext: a comment-only YAML errors with
// EOF, truly empty bytes give zero branches, but `{}` parses to one
// empty map — exactly what `sops` itself produces for an empty file.
func createEmptyEncryptedSecrets(encPath, configPath string) error {
	if _, err := os.Stat(encPath); err == nil {
		return errs.Errorf("refusing to overwrite existing %s with empty content", encPath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return errs.Wrapf(err, "stat %s", encPath)
	}

	envsDir := filepath.Dir(encPath)
	if err := os.MkdirAll(envsDir, 0o755); err != nil {
		return errs.Wrap(err, "mkdir envs")
	}
	tmp, err := os.CreateTemp(envsDir, ".lazure-new-*.plain.yml")
	if err != nil {
		return errs.Wrap(err, "tmp")
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString("{}\n"); err != nil {
		_ = tmp.Close()
		return errs.Wrap(err, "write tmp")
	}
	if err := tmp.Close(); err != nil {
		return errs.Wrap(err, "close tmp")
	}
	return sopsio.Encrypt(tmpPath, encPath, configPath)
}

// sopsConfigPath returns the conventional .sops.yaml location given
// the project's `--dir` value: parent of the deploy directory.
// filepath.Clean normalizes trailing slashes (`deploy/` → `deploy`)
// so `--dir=deploy/` resolves the same as `--dir=deploy`.
func sopsConfigPath(dir string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(dir)), ".sops.yaml")
}

// ---------- encrypt ----------

// SecretsEncrypt implements `lazure secrets encrypt <env>`. Encrypts
// envs/<env>.secrets.plain.yml back into the .secrets.yml file,
// reusing the existing master-key metadata. Deletes the plain file on
// success unless --keep is passed — leaving plaintext lying around
// is the same hazard `secrets edit` already guards against.
func SecretsEncrypt(ctx context.Context, c *cli.Command) error {
	env, encPath, err := secretsEnvPath(c)
	if err != nil {
		return err
	}
	plainPath := strings.TrimSuffix(encPath, ".yml") + ".plain.yml"
	keep := c.Bool("keep")
	slog.Debug("secrets encrypt: start", "env", env, "encrypted", encPath, "plain", plainPath, "keep", keep)

	if _, err := os.Stat(plainPath); err != nil {
		return errs.Usage(errs.Wrapf(err, "secrets encrypt: plain file %q not found", plainPath))
	}
	plainBytes, err := os.ReadFile(plainPath)
	if err != nil {
		return errs.System(errs.Wrapf(err, "secrets encrypt: read %s", plainPath))
	}
	parsed, err := parsePlainSecrets(plainBytes)
	if err != nil {
		return errs.Validation(errs.Wrapf(err, "secrets encrypt: %s isn't valid YAML", plainPath))
	}
	if err := validateSecretNamesMap(parsed); err != nil {
		return errs.Validation(errs.Wrapf(err, "secrets encrypt: invalid name(s) in %s", plainPath))
	}
	if err := sopsio.Encrypt(plainPath, encPath, sopsConfigPath(c.String("dir"))); err != nil {
		return errs.System(errs.Wrap(err, "secrets encrypt"))
	}
	if !keep {
		if err := os.Remove(plainPath); err != nil {
			slog.Warn("secrets encrypt: encrypted OK but failed to clean up plain file", "path", plainPath, "error", err)
		}
	}
	slog.Info("secrets encrypted", "env", env, "path", encPath)
	return nil
}

// ---------- verify ----------

// SecretsVerify implements `lazure secrets verify <env> [--check-kv]`.
func SecretsVerify(ctx context.Context, c *cli.Command) error {
	env, encPath, err := secretsEnvPath(c)
	if err != nil {
		return err
	}
	dir := c.String("dir")
	checkKV := c.Bool("check-kv")
	slog.Debug("secrets verify: start", "env", env, "dir", dir, "check_kv", checkKV)

	slog.Debug("secrets verify: loading manifest")
	manifest, vars, err := lazurecfg.LoadManifest(lazurecfg.LoadOptions{
		ProjectDir: dir,
		Env:        env,
	})
	if err != nil {
		return errs.Usage(errs.Wrap(err, "secrets verify: load manifest"))
	}
	slog.Debug("secrets verify: manifest loaded",
		"app", manifest.App.Name,
		"containers", len(manifest.Containers),
		"init_containers", len(manifest.InitContainers))

	slog.Debug("secrets verify: running structural validation")
	if r := lazurecfg.Validate(manifest); r.HasErrors() {
		return errs.Validation(errs.Wrap(r.Err(), "secrets verify"))
	}
	slog.Debug("secrets verify: validation passed")

	slog.Debug("secrets verify: decrypting SOPS", "path", encPath)
	decrypted, err := sopsio.Decrypt(encPath)
	if err != nil {
		return errs.Usage(errs.Wrap(err, "secrets verify: decrypt"))
	}
	slog.Debug("secrets verify: decrypted", "count", len(decrypted))

	var kv verify.KeyVault
	if checkKV {
		slog.Debug("secrets verify: creating Azure credential")
		tokens, err := azureapi.NewTokenProvider()
		if err != nil {
			return errs.Auth(errs.Wrap(err, "secrets verify: auth"))
		}
		vaultURL, _ := vars["keyvault_url"].(string)
		slog.Debug("secrets verify: constructed KV client", "vault", vaultURL)
		kv = azureapi.NewKeyVaultClient(vaultURL, tokens)
	}

	refs := lazurecfg.CollectSecretRefs(manifest)
	slog.Debug("secrets verify: checking references", "refs", len(refs))
	result := verify.Secrets(ctx, manifest, decrypted, kv)
	for _, w := range result.Warnings {
		slog.Warn(w)
	}
	if result.HasErrors() {
		return errs.Validation(result.Err())
	}

	slog.Info("secrets verified", "env", env, "refs", len(refs), "check_kv", checkKV)
	return nil
}

// ---------- sync ----------

// SecretsSync implements `lazure secrets sync <env> [--dry-run]
// [--concurrency=N] [-y]`. Upserts every SOPS-encrypted secret into
// Key Vault; never deletes. KV secrets not present in SOPS are left
// alone — delete them manually via `az keyvault secret delete` if
// that's truly what you want. Automated pruning was intentionally
// removed as too dangerous a default for a declarative-style tool.
func SecretsSync(ctx context.Context, c *cli.Command) error {
	env, encPath, err := secretsEnvPath(c)
	if err != nil {
		return err
	}
	dryRun := c.Bool("dry-run")
	concurrency := int(c.Int("concurrency"))
	if concurrency < 1 {
		concurrency = 1
	}
	yes := c.Bool("yes")
	slog.Debug("secrets sync: start", "env", env, "dry_run", dryRun, "concurrency", concurrency)

	slog.Debug("secrets sync: decrypting")
	secrets, err := sopsio.Decrypt(encPath)
	if err != nil {
		return errs.Usage(errs.Wrap(err, "secrets sync: decrypt"))
	}
	slog.Debug("secrets sync: decrypted", "count", len(secrets))

	vaultURL, err := sopsio.VaultURL(encPath)
	if err != nil {
		return errs.Usage(errs.Wrap(err, "secrets sync: vault URL"))
	}
	slog.Debug("secrets sync: creating Azure credential")
	tokens, err := azureapi.NewTokenProvider()
	if err != nil {
		return errs.Auth(errs.Wrap(err, "secrets sync: auth"))
	}
	slog.Debug("secrets sync: KV client ready", "vault", vaultURL)
	kv := azureapi.NewKeyVaultClient(vaultURL, tokens)

	if !yes {
		fmt.Printf("will sync %d secret(s) to %s [env=%s]\n", len(secrets), vaultURL, env)
		if dryRun {
			fmt.Println("(dry run — no writes will happen)")
		}
		if !promptConfirm("continue?") {
			return errs.Usage(errs.New("secrets sync: aborted by user"))
		}
	}

	if err := syncUpsert(ctx, kv, secrets, concurrency, dryRun); err != nil {
		return errs.System(errs.Wrap(err, "secrets sync: upsert"))
	}
	slog.Info("sync complete", "env", env, "secret", fmt.Sprintf("%d synced", len(secrets)))
	return nil
}

func syncUpsert(ctx context.Context, kv *azureapi.KeyVaultClient, secrets map[string]string, concurrency int, dryRun bool) error {
	// We deliberately do NOT use errgroup.WithContext here. That
	// variant cancels the shared context on the first error, which
	// then surfaces inside in-flight HTTP errors as the *cause* of
	// the cancellation — making every other secret's "✗" line appear
	// to fail with the first secret's error. We want each row to
	// report its own real error (or context.Canceled on Ctrl-C),
	// so siblings keep running on the parent ctx and we collect
	// failures separately.
	eg := new(errgroup.Group)
	eg.SetLimit(concurrency)
	var failed atomic.Int32
	for _, name := range sortedKeys(secrets) {
		name, value := name, secrets[name]
		eg.Go(func() error {
			if dryRun {
				fmt.Printf("would PUT %s\n", name)
				return nil
			}
			if err := kv.PutSecret(ctx, name, value); err != nil {
				fmt.Printf("✗ %s: %v\n", name, err)
				failed.Add(1)
				return nil
			}
			fmt.Printf("✓ %s\n", name)
			return nil
		})
	}
	_ = eg.Wait()
	if n := failed.Load(); n > 0 {
		return errs.Errorf("%d of %d secret(s) failed to upload", n, len(secrets))
	}
	return nil
}


// ---------- helpers ----------

// secretsEnvPath validates the positional env arg and returns the path
// to the encrypted secrets file for that env.
func secretsEnvPath(c *cli.Command) (string, string, error) {
	env := c.StringArg("env")
	if env == "" {
		return "", "", errs.Usage(errs.New("env argument is required (e.g. 'lazure secrets view dev')"))
	}
	dir := c.String("dir")
	return env, filepath.Join(dir, "envs", env+".secrets.yml"), nil
}

// promptConfirm prints a [y/N] prompt to stdout and reads stdin.
// Returns true only for "y" / "yes" (case-insensitive).
func promptConfirm(question string) bool {
	fmt.Printf("%s [y/N] ", question)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// sortedKeys returns a map's keys alphabetically sorted.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

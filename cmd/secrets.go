package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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
				Name:      "view",
				Usage:     "view secrets (redacted by default; --reveal to show full values)",
				Arguments: envArgs(),
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "reveal", Usage: "show full secret values (default: redacted)"},
					&cli.StringFlag{Name: "format", Usage: "output format: table|json", Value: "table"},
				},
				Action: SecretsView,
			},
			{
				Name:      "edit",
				Usage:     "decrypt, open in $EDITOR, re-encrypt",
				Arguments: envArgs(),
				Action:    SecretsEdit,
			},
			{
				Name:      "verify",
				Usage:     "check that every {secret: X} reference in the manifest exists in SOPS",
				Arguments: envArgs(),
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "check-kv", Usage: "also verify each referenced secret exists in Key Vault"},
				},
				Action: SecretsVerify,
			},
			{
				Name:      "sync",
				Usage:     "upload all SOPS secrets to Key Vault",
				Arguments: envArgs(),
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "prune", Usage: "ALSO delete KV secrets not present in SOPS (irreversible, always prompts)"},
					&cli.BoolFlag{Name: "dry-run", Usage: "print planned operations, do not call KV"},
					&cli.IntFlag{Name: "concurrency", Usage: "parallel HTTP calls", Value: 10},
					&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "skip the confirmation prompt for upsert (prune still prompts)"},
				},
				Action: SecretsSync,
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
	data, err := yaml.Marshal(out) // sigs.k8s.io/yaml emits JSON-compatible when asked
	if err != nil {
		return errs.System(errs.Wrap(err, "secrets view: marshal"))
	}
	// yaml.Marshal from sigs.k8s.io/yaml produces YAML; for JSON, use stdlib json.
	// Re-emit as proper JSON for --format=json consumers.
	jsonOut, err := yaml.YAMLToJSON(data)
	if err != nil {
		return errs.System(errs.Wrap(err, "secrets view: json convert"))
	}
	fmt.Println(string(jsonOut))
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

	// Open $EDITOR.
	editor := os.Getenv("EDITOR")
	if editor == "" {
		return errs.System(errs.New("$EDITOR not set; run e.g. 'export EDITOR=vim' and retry"))
	}
	slog.Info("opening editor", "editor", editor, "path", plainPath)
	edit := exec.CommandContext(ctx, editor, plainPath)
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
	slog.Debug("secrets edit: change detected, re-encrypting")

	if err := sopsio.Encrypt(plainPath, encPath); err != nil {
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
// decrypted map — alphabetical keys, double-quoted values — so diffs
// across edits are minimal.
func marshalPlainSecrets(secrets map[string]string) ([]byte, error) {
	keys := sortedKeys(secrets)
	ordered := map[string]string{}
	_ = ordered // sigs.k8s.io/yaml doesn't preserve insertion order
	// Emit manually for deterministic order + explicit quoting.
	var buf strings.Builder
	buf.WriteString("# Decrypted secrets — DO NOT COMMIT. Delete this file when done editing.\n")
	for _, k := range keys {
		// sigs.k8s.io/yaml.Marshal on a single string produces the
		// correctly-quoted form (handles newlines, special chars, etc.).
		quoted, err := yaml.Marshal(secrets[k])
		if err != nil {
			return nil, err
		}
		q := strings.TrimRight(string(quoted), "\n")
		buf.WriteString(k)
		buf.WriteString(": ")
		buf.WriteString(q)
		buf.WriteString("\n")
	}
	return []byte(buf.String()), nil
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

// SecretsSync implements `lazure secrets sync <env> [--prune] [--dry-run]
// [--concurrency=N] [-y]`.
func SecretsSync(ctx context.Context, c *cli.Command) error {
	env, encPath, err := secretsEnvPath(c)
	if err != nil {
		return err
	}
	prune := c.Bool("prune")
	dryRun := c.Bool("dry-run")
	concurrency := int(c.Int("concurrency"))
	if concurrency < 1 {
		concurrency = 1
	}
	yes := c.Bool("yes")
	slog.Debug("secrets sync: start", "env", env, "prune", prune, "dry_run", dryRun, "concurrency", concurrency)

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

	// Upsert confirmation (skipped with -y); prune ALWAYS confirms below.
	if !yes {
		fmt.Printf("will sync %d secret(s) to %s [env=%s]\n", len(secrets), vaultURL, env)
		if dryRun {
			fmt.Println("(dry run — no writes will happen)")
		}
		if !promptConfirm("continue?") {
			return errs.Usage(errs.New("secrets sync: aborted by user"))
		}
	}

	// Upsert in parallel.
	if err := syncUpsert(ctx, kv, secrets, concurrency, dryRun); err != nil {
		return errs.System(errs.Wrap(err, "secrets sync: upsert"))
	}

	if !prune {
		slog.Info("sync complete", "env", env, "secret", fmt.Sprintf("%d synced", len(secrets)))
		return nil
	}

	if err := syncPrune(ctx, kv, secrets, concurrency, dryRun); err != nil {
		return errs.System(errs.Wrap(err, "secrets sync: prune"))
	}
	slog.Info("sync + prune complete", "env", env)
	return nil
}

func syncUpsert(ctx context.Context, kv *azureapi.KeyVaultClient, secrets map[string]string, concurrency int, dryRun bool) error {
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(concurrency)
	for _, name := range sortedKeys(secrets) {
		name, value := name, secrets[name]
		eg.Go(func() error {
			if dryRun {
				fmt.Printf("would PUT %s\n", name)
				return nil
			}
			if err := kv.PutSecret(egCtx, name, value); err != nil {
				fmt.Printf("✗ %s: %v\n", name, err)
				return err
			}
			fmt.Printf("✓ %s\n", name)
			return nil
		})
	}
	return eg.Wait()
}

func syncPrune(ctx context.Context, kv *azureapi.KeyVaultClient, sopsSecrets map[string]string, concurrency int, dryRun bool) error {
	kvNames, err := kv.ListSecrets(ctx)
	if err != nil {
		return errs.Wrap(err, "list KV")
	}
	var toDelete []string
	for _, name := range kvNames {
		if _, inSOPS := sopsSecrets[name]; !inSOPS {
			toDelete = append(toDelete, name)
		}
	}
	sort.Strings(toDelete)

	if len(toDelete) == 0 {
		fmt.Println("no KV secrets to prune")
		return nil
	}

	// PRUNE ALWAYS CONFIRMS, even with -y. Design memo: irreversible
	// deletion must have an explicit separate confirmation.
	fmt.Printf("\nprune will DELETE %d secret(s) from Key Vault:\n", len(toDelete))
	for _, n := range toDelete {
		fmt.Printf("  - %s\n", n)
	}
	if !promptConfirm("proceed with deletion?") {
		return errs.New("prune aborted by user")
	}

	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(concurrency)
	for _, name := range toDelete {
		name := name
		eg.Go(func() error {
			if dryRun {
				fmt.Printf("would DELETE %s\n", name)
				return nil
			}
			if err := kv.DeleteSecret(egCtx, name); err != nil {
				fmt.Printf("✗ delete %s: %v\n", name, err)
				return err
			}
			fmt.Printf("✓ deleted %s\n", name)
			return nil
		})
	}
	return eg.Wait()
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

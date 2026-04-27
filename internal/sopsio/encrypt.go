package sopsio

import (
	"log/slog"
	"os"

	sops "github.com/getsops/sops/v3"
	"github.com/getsops/sops/v3/aes"
	"github.com/getsops/sops/v3/cmd/sops/common"
	"github.com/getsops/sops/v3/config"
	yamlstore "github.com/getsops/sops/v3/stores/yaml"
	sopsversion "github.com/getsops/sops/v3/version"

	"github.com/investerra/lazure/internal/errs"
)

// Encrypt encrypts plainPath into encryptedPath. Two modes, dispatched
// by whether encryptedPath already exists:
//
//   - Re-encrypt (encryptedPath exists): reuse master-key metadata
//     from the existing file. Same Azure Key Vault key encrypts old
//     and new — no diff noise. A fresh data key is generated each
//     call (correct SOPS behaviour).
//
//   - Bootstrap (encryptedPath missing): load configPath (.sops.yaml)
//     and use its first matching creation_rule as the master-key
//     source. This is the first-encryption path after `lazure init`.
//
// configPath is consulted only on bootstrap. The re-encrypt path
// ignores it (the existing file is the source of truth).
func Encrypt(plainPath, encryptedPath, configPath string) error {
	slog.Debug("sopsio: encrypt", "plain", plainPath, "encrypted", encryptedPath, "config", configPath)
	if _, err := os.Stat(encryptedPath); err == nil {
		return reencrypt(plainPath, encryptedPath)
	}
	return bootstrapEncrypt(plainPath, encryptedPath, configPath)
}

// reencrypt rewrites encryptedPath using its existing master-key
// metadata. Used in the secrets edit cycle and on every
// `lazure secrets encrypt <env>` after the first.
func reencrypt(plainPath, encryptedPath string) error {
	slog.Debug("sopsio: reencrypt path")
	store := &yamlstore.Store{}
	existingTree, err := common.LoadEncryptedFile(store, encryptedPath)
	if err != nil {
		return errs.Wrapf(err, "sopsio: load existing %s", encryptedPath)
	}

	plainBytes, err := os.ReadFile(plainPath)
	if err != nil {
		return errs.Wrapf(err, "sopsio: read plain %s", plainPath)
	}
	branches, err := store.LoadPlainFile(plainBytes)
	if err != nil {
		return errs.Wrapf(err, "sopsio: parse plain %s", plainPath)
	}

	newTree := sops.Tree{
		Branches: branches,
		Metadata: existingTree.Metadata,
		FilePath: existingTree.FilePath,
	}
	return finalizeAndWrite(store, &newTree, encryptedPath)
}

// bootstrapEncrypt produces the very first encrypted file for a
// project. Reads creation_rules from configPath and uses the first
// matching rule's KeyGroups to seed master-key metadata. Errors if
// configPath is missing or has no rule matching encryptedPath — the
// user has to wire up .sops.yaml before lazure can encrypt anything.
func bootstrapEncrypt(plainPath, encryptedPath, configPath string) error {
	slog.Debug("sopsio: bootstrap path", "config", configPath)
	if _, err := os.Stat(configPath); err != nil {
		return errs.Errorf(
			"sopsio: %s does not exist yet and %s was not found — create %s with creation_rules pointing to your Azure Key Vault key",
			encryptedPath, configPath, configPath)
	}
	cfg, err := config.LoadCreationRuleForFile(configPath, encryptedPath, nil)
	if err != nil {
		return errs.Wrapf(err, "sopsio: load creation rule for %s from %s", encryptedPath, configPath)
	}
	if cfg == nil || len(cfg.KeyGroups) == 0 {
		return errs.Errorf("sopsio: no matching creation_rule for %s in %s", encryptedPath, configPath)
	}

	plainBytes, err := os.ReadFile(plainPath)
	if err != nil {
		return errs.Wrapf(err, "sopsio: read plain %s", plainPath)
	}
	store := &yamlstore.Store{}
	branches, err := store.LoadPlainFile(plainBytes)
	if err != nil {
		return errs.Wrapf(err, "sopsio: parse plain %s", plainPath)
	}

	tree := sops.Tree{
		Branches: branches,
		Metadata: sops.Metadata{
			KeyGroups:         cfg.KeyGroups,
			ShamirThreshold:   cfg.ShamirThreshold,
			UnencryptedSuffix: cfg.UnencryptedSuffix,
			EncryptedSuffix:   cfg.EncryptedSuffix,
			UnencryptedRegex:  cfg.UnencryptedRegex,
			EncryptedRegex:    cfg.EncryptedRegex,
			MACOnlyEncrypted:  cfg.MACOnlyEncrypted,
			Version:           sopsversion.Version,
		},
		FilePath: encryptedPath,
	}
	return finalizeAndWrite(store, &tree, encryptedPath)
}

// finalizeAndWrite generates a data key, encrypts the tree, and
// writes the emitted bytes to encryptedPath. Shared between the
// reencrypt and bootstrap paths.
func finalizeAndWrite(store *yamlstore.Store, tree *sops.Tree, encryptedPath string) error {
	slog.Debug("sopsio: generating data key")
	dataKey, gen := tree.GenerateDataKey()
	if len(gen) > 0 {
		return errs.Wrapf(gen[0], "sopsio: generate data key for %s", encryptedPath)
	}
	if err := common.EncryptTree(common.EncryptTreeOpts{
		Tree:    tree,
		Cipher:  aes.NewCipher(),
		DataKey: dataKey,
	}); err != nil {
		return errs.Wrapf(err, "sopsio: encrypt tree for %s", encryptedPath)
	}
	out, err := store.EmitEncryptedFile(*tree)
	if err != nil {
		return errs.Wrapf(err, "sopsio: emit encrypted %s", encryptedPath)
	}
	if err := os.WriteFile(encryptedPath, out, 0o600); err != nil {
		return errs.Wrapf(err, "sopsio: write %s", encryptedPath)
	}
	slog.Debug("sopsio: encrypted file written", "path", encryptedPath, "bytes", len(out))
	return nil
}

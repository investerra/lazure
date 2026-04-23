package sopsio

import (
	"log/slog"
	"os"

	sops "github.com/getsops/sops/v3"
	"github.com/getsops/sops/v3/aes"
	"github.com/getsops/sops/v3/cmd/sops/common"
	yamlstore "github.com/getsops/sops/v3/stores/yaml"

	"github.com/investerra/lazure/internal/errs"
)

// Encrypt re-encrypts plainPath into encryptedPath while REUSING the
// master-key metadata from encryptedPath's prior content. This is the
// write-back half of the secrets edit flow:
//
//	sopsio.Decrypt(enc) → plain.yml → $EDITOR → sopsio.Encrypt(plain, enc)
//
// Reusing the existing SOPS metadata means the same Azure Key Vault key
// encrypts both the old and the new file — no diff noise in git, no
// extra audit events on KV. A fresh data key is generated each call
// (encrypted with the same master key) so the ENC[...] blobs themselves
// do change, which is correct SOPS behaviour.
func Encrypt(plainPath, encryptedPath string) error {
	slog.Debug("sopsio: re-encrypt", "plain", plainPath, "encrypted", encryptedPath)
	store := &yamlstore.Store{}

	slog.Debug("sopsio: loading existing encrypted metadata")
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

	slog.Debug("sopsio: generating fresh data key against existing master keys")
	dataKey, genErrs := newTree.GenerateDataKey()
	if len(genErrs) > 0 {
		return errs.Wrapf(genErrs[0], "sopsio: generate data key for %s", encryptedPath)
	}

	slog.Debug("sopsio: encrypting tree")
	if err := common.EncryptTree(common.EncryptTreeOpts{
		Tree:    &newTree,
		Cipher:  aes.NewCipher(),
		DataKey: dataKey,
	}); err != nil {
		return errs.Wrapf(err, "sopsio: encrypt tree for %s", encryptedPath)
	}

	out, err := store.EmitEncryptedFile(newTree)
	if err != nil {
		return errs.Wrapf(err, "sopsio: emit encrypted %s", encryptedPath)
	}
	if err := os.WriteFile(encryptedPath, out, 0o600); err != nil {
		return errs.Wrapf(err, "sopsio: write %s", encryptedPath)
	}
	slog.Debug("sopsio: encrypted file written", "path", encryptedPath, "bytes", len(out))
	return nil
}

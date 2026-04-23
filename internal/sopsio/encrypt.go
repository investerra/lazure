package sopsio

import (
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
	store := &yamlstore.Store{}

	// Load existing encrypted file ONLY for its metadata (KeyGroups,
	// shamir_threshold, unencrypted_suffix, etc.). The encrypted values
	// inside the branches are discarded below when we swap in the plain
	// branches.
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

	// GenerateDataKey creates a fresh data key and encrypts it with each
	// master key already in Metadata — our Azure KV key. Returns []error
	// because multi-key scenarios can have partial failures; we treat any
	// failure as fatal since we only have one key.
	dataKey, genErrs := newTree.GenerateDataKey()
	if len(genErrs) > 0 {
		return errs.Wrapf(genErrs[0], "sopsio: generate data key for %s", encryptedPath)
	}

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
	return nil
}

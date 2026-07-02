package registry

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BlobStore stores large secret values that exceed the OS keychain's per-item
// size limit. The macOS Keychain (and Windows Credential Manager) reject items
// past a few KB with "data passed to Set was too big"; Audible credentials (an
// RSA device key PEM + ADP/access/refresh tokens + cookies) are well over that.
//
// It uses envelope encryption: a fresh random 32-byte data key is kept in the
// keychain (tiny - always fits) and the value is AES-256-GCM sealed into a 0600
// file. The bulk lives on disk but is unreadable without the keychain key, so the
// secret material itself never leaves the keychain in plaintext.
type BlobStore struct {
	secrets Secrets
	dir     string
}

// NewBlobStore wires a blob store backed by secrets (for the data key) and dir
// (for the ciphertext files). dir is created lazily on first Set.
func NewBlobStore(secrets Secrets, dir string) *BlobStore {
	return &BlobStore{secrets: secrets, dir: dir}
}

func (b *BlobStore) keyName(name string) string { return "blobkey:" + name }

func (b *BlobStore) filePath(name string) string {
	safe := strings.NewReplacer("/", "_", ":", "_", "\\", "_", "..", "_").Replace(name)
	return filepath.Join(b.dir, safe+".enc")
}

// Set seals value into the on-disk file and stores its data key in the keychain.
func (b *BlobStore) Set(name, value string) error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(value), nil)
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(b.filePath(name), sealed, 0o600); err != nil {
		return err
	}
	// Store the data key last: if this fails, Get treats the orphaned file as absent.
	return b.secrets.Set(b.keyName(name), hex.EncodeToString(key))
}

// Get returns the decrypted value, or ("", nil) when no blob is stored (missing
// data key or missing file - both mean "not set yet").
func (b *BlobStore) Get(name string) (string, error) {
	keyHex, err := b.secrets.Get(b.keyName(name))
	if err != nil {
		return "", err
	}
	if keyHex == "" {
		return "", nil
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", fmt.Errorf("decode data key: %w", err)
	}
	sealed, err := os.ReadFile(b.filePath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", fmt.Errorf("corrupt credential file %q", b.filePath(name))
	}
	nonce, ct := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt credentials: %w", err)
	}
	return string(plain), nil
}

// Delete removes both the ciphertext file and the keychain data key.
func (b *BlobStore) Delete(name string) error {
	if err := os.Remove(b.filePath(name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return b.secrets.Delete(b.keyName(name))
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

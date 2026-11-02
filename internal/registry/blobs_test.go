package registry

import (
	"os"
	"strings"
	"testing"
)

func TestBlobStoreRoundTrip(t *testing.T) {
	sec := NewMemorySecrets()
	b := NewBlobStore(sec, t.TempDir())

	// A value far larger than the keychain per-item limit that broke direct Set.
	big := strings.Repeat("-----BEGIN KEY-----\nabc123\n", 500)
	if err := b.Set("audible:default", big); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := b.Get("audible:default")
	if err != nil || got != big {
		t.Fatalf("Get round-trip failed: err=%v len(got)=%d", err, len(got))
	}

	// The keychain holds only the small data key, never the plaintext blob.
	dataKey, _ := sec.Get("blobkey:audible:default")
	if dataKey == "" || strings.Contains(dataKey, "BEGIN KEY") || len(dataKey) > 128 {
		t.Fatalf("data key looks wrong: %q", dataKey)
	}
}

func TestBlobStoreAbsent(t *testing.T) {
	b := NewBlobStore(NewMemorySecrets(), t.TempDir())
	if v, err := b.Get("nope"); err != nil || v != "" {
		t.Fatalf("absent Get = %q, %v; want empty", v, err)
	}
}

func TestBlobStoreDelete(t *testing.T) {
	sec := NewMemorySecrets()
	dir := t.TempDir()
	b := NewBlobStore(sec, dir)
	if err := b.Set("audible:default", "secret-creds"); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete("audible:default"); err != nil {
		t.Fatal(err)
	}
	if v, _ := b.Get("audible:default"); v != "" {
		t.Fatalf("after Delete, Get = %q", v)
	}
	if _, err := os.Stat(b.filePath("audible:default")); !os.IsNotExist(err) {
		t.Fatalf("ciphertext file should be gone: %v", err)
	}
}

func TestBlobStoreTamperDetected(t *testing.T) {
	sec := NewMemorySecrets()
	b := NewBlobStore(sec, t.TempDir())
	if err := b.Set("k", "value"); err != nil {
		t.Fatal(err)
	}
	// Flip a byte in the ciphertext: GCM must refuse to decrypt.
	data, _ := os.ReadFile(b.filePath("k"))
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(b.filePath("k"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get("k"); err == nil {
		t.Fatal("expected a decryption error after tampering")
	}
}

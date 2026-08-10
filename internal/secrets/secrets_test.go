package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCipherRoundTripAndWrongKey(t *testing.T) {
	c, err := New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	value, err := c.Encrypt("telegram://bot:secret@example")
	if err != nil {
		t.Fatal(err)
	}
	if value == "telegram://bot:secret@example" || !IsEncrypted(value) {
		t.Fatalf("ciphertext=%q", value)
	}
	plain, err := c.Decrypt(value)
	if err != nil || plain != "telegram://bot:secret@example" {
		t.Fatalf("plain=%q err=%v", plain, err)
	}
	wrong, _ := New(bytes.Repeat([]byte{2}, 32))
	if _, err := wrong.Decrypt(value); err == nil {
		t.Fatal("wrong key unexpectedly decrypted ciphertext")
	}
}

func TestLoadOrCreateKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doupro.key")
	first, err := LoadOrCreate("", path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode=%o", info.Mode().Perm())
	}
	second, err := LoadOrCreate("", path)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := first.Encrypt("secret")
	if plain, err := second.Decrypt(value); err != nil || plain != "secret" {
		t.Fatalf("plain=%q err=%v", plain, err)
	}
}

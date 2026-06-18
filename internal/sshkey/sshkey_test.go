package sshkey

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInspectMissing(t *testing.T) {
	if got := Inspect(filepath.Join(t.TempDir(), "nope")); got != Missing {
		t.Errorf("missing key: got %v, want Missing", got)
	}
	if got := Inspect(""); got != Unknown {
		t.Errorf("empty path: got %v, want Unknown", got)
	}
}

// TestInspectGeneratedKeys generates real ed25519 keys with ssh-keygen and
// checks both the encrypted and unencrypted cases. Skips if ssh-keygen is
// absent (the parser itself is exercised regardless via the format tests).
func TestInspectGeneratedKeys(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	dir := t.TempDir()

	plain := filepath.Join(dir, "plain")
	if err := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", plain).Run(); err != nil {
		t.Fatalf("generate plain key: %v", err)
	}
	if got := Inspect(plain); got != OK {
		t.Errorf("passphrase-less key: got %v, want OK", got)
	}

	enc := filepath.Join(dir, "enc")
	if err := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "hunter2", "-f", enc).Run(); err != nil {
		t.Fatalf("generate encrypted key: %v", err)
	}
	if got := Inspect(enc); got != Encrypted {
		t.Errorf("passphrase-protected key: got %v, want Encrypted", got)
	}
}

func TestInspectLegacyEncryptedPEM(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "legacy")
	pem := "-----BEGIN RSA PRIVATE KEY-----\n" +
		"Proc-Type: 4,ENCRYPTED\n" +
		"DEK-Info: AES-128-CBC,0123456789ABCDEF\n\n" +
		"deadbeef\n-----END RSA PRIVATE KEY-----\n"
	if err := os.WriteFile(p, []byte(pem), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Inspect(p); got != Encrypted {
		t.Errorf("legacy encrypted PEM: got %v, want Encrypted", got)
	}
}

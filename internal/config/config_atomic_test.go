package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadConfigFailsWhenHashedManagementSecretCannotBePersisted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write permissions are not enforced equivalently on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\nremote-management:\n  secret-key: plaintext\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "persist hashed remote management key") {
		t.Fatalf("LoadConfig() error = %v, want hashed-secret persistence failure", err)
	}
	persisted, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if !strings.Contains(string(persisted), "secret-key: plaintext") {
		t.Fatalf("failed atomic publication modified source: %s", persisted)
	}
}

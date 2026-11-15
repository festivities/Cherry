package config

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// sessionSecretFileWith writes 32 bytes of secret to a new file with mode.
func sessionSecretFileWith(t *testing.T, mode os.FileMode) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "session.secret")
	if err := os.WriteFile(file, bytes.Repeat([]byte{7}, 32), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, mode); err != nil { // WriteFile keeps the mode of an existing file; a new one still gets the umask
		t.Fatal(err)
	}
	return file
}

// TestSessionSecretFileIsRequiredWhenPublic: a public server refuses to start without auth.session-secret-file, and the
// setting is empty by default (the lab keeps its stored keys).
func TestSessionSecretFileIsRequiredWhenPublic(t *testing.T) {
	c, err := Load(nil, none)
	if err != nil || c.SessionSecretFile != "" {
		t.Fatalf("default: secret file %q, err %v; want empty", c.SessionSecretFile, err)
	}
	_, err = Load(append(discordArgs(t, 0o600), "-server-public=true"), none)
	if err == nil || !strings.Contains(err.Error(), "server.public=true needs auth.session-secret-file") {
		t.Fatalf("public without the secret file: err = %v", err)
	}
}

// TestSessionSecretFileMode: off Windows a group- or world-readable secret file is refused; a 0600 one is read.
func TestSessionSecretFileMode(t *testing.T) {
	open := sessionSecretFileWith(t, 0o644)
	closed := sessionSecretFileWith(t, 0o600)
	if runtime.GOOS != "windows" {
		if _, err := Load([]string{"-auth-session-secret-file=" + open}, none); err == nil || !strings.Contains(err.Error(), "readable by group or others") {
			t.Fatalf("0644 secret file: err = %v, want a refusal", err)
		}
	}
	c, err := Load([]string{"-auth-session-secret-file=" + closed}, none)
	if err != nil || c.SessionSecretFile != closed {
		t.Fatalf("0600 secret file: %q, err %v", c.SessionSecretFile, err)
	}
}

// TestSessionSecretFileFromEnvironment: the environment name follows the table; a missing file is refused by name.
func TestSessionSecretFileFromEnvironment(t *testing.T) {
	file := sessionSecretFileWith(t, 0o600)
	c, err := Load(nil, env(map[string]string{"CHERRY_AUTH_SESSION_SECRET_FILE": file}))
	if err != nil || c.SessionSecretFile != file {
		t.Fatalf("environment: %q, err %v", c.SessionSecretFile, err)
	}
	_, err = Load(nil, env(map[string]string{"CHERRY_AUTH_SESSION_SECRET_FILE": filepath.Join(t.TempDir(), "absent")}))
	if err == nil || !strings.Contains(err.Error(), "auth.session-secret-file: cannot read the file") {
		t.Fatalf("missing file: err = %v", err)
	}
}

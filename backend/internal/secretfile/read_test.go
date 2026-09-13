package secretfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadRequiresPrivateRegularAbsoluteFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "secret")
	if err := os.WriteFile(path, []byte("value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, err := Read(path, 32)
	if err != nil || string(payload) != "value" {
		t.Fatalf("Read() = %q, %v", payload, err)
	}
	Zero(payload)
	for _, value := range payload {
		if value != 0 {
			t.Fatal("Zero did not clear payload")
		}
	}

	if _, err := Read("relative", 32); err == nil {
		t.Fatal("relative path was accepted")
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path, 32); err == nil {
		t.Fatal("group-readable file was accepted")
	}
}

func TestReadRejectsSymlinkAndOversize(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(link, 32); err == nil {
		t.Fatal("symlink was accepted")
	}
	if _, err := Read(target, 2); err == nil {
		t.Fatal("oversized file was accepted")
	}
}

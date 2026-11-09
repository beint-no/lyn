package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckPin(t *testing.T) {
	root := t.TempDir()
	site := filepath.Join(root, "sites", "a")
	os.MkdirAll(site, 0o755)
	if err := checkPin(site, "v1.0.0"); err != nil {
		t.Fatalf("no pin: %v", err)
	}
	os.WriteFile(filepath.Join(root, ".lyn-version"), []byte("v1.3.1\n"), 0o644)
	if err := checkPin(site, "v1.3.1"); err != nil {
		t.Fatalf("matching pin: %v", err)
	}
	if err := checkPin(site, "(devel)"); err != nil {
		t.Fatalf("development build: %v", err)
	}
	err := checkPin(site, "v1.1.0")
	if err == nil || !strings.Contains(err.Error(), "go install github.com/beint-no/lyn/cmd/lyn@v1.3.1") {
		t.Fatalf("mismatch: %v", err)
	}
}

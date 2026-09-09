package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := readToken(path)
	if err != nil {
		t.Fatalf("readToken() error = %v", err)
	}
	if value != "secret" {
		t.Fatalf("readToken() = %q, want %q", value, "secret")
	}
}

func TestReadTokenRejectsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readToken(path); err == nil {
		t.Fatal("readToken() error = nil, want non-nil")
	}
}

func TestReadTokenRejectsWhitespaceOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(" \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readToken(path); err == nil {
		t.Fatal("readToken() error = nil, want non-nil")
	}
}

package main

import (
	"strings"
	"testing"
)

// TestMainPackageCompiles is a compile-time smoke test. If the main package
// builds, this test passes. It verifies that all imports resolve and the
// package has no syntax errors — no Docker socket required.
func TestMainPackageCompiles(t *testing.T) {
	t.Log("main package compiled successfully")
}

func TestSanitizeDSN(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"postgres://u:secret@h/db?sslmode=require", "postgres://u:%2A%2A%2A@h/db?sslmode=require"},
		{"postgres://h/db?password=secret", "postgres://h/db?password=%2A%2A%2A"},
		{"postgres://h/db?sslmode=require&sslpassword=secret", "postgres://h/db?sslmode=require&sslpassword=%2A%2A%2A"},
		{"postgres://u:secret@h/db?password=secret", "postgres://u:%2A%2A%2A@h/db?password=%2A%2A%2A"},
		{"postgres://h/db?sslmode=require", "postgres://h/db?sslmode=require"},
		{"file:bollard.db", "file:bollard.db"},
		{"postgres://h/db", "postgres://h/db"},
	}
	for _, c := range cases {
		got := sanitizeDSN(c.in)
		if got != c.want {
			t.Errorf("sanitizeDSN(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.Contains(got, "secret") {
			t.Errorf("sanitizeDSN(%q) leaks secret: %q", c.in, got)
		}
	}
}

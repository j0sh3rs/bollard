package main

import (
	"testing"
)

// TestMainPackageCompiles is a compile-time smoke test. If the main package
// builds, this test passes. It verifies that all imports resolve and the
// package has no syntax errors — no Docker socket required.
func TestMainPackageCompiles(t *testing.T) {
	t.Log("main package compiled successfully")
}

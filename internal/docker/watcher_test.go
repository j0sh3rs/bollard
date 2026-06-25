package docker_test

import (
	"testing"

	"github.com/j0sh3rs/bollard/internal/docker"
)

func TestNewWatcher_ConnectsToSocket(t *testing.T) {
	w, err := docker.NewWatcher()
	if err != nil {
		t.Skipf("Docker socket unavailable: %v", err)
	}
	if w == nil {
		t.Fatal("expected non-nil watcher")
	}
	w.Close()
}

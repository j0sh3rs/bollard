package resolver_test

import (
	"net"
	"testing"

	"github.com/j0sh3rs/bollard/internal/resolver"
)

func TestHostIP_Override(t *testing.T) {
	ip, err := resolver.HostIP("10.0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ip != "10.0.0.1" {
		t.Errorf("expected 10.0.0.1, got %q", ip)
	}
}

func TestHostIP_EmptyOverride(t *testing.T) {
	ip, err := resolver.HostIP("")
	if err != nil {
		t.Skipf("inference unavailable in this environment: %v", err)
	}
	if ip == "" {
		t.Fatal("expected non-empty IP from inference")
	}
	if ip == "127.0.0.1" || ip == "::1" {
		t.Errorf("expected non-loopback IP, got %q", ip)
	}
}

// TestSelectCandidates verifies candidate list parsing with synthetic data.
// Exported via test file since it tests unexported selectCandidate helper.
func TestSelectCandidate_SingleAddress(t *testing.T) {
	candidates := []string{"192.168.1.1"}
	ip, err := resolver.SelectCandidate(candidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ip != "192.168.1.1" {
		t.Errorf("expected 192.168.1.1, got %q", ip)
	}
}

func TestSelectCandidate_NoAddresses(t *testing.T) {
	candidates := []string{}
	_, err := resolver.SelectCandidate(candidates)
	if err == nil {
		t.Fatal("expected error when no candidates")
	}
}

func TestSelectCandidate_MultipleAddresses(t *testing.T) {
	candidates := []string{"192.168.1.1", "10.0.0.1"}
	_, err := resolver.SelectCandidate(candidates)
	if err == nil {
		t.Fatal("expected error when multiple candidates")
	}
}

// Note: TestSelectHostIP would require mocking net.Interfaces() or
// exporting selectHostIP as a public function with dependency injection.
// For now, we rely on the integration test below to verify the logic.

func TestSelectHostIP_Integration(t *testing.T) {
	// Integration test: real interfaces, deterministic enough in CI
	// This verifies the full path works end-to-end
	ip, err := resolver.HostIP("")
	if err != nil {
		// OK to skip in isolated/containerized CI environments
		t.Skipf("skipping integration test: %v", err)
	}
	if ip == "" {
		t.Fatal("expected non-empty IP")
	}
	// Verify it parses as a valid IPv4
	if net.ParseIP(ip) == nil {
		t.Errorf("HostIP returned non-IP value: %q", ip)
	}
	if net.ParseIP(ip).To4() == nil {
		t.Errorf("HostIP returned non-IPv4 value: %q", ip)
	}
}

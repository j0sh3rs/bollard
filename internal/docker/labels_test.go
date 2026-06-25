package docker_test

import (
	"testing"

	"github.com/j0sh3rs/bollard/internal/docker"
)

func TestParseLabels_OptIn(t *testing.T) {
	labels := map[string]string{
		"dns.bollard/hostname": "myapp.home.arpa",
	}
	spec, err := docker.ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec == nil {
		t.Fatal("expected non-nil spec")
	}
	if spec.Hostname != "myapp.home.arpa" {
		t.Errorf("expected hostname myapp.home.arpa, got %q", spec.Hostname)
	}
	if spec.RecordType != "A" {
		t.Errorf("expected default record type A, got %q", spec.RecordType)
	}
	if spec.TTL != 300 {
		t.Errorf("expected default TTL 300, got %d", spec.TTL)
	}
	if !spec.Enabled {
		t.Error("expected enabled=true by default")
	}
}

func TestParseLabels_OptOut(t *testing.T) {
	labels := map[string]string{"unrelated": "value"}
	spec, err := docker.ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec != nil {
		t.Fatal("expected nil spec when hostname label absent")
	}
}

func TestParseLabels_Disabled(t *testing.T) {
	labels := map[string]string{
		"dns.bollard/hostname": "myapp.home.arpa",
		"dns.bollard/enabled":  "false",
	}
	spec, err := docker.ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Enabled {
		t.Error("expected enabled=false")
	}
}

func TestParseLabels_InvalidTTL(t *testing.T) {
	labels := map[string]string{
		"dns.bollard/hostname": "myapp.home.arpa",
		"dns.bollard/ttl":      "notanumber",
	}
	_, err := docker.ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for invalid TTL")
	}
}

func TestParseLabels_UnsupportedRecordType(t *testing.T) {
	labels := map[string]string{
		"dns.bollard/hostname":    "myapp.home.arpa",
		"dns.bollard/record-type": "CNAME",
	}
	_, err := docker.ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for unsupported record type")
	}
}

func TestParseLabels_InvalidIPOverride(t *testing.T) {
	labels := map[string]string{
		"dns.bollard/hostname":    "myapp.home.arpa",
		"dns.bollard/ip-override": "not-an-ip",
	}
	_, err := docker.ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for invalid IP override")
	}
}

func TestParseLabels_IPv6Override(t *testing.T) {
	labels := map[string]string{
		"dns.bollard/hostname":    "myapp.home.arpa",
		"dns.bollard/ip-override": "::1",
	}
	_, err := docker.ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for IPv6 override (MVP supports A only)")
	}
}

func TestParseLabels_ValidIPOverride(t *testing.T) {
	labels := map[string]string{
		"dns.bollard/hostname":    "myapp.home.arpa",
		"dns.bollard/ip-override": "192.168.1.1",
	}
	spec, err := docker.ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.IPOverride != "192.168.1.1" {
		t.Errorf("expected IPOverride 192.168.1.1, got %q", spec.IPOverride)
	}
}

func TestParseLabels_TTLZero(t *testing.T) {
	labels := map[string]string{
		"dns.bollard/hostname": "myapp.home.arpa",
		"dns.bollard/ttl":      "0",
	}
	spec, err := docker.ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.TTL != 0 {
		t.Errorf("expected TTL 0, got %d", spec.TTL)
	}
}

func TestParseLabels_EnabledVariants(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected bool
	}{
		{"true", "true", true},
		{"1", "1", true},
		{"yes", "yes", true},
		{"false", "false", false},
		{"0", "0", false},
		{"no", "no", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels := map[string]string{
				"dns.bollard/hostname": "myapp.home.arpa",
				"dns.bollard/enabled":  tt.value,
			}
			spec, err := docker.ParseLabels(labels)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if spec.Enabled != tt.expected {
				t.Errorf("expected enabled=%v, got %v", tt.expected, spec.Enabled)
			}
		})
	}
}

func TestParseLabels_EnabledInvalid(t *testing.T) {
	labels := map[string]string{
		"dns.bollard/hostname": "myapp.home.arpa",
		"dns.bollard/enabled":  "invalid",
	}
	_, err := docker.ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for invalid enabled value")
	}
}

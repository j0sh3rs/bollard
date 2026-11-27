package resolver

import (
	"net"
	"testing"
)

func TestCandidateFamilies(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		v6   bool
		want string
	}{
		{"192.168.1.5", false, "192.168.1.5"},
		{"192.168.1.5", true, ""},
		{"2001:db8::5", true, "2001:db8::5"},
		{"2001:db8::5", false, ""},
		{"fd00::5", true, "fd00::5"}, // unique-local addresses are routable inside the LAN
		{"fe80::1", true, ""},
		{"169.254.1.1", false, ""},
		{"::1", true, ""},
		{"127.0.0.1", false, ""},
	} {
		got, ok := candidate(net.ParseIP(tc.ip), tc.v6)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("candidate(%s, v6=%v) = %q, %v; want %q", tc.ip, tc.v6, got, ok, tc.want)
		}
	}
	if _, ok := candidate(nil, false); ok {
		t.Error("nil address accepted")
	}
}

func TestSelectCandidate6(t *testing.T) {
	if ip, err := SelectCandidate6([]string{"fd00::5"}); err != nil || ip != "fd00::5" {
		t.Fatalf("single candidate: %q, %v", ip, err)
	}
	if _, err := SelectCandidate6(nil); err == nil {
		t.Error("no candidates must fail")
	}
	if _, err := SelectCandidate6([]string{"fd00::5", "2001:db8::5"}); err == nil {
		t.Error("multiple candidates must fail")
	}
}

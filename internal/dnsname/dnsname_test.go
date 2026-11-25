package dnsname_test

import (
	"strings"
	"testing"

	"github.com/j0sh3rs/bollard/internal/dnsname"
)

func TestCanonicalAndValid(t *testing.T) {
	label := strings.Repeat("a", 63)
	prefix := label + "." + label + "." + label + "."
	for _, tc := range []struct {
		name      string
		input     string
		canonical string
		valid     bool
	}{
		{"mixed case and trailing dot", "My-App.Home.ARPA.", "my-app.home.arpa", true},
		{"single label", "X", "x", true},
		{"maximum length", prefix + strings.Repeat("b", 61), prefix + strings.Repeat("b", 61), true},
		{"too long", prefix + strings.Repeat("b", 62), prefix + strings.Repeat("b", 62), false},
		{"label too long", label + "a.home.arpa", label + "a.home.arpa", false},
		{"only one dot stripped", "name..", "name.", false},
		{"empty label", "name..arpa", "name..arpa", false},
		{"leading hyphen", "-name.arpa", "-name.arpa", false},
		{"trailing hyphen", "name-.arpa", "name-.arpa", false},
		{"underscore", "a_b.arpa", "a_b.arpa", false},
		{"whitespace", " name.arpa", " name.arpa", false},
		{"non-ASCII", "K.example", "K.example", false},
		{"IPv4", "192.0.2.1.", "192.0.2.1", false},
		{"IPv6", "2001:DB8::1", "2001:db8::1", false},
		{"root", ".", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := dnsname.Canonical(tc.input)
			if name != tc.canonical {
				t.Fatalf("Canonical(%q) = %q, want %q", tc.input, name, tc.canonical)
			}
			if got := dnsname.Valid(name); got != tc.valid {
				t.Errorf("Valid(%q) = %v, want %v", name, got, tc.valid)
			}
		})
	}
}

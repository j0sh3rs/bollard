package dnsname

import (
	"net"
	"strings"
)

// Canonical lowercases ASCII letters and removes one terminal dot.
func Canonical(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, strings.TrimSuffix(name, "."))
}

// Valid reports whether canonical is an ASCII DNS hostname, not an IP literal.
func Valid(canonical string) bool {
	if len(canonical) > 253 || net.ParseIP(canonical) != nil {
		return false
	}
	for {
		label, rest, more := strings.Cut(canonical, ".")
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return false
			}
		}
		if !more {
			return true
		}
		canonical = rest
	}
}

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

// HostAliases are names Docker Engine, Docker Desktop (current and legacy) and Podman resolve for
// containers to reach the host or the Docker VM, plus localhost. No label may claim them.
var HostAliases = []string{
	"host.docker.internal", "gateway.docker.internal", "kubernetes.docker.internal", "vm.docker.internal",
	"docker.internal", "host.containers.internal",
	"docker.for.mac.host.internal", "docker.for.mac.localhost", "docker.for.mac.http.internal",
	"docker.for.win.host.internal", "docker.for.win.localhost", "docker.for.win.http.internal",
	"localhost",
}

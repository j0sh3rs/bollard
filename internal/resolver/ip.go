package resolver

import (
	"fmt"
	"net"
	"strings"
)

// HostIP returns override if non-empty. Otherwise infers the host's
// primary non-loopback, non-Docker unicast IPv4 address. Returns an error if
// inference finds no suitable address or finds more than one routable
// candidate (use dns.bollard/ip-override in that case).
func HostIP(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return inferHostIP(false)
}

// HostIPv6 returns override if non-empty. Otherwise it infers the host's single global or
// unique-local IPv6 address on a non-virtual interface. Link-local and loopback addresses are never
// candidates, so a host with only fe80:: addresses yields an error rather than an unroutable record.
func HostIPv6(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return inferHostIP(true)
}

func inferHostIP(v6 bool) (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("resolver: list interfaces: %w", err)
	}
	candidates := selectHostIP(ifaces, v6)
	if v6 {
		return SelectCandidate6(candidates)
	}
	return SelectCandidate(candidates)
}

// isDockerInterface reports whether the interface name looks like a
// Docker-managed virtual interface. These are excluded from IP inference
// to avoid picking a bridge/VLAN IP instead of the physical NIC address.
func isDockerInterface(name string) bool {
	for _, prefix := range []string{"docker", "br-", "veth", "virbr", "flannel", "cni", "tunl", "weave"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// selectHostIP extracts addresses of one family from non-virtual interfaces.
// Errors from inaccessible interfaces are silently skipped.
func selectHostIP(ifaces []net.Interface, v6 bool) []string {
	var candidates []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isDockerInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if s, ok := candidate(ip, v6); ok {
				candidates = append(candidates, s)
			}
		}
	}
	return candidates
}

// candidate returns ip as a string when it is a usable address of the requested family.
func candidate(ip net.IP, v6 bool) (string, bool) {
	if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return "", false
	}
	if v6 {
		if ip.To4() == nil {
			return ip.String(), true
		}
		return "", false
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4.String(), true
	}
	return "", false
}

// SelectCandidate validates the candidate list and returns a single address or an error.
// This function is exported for testing purposes.
func SelectCandidate(candidates []string) (string, error) {
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("resolver: no routable IPv4 address found; set dns.bollard/ip-override")
	case 1:
		return candidates[0], nil
	default:
		return "", fmt.Errorf("resolver: multiple routable IPv4 addresses found (%v); set dns.bollard/ip-override explicitly", candidates)
	}
}

// SelectCandidate6 is SelectCandidate for IPv6 addresses.
func SelectCandidate6(candidates []string) (string, error) {
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("resolver: no routable IPv6 address found; set dns.bollard/ip-override")
	case 1:
		return candidates[0], nil
	default:
		return "", fmt.Errorf("resolver: multiple routable IPv6 addresses found (%v); set dns.bollard/ip-override explicitly", candidates)
	}
}

package resolver

import (
	"fmt"
	"net"
)

// HostIP returns override if non-empty. Otherwise infers the host's
// primary non-loopback unicast IPv4 address. Returns an error if
// inference finds no suitable address or finds more than one routable
// candidate (use dns.bollard/ip-override in that case).
func HostIP(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return inferHostIP()
}

func inferHostIP() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("resolver: list interfaces: %w", err)
	}
	candidates, err := selectHostIP(ifaces)
	if err != nil {
		return "", err
	}
	return SelectCandidate(candidates)
}

// selectHostIP extracts IPv4 addresses from interface list for testability.
// Errors from inaccessible interfaces are silently ignored to allow best-effort
// enumeration in environments where some interfaces may not be accessible.
func selectHostIP(ifaces []net.Interface) ([]string, error) {
	var candidates []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			// Silently skip interfaces that are inaccessible (e.g., in restricted namespaces)
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
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				candidates = append(candidates, ip4.String())
			}
		}
	}
	// Return warnings but don't fail — partial enumeration is acceptable.
	// This allows the function to work even if some interfaces are inaccessible.
	return candidates, nil
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

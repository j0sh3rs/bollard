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

// selectHostIP extracts IPv4 addresses from non-virtual interfaces.
// Errors from inaccessible interfaces are silently skipped.
func selectHostIP(ifaces []net.Interface) ([]string, error) {
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
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				candidates = append(candidates, ip4.String())
			}
		}
	}
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

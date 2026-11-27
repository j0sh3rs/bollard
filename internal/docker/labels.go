package docker

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/j0sh3rs/bollard/internal/dnsname"
)

const (
	labelHostname   = "dns.bollard/hostname"
	labelRecordType = "dns.bollard/record-type"
	labelTTL        = "dns.bollard/ttl"
	labelIPOverride = "dns.bollard/ip-override"
	labelTarget     = "dns.bollard/target"
	labelNetwork    = "dns.bollard/network"
	labelEnabled    = "dns.bollard/enabled"

	traefikEnable      = "traefik.enable"
	traefikRulePrefix  = "traefik.http.routers."
	traefikRuleSuffix  = ".rule"
	defaultRecordType  = "A"
	defaultTTL         = 300
	maxHostnamesPerBox = 32
)

// RecordSpec is the parsed, validated intent for one DNS record of a container.
type RecordSpec struct {
	Hostname   string
	RecordType string // "A", "AAAA" or "CNAME"
	TTL        int
	IPOverride string // A: IPv4, AAAA: IPv6; empty means infer
	Target     string // CNAME target hostname
	Network    string // Docker network whose address to publish; empty means infer
}

// ParseOptions tunes label parsing.
type ParseOptions struct {
	// InferTraefik derives hostnames from traefik.http.routers.*.rule labels when the container
	// has no dns.bollard/hostname label.
	InferTraefik bool
}

// ParseLabels returns the records a container asks for: one spec per hostname, sharing the
// record type, TTL, address and target labels. It returns nil, nil when the container is not
// opted in or is disabled, and an error when labels are present but invalid.
func ParseLabels(labels map[string]string, opts ParseOptions) ([]RecordSpec, error) {
	enabled := true
	if enabledStr, ok := labels[labelEnabled]; ok {
		switch enabledStr {
		case "true", "1", "yes":
		case "false", "0", "no":
			enabled = false
		default:
			return nil, fmt.Errorf("label %s: invalid value %q (use true/false)", labelEnabled, enabledStr)
		}
	}

	hostnames, err := parseHostnames(labels, opts)
	if err != nil {
		return nil, err
	}
	if len(hostnames) == 0 {
		return nil, nil
	}

	proto := RecordSpec{RecordType: defaultRecordType, TTL: defaultTTL}
	if rt, ok := labels[labelRecordType]; ok {
		switch rt {
		case "A", "AAAA", "CNAME":
			proto.RecordType = rt
		default:
			return nil, fmt.Errorf("label %s: unsupported record type %q (use A, AAAA or CNAME)", labelRecordType, rt)
		}
	}
	if ttlStr, ok := labels[labelTTL]; ok {
		ttl, err := strconv.Atoi(ttlStr)
		if err != nil {
			return nil, fmt.Errorf("label %s: invalid TTL %q: %w", labelTTL, ttlStr, err)
		}
		if ttl < 0 {
			return nil, fmt.Errorf("label %s: TTL must be non-negative, got %d", labelTTL, ttl)
		}
		proto.TTL = ttl
	}
	if err := parseAddressing(labels, &proto); err != nil {
		return nil, err
	}

	if !enabled {
		return nil, nil
	}
	specs := make([]RecordSpec, 0, len(hostnames))
	for _, h := range hostnames {
		if proto.RecordType == "CNAME" && h == proto.Target {
			return nil, fmt.Errorf("label %s: %q cannot point at itself", labelTarget, h)
		}
		spec := proto
		spec.Hostname = h
		specs = append(specs, spec)
	}
	return specs, nil
}

func parseHostnames(labels map[string]string, opts ParseOptions) ([]string, error) {
	raw, ok := labels[labelHostname]
	if !ok || raw == "" {
		if opts.InferTraefik {
			return traefikHostnames(labels), nil
		}
		return nil, nil
	}
	var hostnames []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		canonical := dnsname.Canonical(part)
		if !dnsname.Valid(canonical) {
			return nil, fmt.Errorf("label %s: invalid hostname %q", labelHostname, part)
		}
		if seen[canonical] {
			return nil, fmt.Errorf("label %s: duplicate hostname %q", labelHostname, canonical)
		}
		seen[canonical] = true
		hostnames = append(hostnames, canonical)
	}
	if len(hostnames) > maxHostnamesPerBox {
		return nil, fmt.Errorf("label %s: %d hostnames exceeds the limit of %d", labelHostname, len(hostnames), maxHostnamesPerBox)
	}
	return hostnames, nil
}

// traefikHostnames collects positive Host() names from the container's Traefik router rules.
// A container with traefik.enable=false is not routed, so it gets no records.
func traefikHostnames(labels map[string]string) []string {
	if labels[traefikEnable] == "false" {
		return nil
	}
	var keys []string
	for k := range labels {
		if strings.HasPrefix(k, traefikRulePrefix) && strings.HasSuffix(k, traefikRuleSuffix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var hostnames []string
	seen := map[string]bool{}
	for _, k := range keys {
		for _, h := range dnsname.RuleHosts(labels[k]) {
			if !seen[h] && len(hostnames) < maxHostnamesPerBox {
				seen[h] = true
				hostnames = append(hostnames, h)
			}
		}
	}
	return hostnames
}

// parseAddressing validates the labels that decide a record's value for its type.
func parseAddressing(labels map[string]string, spec *RecordSpec) error {
	ip, hasIP := labels[labelIPOverride]
	network := labels[labelNetwork]
	target, hasTarget := labels[labelTarget]

	if spec.RecordType == "CNAME" {
		if hasIP && ip != "" {
			return fmt.Errorf("label %s: not allowed on a CNAME record", labelIPOverride)
		}
		if network != "" {
			return fmt.Errorf("label %s: not allowed on a CNAME record", labelNetwork)
		}
		canonical := dnsname.Canonical(target)
		if !hasTarget || !dnsname.Valid(canonical) {
			return fmt.Errorf("label %s: a CNAME record needs a valid target hostname, got %q", labelTarget, target)
		}
		spec.Target = canonical
		return nil
	}

	if hasTarget {
		return fmt.Errorf("label %s: only allowed on a CNAME record", labelTarget)
	}
	if hasIP && ip != "" {
		if network != "" {
			return fmt.Errorf("labels %s and %s are mutually exclusive", labelIPOverride, labelNetwork)
		}
		parsed := net.ParseIP(ip)
		if parsed == nil {
			return fmt.Errorf("label %s: %q is not a valid IP address", labelIPOverride, ip)
		}
		switch spec.RecordType {
		case "A":
			if parsed.To4() == nil {
				return fmt.Errorf("label %s: %q is not a valid IPv4 address for an A record (use record-type AAAA for IPv6)", labelIPOverride, ip)
			}
		case "AAAA":
			if parsed.To4() != nil {
				return fmt.Errorf("label %s: %q is not a valid IPv6 address for an AAAA record", labelIPOverride, ip)
			}
			if parsed.IsLinkLocalUnicast() || parsed.IsLoopback() {
				return fmt.Errorf("label %s: %q is not routable (link-local or loopback)", labelIPOverride, ip)
			}
		}
		spec.IPOverride = ip
	}
	spec.Network = network
	return nil
}

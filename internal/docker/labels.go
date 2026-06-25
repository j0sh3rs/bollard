package docker

import (
	"fmt"
	"net"
	"strconv"
)

const (
	labelHostname   = "dns.bollard/hostname"
	labelRecordType = "dns.bollard/record-type"
	labelTTL        = "dns.bollard/ttl"
	labelIPOverride = "dns.bollard/ip-override"
	labelEnabled    = "dns.bollard/enabled"

	defaultRecordType = "A"
	defaultTTL        = 300
)

// RecordSpec is the parsed, validated intent from container labels.
type RecordSpec struct {
	Hostname   string
	RecordType string
	TTL        int
	IPOverride string
	Enabled    bool
}

// ParseLabels returns nil, nil if the container is not opted in.
// Returns an error if labels are present but invalid.
func ParseLabels(labels map[string]string) (*RecordSpec, error) {
	hostname, ok := labels[labelHostname]
	if !ok || hostname == "" {
		return nil, nil
	}
	spec := &RecordSpec{
		Hostname:   hostname,
		RecordType: defaultRecordType,
		TTL:        defaultTTL,
		Enabled:    true,
	}
	if rt, ok := labels[labelRecordType]; ok {
		if rt != "A" {
			return nil, fmt.Errorf("label %s: unsupported record type %q (MVP supports A only)", labelRecordType, rt)
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
		spec.TTL = ttl
	}
	if ip, ok := labels[labelIPOverride]; ok {
		if ip != "" {
			parsed := net.ParseIP(ip)
			if parsed == nil {
				return nil, fmt.Errorf("label %s: %q is not a valid IP address", labelIPOverride, ip)
			}
			if parsed.To4() == nil {
				return nil, fmt.Errorf("label %s: %q is not a valid IPv4 address (MVP supports A only)", labelIPOverride, ip)
			}
		}
		spec.IPOverride = ip
	}
	if enabledStr, ok := labels[labelEnabled]; ok {
		switch enabledStr {
		case "true", "1", "yes":
			spec.Enabled = true
		case "false", "0", "no":
			spec.Enabled = false
		default:
			return nil, fmt.Errorf("label %s: invalid value %q (use true/false)", labelEnabled, enabledStr)
		}
	}
	return spec, nil
}

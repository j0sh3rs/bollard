package unifi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
)

// DNSProvider is the interface bollard uses to manage UniFi DNS records.
type DNSProvider interface {
	ListRecords(ctx context.Context) ([]DNSRecord, error)
	CreateRecord(ctx context.Context, r DNSRecord) (string, error)
	DeleteRecord(ctx context.Context, unifiID string) error
}

var errModernUnsupported = errors.New("unifi: modern API unsupported")

type siteProbeError struct {
	status int
	err    error
}

func (e *siteProbeError) Error() string {
	return fmt.Sprintf("unifi: site probe failed (status=%d): %v", e.status, e.err)
}

func (e *siteProbeError) Unwrap() error { return e.err }

// IsRetryableStartupError reports transient HTTP or timeout failures during a controller probe.
func IsRetryableStartupError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var probe *siteProbeError
	if errors.As(err, &probe) {
		if probe.status >= 300 {
			return probe.status == http.StatusTooManyRequests || (probe.status >= 500 && probe.status < 600)
		}
		err = probe.err
	} else if errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.Temporary()
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return true
	}
	return false
}

// New probes the UniFi controller and returns the appropriate DNSProvider.
// Only an unsupported modern endpoint falls back to legacy static-DNS.
func New(ctx context.Context, cfg *Config) (DNSProvider, error) {
	cfg.setDefaults()
	t, err := newHTTPTransport(cfg)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteID(ctx, t, cfg)
	if err == nil {
		if cfg.ObserveProvider != nil {
			cfg.ObserveProvider("modern")
		}
		return &modernProvider{transport: t, cfg: cfg, siteID: siteID}, nil
	}
	if err != errModernUnsupported {
		t.CloseIdleConnections()
		return nil, err
	}
	if cfg.ObserveProvider != nil {
		cfg.ObserveProvider("legacy")
	}
	return &legacyProvider{transport: t, cfg: cfg}, nil
}

func resolveSiteID(ctx context.Context, t *httpTransport, cfg *Config) (string, error) {
	const pageLimit = 200
	offset := 0
	for {
		url := fmt.Sprintf("%s/proxy/network/integration/v1/sites?offset=%d&limit=%d", cfg.Host, offset, pageLimit)
		data, status, err := t.do(ctx, http.MethodGet, url, nil)
		if offset == 0 && (status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented) {
			return "", errModernUnsupported
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", fmt.Errorf("unifi: site probe failed (status=%d): %w", status, ctxErr)
			}
			return "", &siteProbeError{status: status, err: err}
		}
		var page struct {
			Count      int `json:"count"`
			TotalCount int `json:"totalCount"`
			Data       []struct {
				ID                string `json:"id"`
				Name              string `json:"name"`
				InternalReference string `json:"internalReference"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return "", fmt.Errorf("unifi: parse sites: %w", err)
		}
		if page.Count < 0 || page.TotalCount < 0 || (len(page.Data) == 0 && offset < page.TotalCount) {
			return "", fmt.Errorf("unifi: invalid sites pagination at offset %d", offset)
		}
		for _, site := range page.Data {
			if cfg.Site != "" && (site.Name == cfg.Site || site.ID == cfg.Site || site.InternalReference == cfg.Site) {
				return site.ID, nil
			}
		}
		offset += len(page.Data)
		if offset >= page.TotalCount {
			return "", fmt.Errorf("unifi: site %q not found", cfg.Site)
		}
	}
}

type modernProvider struct {
	transport *httpTransport
	cfg       *Config
	siteID    string
}

func (p *modernProvider) CloseIdleConnections() {
	p.transport.CloseIdleConnections()
}

func (p *modernProvider) policiesURL() string {
	return fmt.Sprintf("%s/proxy/network/integration/v1/sites/%s/dns/policies", p.cfg.Host, p.siteID)
}

func (p *modernProvider) ListRecords(ctx context.Context) ([]DNSRecord, error) {
	const pageLimit = 200
	var all []DNSRecord
	offset := 0
	for {
		url := fmt.Sprintf("%s?offset=%d&limit=%d", p.policiesURL(), offset, pageLimit)
		data, _, err := p.transport.do(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("unifi: list records: %w", err)
		}
		var page dnsPolicyPage
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("unifi: parse records: %w", err)
		}
		if page.Count < 0 || page.TotalCount < 0 || (len(page.Data) == 0 && offset < page.TotalCount) {
			return nil, fmt.Errorf("unifi: invalid records pagination at offset %d", offset)
		}
		for _, env := range page.Data {
			if !p.cfg.IncludeAllRecords && (!env.Enabled || env.Type != policyTypeA) {
				continue
			}
			ttl := 0
			if env.TTLSeconds != nil {
				ttl = *env.TTLSeconds
			}
			recordType := env.Type
			if env.Type == policyTypeA {
				recordType = "A"
			}
			all = append(all, DNSRecord{
				ID: env.ID, Hostname: env.Domain, IP: env.IPv4Address,
				RecordType: recordType, TTL: ttl, Disabled: !env.Enabled,
			})
		}
		offset += len(page.Data)
		if offset >= page.TotalCount {
			break
		}
	}
	return all, nil
}

func (p *modernProvider) CreateRecord(ctx context.Context, r DNSRecord) (string, error) {
	ttl := r.TTL
	env := dnsPolicyEnvelope{
		Type: policyTypeA, Enabled: true,
		Domain: r.Hostname, IPv4Address: r.IP, TTLSeconds: &ttl,
	}
	body, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("unifi: marshal record: %w", err)
	}
	data, _, err := p.transport.do(ctx, http.MethodPost, p.policiesURL(), body)
	if err != nil {
		return "", fmt.Errorf("unifi: create record: %w", err)
	}
	var created dnsPolicyEnvelope
	if err := json.Unmarshal(data, &created); err != nil {
		return "", fmt.Errorf("unifi: parse create response: %w", err)
	}
	if created.ID == "" {
		return "", errors.New("unifi: create returned no record ID")
	}
	return created.ID, nil
}

func (p *modernProvider) DeleteRecord(ctx context.Context, unifiID string) error {
	if unifiID == "" {
		return errors.New("unifi: record ID required for delete")
	}
	url := fmt.Sprintf("%s/%s", p.policiesURL(), unifiID)
	_, status, err := p.transport.do(ctx, http.MethodDelete, url, nil)
	if err != nil && status != http.StatusNotFound {
		return fmt.Errorf("unifi: delete record %s: %w", unifiID, err)
	}
	return nil
}

type legacyProvider struct {
	transport *httpTransport
	cfg       *Config
}

func (p *legacyProvider) CloseIdleConnections() {
	p.transport.CloseIdleConnections()
}

func (p *legacyProvider) baseURL() string {
	return fmt.Sprintf("%s/proxy/network/v2/api/site/%s/static-dns", p.cfg.Host, p.cfg.Site)
}

func (p *legacyProvider) ListRecords(ctx context.Context) ([]DNSRecord, error) {
	data, _, err := p.transport.do(ctx, http.MethodGet, p.baseURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("unifi: legacy list: %w", err)
	}
	// Legacy endpoint returns a raw JSON array, not an envelope.
	var entries []staticDNSEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("unifi: parse legacy records: %w", err)
	}
	var records []DNSRecord
	for _, e := range entries {
		if !p.cfg.IncludeAllRecords && (!e.Enabled || e.RecordType != "A") {
			continue
		}
		records = append(records, DNSRecord{
			ID: e.ID, Hostname: e.Key, IP: e.Value, RecordType: e.RecordType, TTL: e.TTL,
			Disabled: !e.Enabled,
		})
	}
	return records, nil
}

func (p *legacyProvider) CreateRecord(ctx context.Context, r DNSRecord) (string, error) {
	body, err := json.Marshal(staticDNSEntry{
		Key: r.Hostname, Value: r.IP, RecordType: "A", TTL: r.TTL, Enabled: true,
	})
	if err != nil {
		return "", fmt.Errorf("unifi: marshal legacy record: %w", err)
	}
	data, _, err := p.transport.do(ctx, http.MethodPost, p.baseURL(), body)
	if err != nil {
		return "", fmt.Errorf("unifi: legacy create: %w", err)
	}
	// Legacy endpoint returns the created entry as a single JSON object.
	var created staticDNSEntry
	if err := json.Unmarshal(data, &created); err != nil {
		return "", fmt.Errorf("unifi: parse legacy create response: %w", err)
	}
	if created.ID == "" {
		return "", fmt.Errorf("unifi: legacy create returned no id")
	}
	return created.ID, nil
}

func (p *legacyProvider) DeleteRecord(ctx context.Context, unifiID string) error {
	if unifiID == "" {
		return errors.New("unifi: record ID required for delete")
	}
	url := fmt.Sprintf("%s/%s", p.baseURL(), unifiID)
	_, status, err := p.transport.do(ctx, http.MethodDelete, url, nil)
	if err != nil && status != http.StatusNotFound {
		return fmt.Errorf("unifi: legacy delete %s: %w", unifiID, err)
	}
	return nil
}

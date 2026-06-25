package unifi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// DNSProvider is the interface bollard uses to manage UniFi DNS records.
type DNSProvider interface {
	ListRecords(ctx context.Context) ([]DNSRecord, error)
	CreateRecord(ctx context.Context, r DNSRecord) (string, error)
	DeleteRecord(ctx context.Context, unifiID string) error
}

// New probes the UniFi controller and returns the appropriate DNSProvider.
// Tries modern Integration API first; falls back to legacy static-DNS.
func New(cfg *Config) (DNSProvider, error) {
	cfg.setDefaults()
	t, err := newHTTPTransport(cfg)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteID(context.Background(), t, cfg)
	if err == nil {
		return &modernProvider{transport: t, cfg: cfg, siteID: siteID}, nil
	}
	return &legacyProvider{transport: t, cfg: cfg}, nil
}

func resolveSiteID(ctx context.Context, t *httpTransport, cfg *Config) (string, error) {
	url := fmt.Sprintf("%s/proxy/network/integration/v1/sites", cfg.Host)
	data, status, err := t.do(ctx, http.MethodGet, url, nil)
	if err != nil || status >= 400 {
		return "", fmt.Errorf("unifi: site probe failed (status=%d): %w", status, err)
	}
	var resp struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("unifi: parse sites: %w", err)
	}
	for _, site := range resp.Data {
		if site.Name == cfg.Site || site.ID == cfg.Site {
			return site.ID, nil
		}
	}
	return "", fmt.Errorf("unifi: site %q not found", cfg.Site)
}

type modernProvider struct {
	transport *httpTransport
	cfg       *Config
	siteID    string
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
		var page apiPage[dnsPolicyEnvelope]
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("unifi: parse records: %w", err)
		}
		for _, env := range page.Data {
			if !env.Enabled || env.Type != policyTypeA {
				continue
			}
			ttl := 0
			if env.TTLSeconds != nil {
				ttl = *env.TTLSeconds
			}
			all = append(all, DNSRecord{
				ID: env.ID, Hostname: env.Domain, IP: env.IPv4Address,
				RecordType: "A", TTL: ttl,
			})
		}
		if offset+page.Count >= page.TotalCount {
			break
		}
		offset += page.Count
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
	return created.ID, nil
}

func (p *modernProvider) DeleteRecord(ctx context.Context, unifiID string) error {
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

func (p *legacyProvider) baseURL() string {
	return fmt.Sprintf("%s/proxy/network/v2/api/site/%s/static-dns", p.cfg.Host, p.cfg.Site)
}

func (p *legacyProvider) ListRecords(ctx context.Context) ([]DNSRecord, error) {
	data, _, err := p.transport.do(ctx, http.MethodGet, p.baseURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("unifi: legacy list: %w", err)
	}
	var resp staticDNSResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("unifi: parse legacy records: %w", err)
	}
	var records []DNSRecord
	for _, e := range resp.Data {
		if !e.Enabled || e.RecordType != "A" {
			continue
		}
		records = append(records, DNSRecord{
			ID: e.ID, Hostname: e.Key, IP: e.Value, RecordType: "A", TTL: e.TTL,
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
	var resp staticDNSResponse
	if err := json.Unmarshal(data, &resp); err != nil || len(resp.Data) == 0 {
		return "", fmt.Errorf("unifi: parse legacy create response")
	}
	return resp.Data[0].ID, nil
}

func (p *legacyProvider) DeleteRecord(ctx context.Context, unifiID string) error {
	url := fmt.Sprintf("%s/%s", p.baseURL(), unifiID)
	_, status, err := p.transport.do(ctx, http.MethodDelete, url, nil)
	if err != nil && status != http.StatusNotFound {
		return fmt.Errorf("unifi: legacy delete %s: %w", unifiID, err)
	}
	return nil
}

// Package traefik registers HTTP router hostnames in UniFi DNS as a Traefik provider plugin.
package traefik

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/j0sh3rs/bollard/internal/unifi"
)

// Config holds the provider's static Traefik configuration.
type Config struct {
	TraefikAPI         string `json:"traefikAPI,omitempty"`
	UnifiHost          string `json:"unifiHost,omitempty"`
	UnifiAPIKey        string `json:"unifiAPIKey,omitempty"`
	UnifiAPIKeyFile    string `json:"unifiAPIKeyFile,omitempty"`
	UnifiSite          string `json:"unifiSite,omitempty"`
	UnifiCACert        string `json:"unifiCACert,omitempty"`
	UnifiSkipTLSVerify bool   `json:"unifiSkipTLSVerify,omitempty"`
	Address            string `json:"address,omitempty"`
	PollInterval       string `json:"pollInterval,omitempty"`
}

// CreateConfig supplies defaults without credentials or network side effects.
func CreateConfig() *Config {
	return &Config{
		TraefikAPI:   "http://127.0.0.1:8080",
		UnifiSite:    "default",
		PollInterval: "30s",
	}
}

// Provider discovers routers independently of incoming HTTP requests.
type Provider struct {
	ctx         context.Context
	api         string
	address     string
	interval    time.Duration
	unifiConfig *unifi.Config
	dns         unifi.DNSProvider
	client      *http.Client
	logger      *log.Logger
	mu          sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
}

// New validates configuration. Network access starts only in Provide.
func New(ctx context.Context, config *Config, name string) (*Provider, error) {
	if config == nil {
		return nil, fmt.Errorf("bollard: configuration is required")
	}
	api, err := baseURL(config.TraefikAPI)
	if err != nil {
		return nil, fmt.Errorf("bollard: invalid traefikAPI: %w", err)
	}
	host, err := baseURL(config.UnifiHost)
	if err != nil {
		return nil, fmt.Errorf("bollard: invalid unifiHost: %w", err)
	}
	ip := net.ParseIP(config.Address)
	if ip == nil || ip.To4() == nil || strings.Contains(config.Address, ":") {
		return nil, fmt.Errorf("bollard: address must be an IPv4 address")
	}
	interval, err := time.ParseDuration(config.PollInterval)
	if err != nil || interval <= 0 {
		return nil, fmt.Errorf("bollard: pollInterval must be a positive duration")
	}
	site := config.UnifiSite
	if site == "" {
		site = "default"
	}
	if site == "." || site == ".." || strings.ContainsAny(site, "/\\?#%") {
		return nil, fmt.Errorf("bollard: unifiSite must be a site name or ID")
	}
	key := config.UnifiAPIKey
	if config.UnifiAPIKeyFile != "" {
		if key != "" {
			return nil, fmt.Errorf("bollard: set only one of unifiAPIKey and unifiAPIKeyFile")
		}
		data, err := os.ReadFile(config.UnifiAPIKeyFile)
		if err != nil {
			return nil, fmt.Errorf("bollard: read unifiAPIKeyFile: %w", err)
		}
		key = strings.TrimSpace(string(data))
	}
	if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") {
		return nil, fmt.Errorf("bollard: a nonempty UniFi API key is required")
	}
	return &Provider{
		ctx: ctx, api: api, address: ip.To4().String(), interval: interval,
		unifiConfig: &unifi.Config{
			Host: host, APIKey: key, Site: site,
			CACertPath: config.UnifiCACert, SkipTLSVerify: config.UnifiSkipTLSVerify,
			IncludeAllRecords: true,
		},
		client: &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		logger: log.New(os.Stderr, "bollard["+name+"]: ", log.LstdFlags),
	}, nil
}

func baseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("expected HTTP(S) URL without credentials, query, or fragment")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// Init satisfies the Traefik provider contract; New already validates configuration.
func (p *Provider) Init() error { return nil }

// Provide starts discovery without modifying Traefik's existing routing configuration.
func (p *Provider) Provide(configuration chan<- json.Marshaler) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		return fmt.Errorf("bollard: provider already started")
	}
	ctx, cancel := context.WithCancel(p.ctx)
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		select {
		case configuration <- json.RawMessage(`{}`):
		case <-ctx.Done():
			return
		}
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			if err := p.reconcile(ctx); err != nil && ctx.Err() == nil {
				p.logError("DNS discovery failed", err)
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	return nil
}

// Stop cancels in-flight requests and waits for the worker. DNS records remain intact.
func (p *Provider) Stop() error {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	p.client.CloseIdleConnections()
	return nil
}

func (p *Provider) reconcile(ctx context.Context) error {
	hosts, err := p.discover(ctx)
	if err != nil || len(hosts) == 0 {
		return err
	}
	if p.dns == nil {
		p.dns, err = unifi.New(ctx, p.unifiConfig)
		if err != nil {
			return fmt.Errorf("initialize UniFi: %w", err)
		}
	}
	records, err := p.dns.ListRecords(ctx)
	if err != nil {
		return fmt.Errorf("list UniFi records: %w", err)
	}
	for _, record := range records {
		host := strings.ToLower(strings.TrimSuffix(record.Hostname, "."))
		if hosts[host] && record.IP != p.address {
			p.logger.Printf("DNS conflict for %s: existing record preserved", host)
		}
		delete(hosts, host)
	}
	ordered := make([]string, 0, len(hosts))
	for host := range hosts {
		ordered = append(ordered, host)
	}
	sort.Strings(ordered)
	for _, host := range ordered {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, err := p.dns.CreateRecord(ctx, unifi.DNSRecord{Hostname: host, IP: p.address, RecordType: "A", TTL: 300})
		if err != nil {
			p.logError("create DNS record for "+host, err)
			continue
		}
		p.logger.Printf("created A record %s -> %s", host, p.address)
	}
	return nil
}

func (p *Provider) logError(operation string, err error) {
	p.logger.Printf("%s: %s", operation, strings.ReplaceAll(err.Error(), p.unifiConfig.APIKey, "[redacted]"))
}

type router struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
	Rule     string `json:"rule"`
}

func (p *Provider) discover(ctx context.Context) (map[string]bool, error) {
	hosts := make(map[string]bool)
	page := 1
	for {
		endpoint := fmt.Sprintf("%s/api/http/routers?page=%d&per_page=100", p.api, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("create Traefik API request: %w", err)
		}
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("read Traefik API: %w", err)
		}
		const maxBody = 4 * 1024 * 1024
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read Traefik API body: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Traefik API returned HTTP %d", resp.StatusCode)
		}
		if len(body) > maxBody {
			return nil, fmt.Errorf("Traefik API response exceeds 4 MiB")
		}
		var routers []router
		if err := json.Unmarshal(body, &routers); err != nil {
			return nil, fmt.Errorf("decode Traefik routers: %w", err)
		}
		for _, router := range routers {
			if router.Status != "enabled" || router.Provider == "internal" || strings.HasSuffix(router.Name, "@internal") {
				continue
			}
			for _, host := range ruleHosts(router.Rule) {
				hosts[host] = true
			}
		}
		next := resp.Header.Get("X-Next-Page")
		if next == "" || next == "0" || next == "1" {
			return hosts, nil
		}
		nextPage, err := strconv.Atoi(next)
		if err != nil || nextPage <= page || len(routers) == 0 {
			return nil, fmt.Errorf("invalid Traefik API pagination")
		}
		page = nextPage
	}
}

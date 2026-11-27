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

	"github.com/j0sh3rs/bollard/internal/dnsname"
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

// Stop must finish inside Traefik's 10s server-close budget: an in-flight create (createTimeout)
// is drained first, then cleanup (cleanupTimeout) runs.
const (
	// shortcut: a create slower than this loses its record ID and the record is orphaned; raise only if Traefik's close budget grows.
	createTimeout  = 4 * time.Second
	cleanupTimeout = 5 * time.Second
)

// Provider discovers routers independently of incoming HTTP requests.
type Provider struct {
	ctx            context.Context
	api            string
	address        string
	interval       time.Duration
	controllerHost string
	unifiConfig    *unifi.Config
	dns            unifi.DNSProvider
	client         *http.Client
	logger         *log.Logger
	mu             sync.Mutex
	cancel         context.CancelFunc
	done           chan struct{}
	created        map[string]string // UniFi record ID -> hostname, for records this instance created
	stopOnce       sync.Once
	reserved       map[string]bool // names the plugin must never create: its own control-plane hosts
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
		ctx: ctx, api: strings.TrimRight(api.String(), "/"), address: ip.To4().String(), interval: interval,
		controllerHost: controllerHost(host.Hostname()),
		reserved:       reservedNames(host.Hostname(), api.Hostname()),
		unifiConfig: &unifi.Config{
			Host: strings.TrimRight(host.String(), "/"), APIKey: key, Site: site,
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

func baseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("expected HTTP(S) URL without credentials, query, or fragment")
	}
	return u, nil
}

func controllerHost(host string) string {
	host = dnsname.Canonical(host)
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
}

// reservedNames lists names a router rule must not make the plugin create: the UniFi controller,
// the Traefik API host, and the Docker host aliases, matching the daemon's guard.
func reservedNames(hosts ...string) map[string]bool {
	out := make(map[string]bool, len(hosts)+len(dnsname.HostAliases))
	for _, h := range hosts {
		out[controllerHost(h)] = true
	}
	for _, h := range dnsname.HostAliases {
		out[h] = true
	}
	return out
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

// Stop cancels in-flight requests and waits for the worker. It then deletes the DNS records this
// instance created, so they are not orphaned, and releases idle UniFi connections. Records from
// earlier runs or other writers are never touched.
func (p *Provider) Stop() error {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	p.stopOnce.Do(p.cleanup) // concurrent Stop calls wait here rather than racing on p.created
	p.client.CloseIdleConnections()
	if closer, ok := p.dns.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
	return nil
}

// cleanup deletes every record created by this instance. A failure is logged and does not stop the rest.
// It runs after the worker exits, so created needs no lock. Traefik may cancel p.ctx first, so it uses its own deadline.
func (p *Provider) cleanup() {
	if len(p.created) == 0 {
		return
	}
	deadline := time.Now().Add(cleanupTimeout)
	ids := make([]string, 0, len(p.created))
	for id := range p.created {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for i, id := range ids {
		host := p.created[id]
		// Fair share of the remaining budget, so one hung DELETE cannot starve the rest.
		ctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline)/time.Duration(len(ids)-i))
		err := p.dns.DeleteRecord(ctx, id)
		cancel()
		if err != nil {
			p.logError("delete DNS record for "+host, err)
			continue
		}
		delete(p.created, id)
		p.logger.Printf("deleted A record %s on shutdown", host)
	}
}

type controllerGate struct {
	slot chan struct{}
	refs int // holders and waiters; no idle entries remain
}

var (
	controllerMu    sync.Mutex
	controllerGates = make(map[string]*controllerGate)
)

func lockController(ctx context.Context, host string) (*controllerGate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	controllerMu.Lock()
	gate := controllerGates[host]
	if gate == nil {
		gate = &controllerGate{slot: make(chan struct{}, 1)}
		controllerGates[host] = gate
	}
	gate.refs++
	controllerMu.Unlock()

	select {
	case gate.slot <- struct{}{}:
		if ctx.Err() == nil {
			return gate, nil
		}
		<-gate.slot
	case <-ctx.Done():
	}
	releaseController(host, gate)
	return nil, ctx.Err()
}

func releaseController(host string, gate *controllerGate) {
	controllerMu.Lock()
	gate.refs--
	if gate.refs == 0 {
		delete(controllerGates, host)
	}
	controllerMu.Unlock()
}

func unlockController(host string, gate *controllerGate) {
	<-gate.slot
	releaseController(host, gate)
}

func (p *Provider) reconcile(ctx context.Context) error {
	hosts, err := p.discover(ctx)
	if err != nil || len(hosts) == 0 {
		return err
	}
	for host := range hosts {
		if p.reserved[host] {
			p.logger.Printf("hostname %s is reserved, not creating it", host)
			delete(hosts, host)
		}
	}
	if len(hosts) == 0 {
		return nil
	}

	// Different site names can resolve to the same site, so serialize by controller.
	gate, err := lockController(ctx, p.controllerHost)
	if err != nil {
		return err
	}
	defer unlockController(p.controllerHost, gate)

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
		host := dnsname.Canonical(record.Hostname)
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
	recoveryOK := true
	for _, host := range ordered {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Shutdown cancels ctx; a POST already sent may still commit in UniFi. Let it finish, bounded,
		// so Stop learns the record ID and can delete it.
		createCtx, cancelCreate := context.WithTimeout(context.Background(), createTimeout)
		id, err := p.dns.CreateRecord(createCtx, unifi.DNSRecord{Hostname: host, IP: p.address, RecordType: "A", TTL: 300})
		cancelCreate()
		if err != nil {
			p.logError("create DNS record for "+host, err)
			// The POST may have committed before the error (timeout, lost response, no ID returned).
			// Look for it so Stop can still delete it. One failed lookup disables further lookups this tick.
			if recoveryOK && !unifi.IsRejection(err) {
				var lerr error
				if id, lerr = p.findCreated(host); lerr != nil {
					recoveryOK = false
				}
			}
			if id == "" {
				continue
			}
			p.logger.Printf("recovered A record %s after a failed create", host)
		}
		if p.created == nil {
			p.created = make(map[string]string)
		}
		p.created[id] = host
		p.logger.Printf("created A record %s -> %s", host, p.address)
	}
	return nil
}

// findCreated returns the ID of the single A record for host at this instance's address, or "" when
// there is none or the match is ambiguous. reconcile only calls it for a host that was absent from the
// zone before the create, so a match is the record the failed create committed (or a concurrent writer
// that chose the same name and address, whose record Stop would then also delete).
func (p *Provider) findCreated(host string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), createTimeout)
	defer cancel()
	records, err := p.dns.ListRecords(ctx)
	if err != nil {
		return "", err
	}
	id, n := "", 0
	for _, r := range records {
		if dnsname.Canonical(r.Hostname) == host && r.IP == p.address && r.RecordType == "A" && r.ID != "" {
			id, n = r.ID, n+1
		}
	}
	if n != 1 {
		return "", nil
	}
	if _, known := p.created[id]; known {
		return "", nil
	}
	return id, nil
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
			for _, host := range dnsname.RuleHosts(router.Rule) {
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

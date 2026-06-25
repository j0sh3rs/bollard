package unifi

import (
	"fmt"
	"time"
)

// DNSRecord is bollard's internal representation of a managed DNS record.
type DNSRecord struct {
	ID         string
	Hostname   string
	IP         string
	RecordType string
	TTL        int
}

// Config holds UniFi connection parameters.
type Config struct {
	Host              string
	APIKey            string
	Site              string
	SkipTLSVerify     bool
	CACertPath        string
	RetryAttempts     int
	RetryInitialDelay time.Duration
	RetryMaxDelay     time.Duration
}

func (c *Config) setDefaults() {
	if c.Site == "" {
		c.Site = "default"
	}
	if c.RetryAttempts == 0 {
		c.RetryAttempts = 3
	}
	if c.RetryInitialDelay == 0 {
		c.RetryInitialDelay = 500 * time.Millisecond
	}
	if c.RetryMaxDelay == 0 {
		c.RetryMaxDelay = 10 * time.Second
	}
}

// Modern API wire types.
type dnsPolicyEnvelope struct {
	ID          string `json:"id,omitempty"`
	Type        string `json:"type"`
	Enabled     bool   `json:"enabled"`
	Domain      string `json:"domain,omitempty"`
	IPv4Address string `json:"ipv4Address,omitempty"`
	TTLSeconds  *int   `json:"ttlSeconds,omitempty"`
}

const policyTypeA = "A_RECORD"

type apiPage[T any] struct {
	Count      int `json:"count"`
	TotalCount int `json:"totalCount"`
	Data       []T `json:"data"`
}

// Legacy static-DNS wire types.
type staticDNSEntry struct {
	ID         string `json:"_id,omitempty"`
	Key        string `json:"key"`
	Value      string `json:"value"`
	RecordType string `json:"record_type"`
	TTL        int    `json:"ttl,omitempty"`
	Enabled    bool   `json:"enabled"`
}

type staticDNSResponse struct {
	Data []staticDNSEntry `json:"data"`
	Meta struct {
		RC string `json:"rc"`
	} `json:"meta"`
}

type apiErrorResponse struct {
	StatusCode int    `json:"statusCode"`
	StatusName string `json:"statusName"`
	Message    string `json:"message"`
}

func (e *apiErrorResponse) Error() string {
	return fmt.Sprintf("unifi api error %d (%s): %s", e.StatusCode, e.StatusName, e.Message)
}

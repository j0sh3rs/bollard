package docker_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/j0sh3rs/bollard/internal/docker"
)

func parse(t *testing.T, labels map[string]string, opts docker.ParseOptions) []docker.RecordSpec {
	t.Helper()
	specs, err := docker.ParseLabels(labels, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return specs
}

func TestParseLabels_OptInDefaults(t *testing.T) {
	specs := parse(t, map[string]string{"dns.bollard/hostname": "MyApp.HOME.arpa."}, docker.ParseOptions{})
	want := []docker.RecordSpec{{Hostname: "myapp.home.arpa", RecordType: "A", TTL: 300}}
	if !reflect.DeepEqual(specs, want) {
		t.Fatalf("got %+v, want %+v", specs, want)
	}
}

func TestParseLabels_NotOptedInOrDisabled(t *testing.T) {
	for name, labels := range map[string]map[string]string{
		"no labels":      {},
		"empty hostname": {"dns.bollard/hostname": ""},
		"enabled false":  {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/enabled": "false"},
		"enabled no":     {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/enabled": "no"},
		"enabled zero":   {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/enabled": "0"},
	} {
		t.Run(name, func(t *testing.T) {
			if specs := parse(t, labels, docker.ParseOptions{}); specs != nil {
				t.Fatalf("got %+v, want none", specs)
			}
		})
	}
}

func TestParseLabels_EnabledTrueVariants(t *testing.T) {
	for _, v := range []string{"true", "1", "yes"} {
		specs := parse(t, map[string]string{"dns.bollard/hostname": "a.home.arpa", "dns.bollard/enabled": v}, docker.ParseOptions{})
		if len(specs) != 1 {
			t.Errorf("enabled=%q: got %d specs, want 1", v, len(specs))
		}
	}
}

func TestParseLabels_MultipleHostnames(t *testing.T) {
	specs := parse(t, map[string]string{
		"dns.bollard/hostname": "One.home.arpa, two.home.arpa ,three.home.arpa.",
		"dns.bollard/ttl":      "60",
	}, docker.ParseOptions{})
	var got []string
	for _, s := range specs {
		got = append(got, s.Hostname)
		if s.TTL != 60 || s.RecordType != "A" {
			t.Errorf("spec %+v does not share the TTL and type labels", s)
		}
	}
	if want := []string{"one.home.arpa", "two.home.arpa", "three.home.arpa"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hostnames %v, want %v", got, want)
	}
}

func TestParseLabels_Invalid(t *testing.T) {
	many := make([]string, 33)
	for i := range many {
		many[i] = "h" + strings.Repeat("a", i) + ".home.arpa"
	}
	for name, labels := range map[string]map[string]string{
		"bad hostname":        {"dns.bollard/hostname": "bad_name.home.arpa"},
		"bad second hostname": {"dns.bollard/hostname": "ok.home.arpa,-bad.home.arpa"},
		"duplicate hostname":  {"dns.bollard/hostname": "a.home.arpa,A.HOME.ARPA."},
		"too many hostnames":  {"dns.bollard/hostname": strings.Join(many, ",")},
		"bad ttl":             {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/ttl": "abc"},
		"negative ttl":        {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/ttl": "-1"},
		"unsupported type":    {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/record-type": "MX"},
		"bad enabled":         {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/enabled": "maybe"},
		"bad ip":              {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/ip-override": "nope"},
		"A with IPv6":         {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/ip-override": "2001:db8::1"},
		"AAAA with IPv4":      {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/record-type": "AAAA", "dns.bollard/ip-override": "192.168.1.5"},
		"AAAA link-local":     {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/record-type": "AAAA", "dns.bollard/ip-override": "fe80::1"},
		"ip and network":      {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/ip-override": "192.168.1.5", "dns.bollard/network": "lan"},
		"target on A":         {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/target": "b.home.arpa"},
		"CNAME no target":     {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/record-type": "CNAME"},
		"CNAME bad target":    {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/record-type": "CNAME", "dns.bollard/target": "bad_name"},
		"CNAME to itself":     {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/record-type": "CNAME", "dns.bollard/target": "A.home.arpa."},
		"CNAME with ip":       {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/record-type": "CNAME", "dns.bollard/target": "b.home.arpa", "dns.bollard/ip-override": "192.168.1.5"},
		"CNAME with network":  {"dns.bollard/hostname": "a.home.arpa", "dns.bollard/record-type": "CNAME", "dns.bollard/target": "b.home.arpa", "dns.bollard/network": "lan"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := docker.ParseLabels(labels, docker.ParseOptions{}); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestParseLabels_RecordTypes(t *testing.T) {
	for name, tc := range map[string]struct {
		labels map[string]string
		want   docker.RecordSpec
	}{
		"AAAA inferred address": {
			map[string]string{"dns.bollard/hostname": "a.home.arpa", "dns.bollard/record-type": "AAAA"},
			docker.RecordSpec{Hostname: "a.home.arpa", RecordType: "AAAA", TTL: 300},
		},
		"AAAA override": {
			map[string]string{"dns.bollard/hostname": "a.home.arpa", "dns.bollard/record-type": "AAAA", "dns.bollard/ip-override": "fd00::5"},
			docker.RecordSpec{Hostname: "a.home.arpa", RecordType: "AAAA", TTL: 300, IPOverride: "fd00::5"},
		},
		"A network": {
			map[string]string{"dns.bollard/hostname": "a.home.arpa", "dns.bollard/network": "lan"},
			docker.RecordSpec{Hostname: "a.home.arpa", RecordType: "A", TTL: 300, Network: "lan"},
		},
		"CNAME": {
			map[string]string{"dns.bollard/hostname": "www.home.arpa", "dns.bollard/record-type": "CNAME", "dns.bollard/target": "App.Home.Arpa.", "dns.bollard/ttl": "0"},
			docker.RecordSpec{Hostname: "www.home.arpa", RecordType: "CNAME", TTL: 0, Target: "app.home.arpa"},
		},
		"A override": {
			map[string]string{"dns.bollard/hostname": "a.home.arpa", "dns.bollard/ip-override": "192.168.1.5"},
			docker.RecordSpec{Hostname: "a.home.arpa", RecordType: "A", TTL: 300, IPOverride: "192.168.1.5"},
		},
		"empty override means infer": {
			map[string]string{"dns.bollard/hostname": "a.home.arpa", "dns.bollard/ip-override": ""},
			docker.RecordSpec{Hostname: "a.home.arpa", RecordType: "A", TTL: 300},
		},
	} {
		t.Run(name, func(t *testing.T) {
			specs := parse(t, tc.labels, docker.ParseOptions{})
			if len(specs) != 1 || specs[0] != tc.want {
				t.Fatalf("got %+v, want %+v", specs, tc.want)
			}
		})
	}
}

func TestParseLabels_TraefikInference(t *testing.T) {
	rule := func(r string) map[string]string {
		return map[string]string{"traefik.http.routers.web.rule": r}
	}
	for name, tc := range map[string]struct {
		labels map[string]string
		opts   docker.ParseOptions
		want   []string
	}{
		"off by default":      {rule("Host(`app.home.arpa`)"), docker.ParseOptions{}, nil},
		"single host":         {rule("Host(`app.home.arpa`)"), docker.ParseOptions{InferTraefik: true}, []string{"app.home.arpa"}},
		"negated is skipped":  {rule("!Host(`no.home.arpa`) && Host(`yes.home.arpa`)"), docker.ParseOptions{InferTraefik: true}, []string{"yes.home.arpa"}},
		"regexp is skipped":   {rule("HostRegexp(`.*.home.arpa`)"), docker.ParseOptions{InferTraefik: true}, nil},
		"explicit label wins": {map[string]string{"dns.bollard/hostname": "mine.home.arpa", "traefik.http.routers.web.rule": "Host(`app.home.arpa`)"}, docker.ParseOptions{InferTraefik: true}, []string{"mine.home.arpa"}},
		"traefik disabled":    {map[string]string{"traefik.enable": "false", "traefik.http.routers.web.rule": "Host(`app.home.arpa`)"}, docker.ParseOptions{InferTraefik: true}, nil},
		"two routers deduped and sorted": {map[string]string{
			"traefik.http.routers.b.rule": "Host(`b.home.arpa`) || Host(`a.home.arpa`)",
			"traefik.http.routers.a.rule": "Host(`a.home.arpa`)",
		}, docker.ParseOptions{InferTraefik: true}, []string{"a.home.arpa", "b.home.arpa"}},
	} {
		t.Run(name, func(t *testing.T) {
			var got []string
			for _, s := range parse(t, tc.labels, tc.opts) {
				got = append(got, s.Hostname)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("hostnames %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseLabels_InferredNamesHonourOtherLabels(t *testing.T) {
	specs := parse(t, map[string]string{
		"traefik.http.routers.web.rule": "Host(`app.home.arpa`)",
		"dns.bollard/ttl":               "30",
		"dns.bollard/enabled":           "false",
	}, docker.ParseOptions{InferTraefik: true})
	if specs != nil {
		t.Fatalf("a disabled container got records: %+v", specs)
	}
}

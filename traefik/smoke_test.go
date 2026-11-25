package traefik

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestTraefikPlugin proves interpreter loading, router discovery, DNS creation, and unchanged HTTP routing.
func TestTraefikPlugin(t *testing.T) {
	image := os.Getenv("TRAEFIK_IMAGE")
	if image == "" {
		t.Skip("set TRAEFIK_IMAGE to run the Docker-backed plugin smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var mu sync.Mutex
	policies := []map[string]interface{}{}
	created := make(chan struct{}, 1)
	mock := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/" {
			fmt.Fprint(w, "backend reached")
			return
		}
		if r.Header.Get("X-API-KEY") != "smoke-key" {
			t.Error("missing API key at mock controller")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/proxy/network/integration/v1/sites":
			fmt.Fprint(w, `{"data":[{"id":"smoke-site","name":"default"}],"count":1,"totalCount":1}`)
		case "/proxy/network/integration/v1/sites/smoke-site/dns/policies":
			switch r.Method {
			case http.MethodGet:
				json.NewEncoder(w).Encode(map[string]interface{}{"data": policies, "count": len(policies), "totalCount": len(policies)})
			case http.MethodPost:
				var policy map[string]interface{}
				if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if policy["domain"] != "smoke.home.arpa" || policy["ipv4Address"] != "192.0.2.10" || policy["type"] != "A_RECORD" || policy["enabled"] != true || policy["ttlSeconds"] != float64(300) {
					t.Errorf("unexpected DNS policy: %v", policy)
				}
				policy["id"] = "smoke-record"
				policies = append(policies, policy)
				json.NewEncoder(w).Encode(policy)
				select {
				case created <- struct{}{}:
				default:
				}
			default:
				t.Errorf("unexpected DNS mutation %s", r.Method)
				w.WriteHeader(405)
			}
		default:
			t.Errorf("unexpected mock controller path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	mock.Listener = listener
	mock.Start()
	defer mock.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	endpoint := fmt.Sprintf("http://host.docker.internal:%d", port)

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// A cold Docker pull writes progress to stderr before printing the container ID.
	// Inject that output even when CI's image cache is warm.
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "docker")
	script := "#!/bin/sh\nif [ \"$1\" = run ]; then printf 'Pulling smoke image\\n' >&2; fi\nexec \"$BOLLARD_SMOKE_DOCKER\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOLLARD_SMOKE_DOCKER", docker)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	static := fmt.Sprintf(`entryPoints:
  web:
    address: ":8000"
  bollard-api:
    address: "127.0.0.1:8080"
api:
  insecure: false
  dashboard: false
log:
  level: DEBUG
experimental:
  localPlugins:
    bollard:
      moduleName: github.com/j0sh3rs/bollard
providers:
  file:
    filename: /etc/traefik/dynamic.yml
  plugin:
    bollard:
      traefikAPI: http://127.0.0.1:8080
      unifiHost: %s
      unifiAPIKey: smoke-key
      address: 192.0.2.10
      pollInterval: 200ms
`, endpoint)
	dynamic := fmt.Sprintf(`http:
  routers:
    bollard-api:
      entryPoints: [bollard-api]
      rule: PathPrefix(`+"`/api`"+`)
      service: api@internal
    smoke:
      entryPoints: [web]
      rule: Host(`+"`smoke.home.arpa`"+`)
      service: smoke
  services:
    smoke:
      loadBalancer:
        servers:
          - url: %s
`, endpoint)
	for name, content := range map[string]string{"traefik.yml": static, "dynamic.yml": dynamic} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"run", "--detach", "--publish", "127.0.0.1::8000", "--mount", "type=bind,src=" + root + ",dst=/plugins-local/src/github.com/j0sh3rs/bollard,readonly", "--mount", "type=bind,src=" + dir + ",dst=/etc/traefik,readonly"}
	if runtime.GOOS == "linux" {
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
	}
	args = append(args, image, "--configFile=/etc/traefik/traefik.yml")
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		t.Fatalf("start Traefik: %v\n%s", err, out)
	}
	id := strings.TrimSpace(string(out))
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer stopCancel()
		if out, err := exec.CommandContext(stopCtx, "docker", "stop", "--time", "5", id).CombinedOutput(); err != nil {
			t.Errorf("stop Traefik: %v\n%s", err, out)
		}
		logs, _ := exec.CommandContext(stopCtx, "docker", "logs", id).CombinedOutput()
		if t.Failed() {
			t.Logf("Traefik logs:\n%s", logs)
		}
		if out, err := exec.CommandContext(stopCtx, "docker", "rm", id).CombinedOutput(); err != nil {
			t.Errorf("remove Traefik container: %v\n%s", err, out)
		}
	}()
	select {
	case <-created:
		t.Log("real Traefik loaded provider and created smoke.home.arpa -> 192.0.2.10")
	case <-ctx.Done():
		t.Fatal("Traefik did not create DNS record before deadline")
	}
	out, err = exec.CommandContext(ctx, "docker", "port", id, "8000/tcp").CombinedOutput()
	if err != nil {
		t.Fatalf("get Traefik HTTP port: %v\n%s", err, out)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+strings.TrimSpace(string(out)), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "smoke.home.arpa"
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 || string(body) != "backend reached" {
		t.Fatalf("router did not reach backend: status=%d body=%q err=%v", resp.StatusCode, body, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(policies) != 1 {
		t.Fatalf("created duplicate policies: %v", policies)
	}
}

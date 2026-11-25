package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDaemonImageSmoke(t *testing.T) {
	image := os.Getenv("BOLLARD_SMOKE_IMAGE")
	if image == "" {
		t.Skip("set BOLLARD_SMOKE_IMAGE to an image built with VERSION=v0.0.0-smoke")
	}

	const (
		dindImage     = "docker:28-dind"
		workloadImage = "alpine:3.23"
		socket        = "unix:///smoke/docker.sock"
		ip            = "192.0.2.10"
		ownedID       = "smoke-owned"
		sites         = "/proxy/network/integration/v1/sites"
		policies      = sites + "/smoke/dns/policies"
	)
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	namespace := "bollard-smoke-" + hex.EncodeToString(token[:])
	hostname := namespace + ".test"

	if out, err := smokeDocker("run", "--rm", "--pull=never", "--network", "none", image, "-version"); err != nil || out != "v0.0.0-smoke\n" {
		t.Fatalf("image -version = %q, %v; want exactly v0.0.0-smoke followed by a newline", out, err)
	}

	type policy struct {
		ID      string `json:"id,omitempty"`
		Type    string `json:"type"`
		Domain  string `json:"domain"`
		Address string `json:"ipv4Address"`
		TTL     int    `json:"ttlSeconds"`
		Enabled bool   `json:"enabled"`
	}
	unrelated := policy{"unrelated", "A_RECORD", "unrelated.test", "192.0.2.99", 300, true}
	var mu sync.Mutex
	var owned *policy
	var deleted bool
	var siteProbes int
	listener, err := net.Listen("tcp4", ":0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &http.Server{ReadHeaderTimeout: 2 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != namespace {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == sites:
			siteProbes++
			if siteProbes <= 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"count":1,"totalCount":1,"data":[{"id":"smoke","name":"smoke"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == policies:
			records := []policy{unrelated}
			if owned != nil {
				records = append(records, *owned)
			}
			json.NewEncoder(w).Encode(map[string]any{"data": records, "count": len(records), "totalCount": len(records)})
		case r.Method == http.MethodPost && r.URL.Path == policies:
			var got policy
			want := policy{"", "A_RECORD", hostname, ip, 120, true}
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got != want || owned != nil || deleted {
				t.Errorf("unexpected DNS mutation: POST %+v (decode error: %v)", got, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			got.ID = ownedID
			owned = &got
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(got)
		case r.Method == http.MethodDelete && r.URL.Path == policies+"/"+ownedID && owned != nil:
			owned, deleted = nil, true
			w.WriteHeader(http.StatusNoContent)
		default:
			if r.Method != http.MethodGet {
				t.Errorf("unexpected DNS mutation: %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNotFound)
		}
	})}
	go func() { _ = fixture.Serve(listener) }()
	t.Cleanup(func() { _ = fixture.Close() })

	for _, img := range []string{dindImage, workloadImage} {
		if _, err := smokeDocker("image", "inspect", img); err != nil {
			smokeDockerOK(t, "pull", img)
		}
	}

	volume := smokeDockerOK(t, "volume", "create", namespace)
	t.Cleanup(func() { smokeDockerCleanup(t, "volume", "rm", volume) })
	mount := "type=volume,src=" + volume + ",dst=/smoke"
	// Give Bollard only the disposable daemon's socket, never the host's socket.
	dind := smokeDockerOK(t, "create", "--privileged", "--network", "none", "--mount", mount,
		"--entrypoint", "dockerd", dindImage, "--host="+socket,
		"--bridge=none", "--iptables=false", "--ip6tables=false", "--storage-driver=vfs")
	defer smokeContainerCleanup(t, dind)
	smokeDockerOK(t, "start", dind)
	nested := []string{"exec", dind, "docker", "--host", socket}
	smokeWait(t, "isolated Docker daemon", func() bool {
		_, err := smokeDocker(append(nested, "info")...)
		return err == nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	archive := exec.CommandContext(ctx, "docker", "save", workloadImage)
	load := exec.CommandContext(ctx, "docker", "exec", "-i", dind, "docker", "--host", socket, "load")
	var saveErr bytes.Buffer
	archive.Stderr = &saveErr
	pipe, err := archive.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	load.Stdin = pipe
	if err := archive.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	out, loadErr := load.CombinedOutput()
	if loadErr != nil {
		cancel()
	}
	err = archive.Wait()
	cancel()
	if err != nil || loadErr != nil {
		t.Fatalf("transfer workload image: save: %v (%s), load: %v (%s)", err, &saveErr, loadErr, out)
	}

	workload := smokeDockerOK(t, append(nested, "create", "--network", "none", "--name", namespace,
		"--label", "dns.bollard/hostname="+hostname, "--label", "dns.bollard/ip-override="+ip,
		"--label", "dns.bollard/ttl=120", workloadImage, "sleep", "300")...)
	defer smokeDockerCleanup(t, append(nested, "rm", "-f", workload)...)

	args := []string{"create", "--pull=never", "--mount", mount + ",readonly", "--health-interval", "1s",
		"--env", "DOCKER_HOST=" + socket,
		"--env", "UNIFI_HOST=http://host.docker.internal:" + fmt.Sprint(listener.Addr().(*net.TCPAddr).Port),
		"--env", "UNIFI_SITE=smoke", "--env", "UNIFI_API_KEY=" + namespace,
		"--env", "UNIFI_SKIP_TLS_VERIFY=false", "--env", "DATABASE_URL=file:/bollard-smoke.db",
		"--env", "METRICS_ADDR=127.0.0.1:9190", "--env", "RECONCILE_INTERVAL=1s"}
	if runtime.GOOS == "linux" {
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
	}
	daemon := smokeDockerOK(t, append(args, image)...)
	defer smokeContainerCleanup(t, daemon)
	smokeDockerOK(t, "start", daemon)
	smokeWait(t, "image HEALTHCHECK", func() bool {
		out, err := smokeDocker("inspect", "--format", "{{.State.Health.Status}}", daemon)
		return err == nil && strings.TrimSpace(out) == "healthy"
	})
	smokeWait(t, "daemon startup after three failed UniFi probes", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return siteProbes > 3
	})

	smokeDockerOK(t, append(nested, "start", workload)...)
	smokeWait(t, "annotated workload DNS creation", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return owned != nil
	})
	smokeDockerOK(t, append(nested, "stop", "--time", "1", workload)...)
	smokeWait(t, "owned DNS deletion after stop", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return deleted && owned == nil
	})
	exposition := smokeDockerOK(t, "run", "--rm", "--pull=never", "--network", "container:"+daemon,
		workloadImage, "wget", "-qO-", "http://127.0.0.1:9190/metrics")
	exposition = "\n" + exposition + "\n"
	for _, want := range []string{
		`bollard_unifi_requests_total{method="GET",status="503"} 3`,
		`bollard_unifi_requests_total{method="POST",status="201"} 1`,
		`bollard_unifi_request_duration_seconds_count{method="POST"} 1`,
		`bollard_unifi_api_version{provider="modern"} 1`,
		`bollard_up 1`,
	} {
		if !strings.Contains(exposition, "\n"+want+"\n") {
			t.Errorf("metrics exposition missing %q", want)
		}
	}
}

func smokeDocker(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}

func smokeDockerOK(t *testing.T, args ...string) string {
	t.Helper()
	out, err := smokeDocker(args...)
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", args[0], err, out)
	}
	return strings.TrimSpace(out)
}

func smokeDockerCleanup(t *testing.T, args ...string) {
	t.Helper()
	if out, err := smokeDocker(args...); err != nil {
		t.Errorf("cleanup docker %s: %v\n%s", args[0], err, out)
	}
}

func smokeContainerCleanup(t *testing.T, id string) {
	t.Helper()
	if t.Failed() {
		out, err := smokeDocker("logs", id)
		t.Logf("container %s logs (%v):\n%s", id, err, out)
	}
	smokeDockerCleanup(t, "rm", "-fv", id)
}

func smokeWait(t *testing.T, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

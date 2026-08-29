package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sparkfabrik/http-proxy/pkg/config"
	"github.com/sparkfabrik/http-proxy/pkg/logger"
	"github.com/sparkfabrik/http-proxy/pkg/tailscale"
	"github.com/sparkfabrik/http-proxy/pkg/traefikapi"
)

const tailnetStatus = `{
  "Self": {"HostName": "machine-a", "Online": true, "UserID": 1, "TailscaleIPs": ["100.64.0.1"]},
  "Peer": {
    "nodekey:01": {"HostName": "machine-b", "Online": true, "UserID": 1, "TailscaleIPs": ["100.64.0.2"]},
    "nodekey:02": {"HostName": "machine-c", "Online": true, "UserID": 1, "TailscaleIPs": ["100.64.0.3"]},
    "nodekey:03": {"HostName": "machine-d", "Online": true, "UserID": 2, "TailscaleIPs": ["100.64.0.4"]}
  }
}`

// Two machines called the same thing, which Tailscale permits and real tailnets
// contain.
const duplicateHostnames = `{
  "Self": {"HostName": "machine-a", "Online": true, "UserID": 1, "TailscaleIPs": ["100.64.0.1"]},
  "Peer": {
    "nodekey:10": {"HostName": "machine-b", "Online": true, "UserID": 1, "TailscaleIPs": ["100.64.0.10"]},
    "nodekey:20": {"HostName": "machine-b", "Online": true, "UserID": 1, "TailscaleIPs": ["100.64.0.20"]}
  }
}`

type fakeSource struct {
	document  string
	updatedAt time.Time
	err       error
}

func (f fakeSource) Status(context.Context) (*tailscale.Status, error) {
	if f.err != nil {
		return nil, f.err
	}
	status, err := tailscale.ParseStatus(strings.NewReader(f.document))
	if err != nil {
		return nil, err
	}
	status.UpdatedAt = f.updatedAt
	return status, nil
}

// fakeProbe answers per base URL, so a machine that runs no proxy is expressed
// by having no entry rather than by a flag.
type fakeProbe struct {
	routes   map[string][]traefikapi.Route
	rejected map[string][]string
	errs     map[string]error
	calls    []string
}

func (f *fakeProbe) probe(_ context.Context, baseURL string) (traefikapi.Result, error) {
	f.calls = append(f.calls, baseURL)
	if err, failing := f.errs[baseURL]; failing {
		return traefikapi.Result{}, err
	}
	routes, found := f.routes[baseURL]
	if !found {
		return traefikapi.Result{}, fmt.Errorf("%w: connection refused", traefikapi.ErrUnreachable)
	}
	return traefikapi.Result{Routes: routes, Rejected: f.rejected[baseURL]}, nil
}

func hostRoute(host string) traefikapi.Route {
	return traefikapi.Route{
		Rule:     fmt.Sprintf("Host(`%s`)", host),
		Priority: 49,
		Hosts:    []traefikapi.Host{{Value: host}},
	}
}

// wildcardRoute is a peer route claiming a pattern rather than a name.
func wildcardRoute(pattern string) traefikapi.Route {
	return traefikapi.Route{
		Rule:     fmt.Sprintf("HostRegexp(`%s`)", pattern),
		Priority: 49,
		Hosts:    []traefikapi.Host{{Value: pattern, IsRegexp: true}},
	}
}

// servedLocally builds the local side of the ownership check from plain names.
func servedLocally(hosts ...string) *localRoutes {
	local := &localRoutes{literals: map[string]struct{}{}, patternText: map[string]struct{}{}}
	for _, host := range hosts {
		local.literals[host] = struct{}{}
	}
	return local
}

func testDiscovery(t *testing.T, cfg *config.TailscaleConfig, source tailscale.Source, probe *fakeProbe) *discovery {
	t.Helper()
	dir := t.TempDir()
	cfg.TraefikDynamicDir = filepath.Join(dir, "dynamic")
	cfg.StateFile = filepath.Join(dir, "state", "tailscale-peers.json")
	if cfg.LocalAPIURL == "" {
		cfg.LocalAPIURL = "http://http-proxy:8080"
	}
	if cfg.RefreshInterval == 0 {
		cfg.RefreshInterval = 10 * time.Second
	}
	return newDiscovery(cfg, logger.New("test"), source, probe.probe)
}

func peerURL(address string) string {
	return fmt.Sprintf("http://%s:%d", address, peerAPIPort)
}

// No document means nothing probed and nothing written.
func TestSourceFailureProbesNothingAndWritesNothing(t *testing.T) {
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{
		"http://http-proxy:8080": nil,
		peerURL("100.64.0.2"):    {hostRoute("app.loc")},
	}}
	cfg := &config.TailscaleConfig{Enabled: true, Source: config.TailscaleSourceSocket}

	d := testDiscovery(t, cfg, fakeSource{err: fmt.Errorf("dial unix: no such file")}, probe)
	report := d.runCycle(t.Context())

	for _, called := range probe.calls {
		if called == peerURL("100.64.0.2") {
			t.Error("probed a machine with no status document, want the cycle to contribute nothing")
		}
	}
	if report.SourceError == "" {
		t.Error("SourceError is empty, want a failing source reported rather than an empty tailnet")
	}
	if len(report.Peers) != 0 {
		t.Errorf("Peers = %+v, want none", report.Peers)
	}

	entries, err := os.ReadDir(cfg.TraefikDynamicDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("failed to read the dynamic directory: %v", err)
	}
	for _, entry := range entries {
		if isPeerConfigFile(entry.Name()) {
			t.Errorf("wrote %s, want nothing written for an unverified cycle", entry.Name())
		}
	}
}

// The document's age is recorded, so the command line can show it.
func TestVerifiedCycleRecordsTheDocumentAge(t *testing.T) {
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{
		"http://http-proxy:8080": nil,
		peerURL("100.64.0.2"):    {hostRoute("app.loc")},
	}}
	cfg := &config.TailscaleConfig{Enabled: true, Source: config.TailscaleSourceFile}
	written := time.Now().Add(-30 * time.Second).UTC().Truncate(time.Second)

	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus, updatedAt: written}, probe)
	report := d.runCycle(t.Context())

	if !report.SourceUpdatedAt.Equal(written) {
		t.Errorf("SourceUpdatedAt = %s, want %s", report.SourceUpdatedAt, written)
	}

	statuses := map[string]string{}
	for _, peer := range report.Peers {
		statuses[peer.Name] = peer.Status
	}
	if statuses["machine-b"] != statusOK {
		t.Errorf("machine-b status = %q, want %q", statuses["machine-b"], statusOK)
	}
	if statuses["machine-d"] != statusSkipped {
		t.Errorf("machine-d status = %q, want another user's machine skipped", statuses["machine-d"])
	}

	if err := d.writeReport(report); err != nil {
		t.Fatalf("writeReport() error = %v", err)
	}
	data, err := os.ReadFile(cfg.StateFile)
	if err != nil {
		t.Fatalf("failed to read the state file: %v", err)
	}
	var roundTripped Report
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("state file is not valid json: %v", err)
	}
	if !roundTripped.SourceUpdatedAt.Equal(written) {
		t.Errorf("state file sourceUpdatedAt = %s, want %s", roundTripped.SourceUpdatedAt, written)
	}
}

// A machine that stops answering has its file removed.
func TestStaleConfigurationIsRemoved(t *testing.T) {
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{
		"http://http-proxy:8080": nil,
		peerURL("100.64.0.2"):    {hostRoute("app.loc")},
		peerURL("100.64.0.3"):    {hostRoute("other.loc")},
	}}
	cfg := &config.TailscaleConfig{Enabled: true, Source: config.TailscaleSourceSocket}

	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)
	d.runCycle(t.Context())

	for _, name := range []string{"tailscale-peer-machine-b.yaml", "tailscale-peer-machine-c.yaml"} {
		if _, err := os.Stat(filepath.Join(cfg.TraefikDynamicDir, name)); err != nil {
			t.Fatalf("expected %s to be written: %v", name, err)
		}
	}

	// A file the other service owns, and one the proxy carries itself, must
	// survive reconciliation.
	foreign := filepath.Join(cfg.TraefikDynamicDir, "0123456789ab.yaml")
	if err := os.WriteFile(foreign, []byte("http: {}\n"), 0644); err != nil {
		t.Fatalf("failed to write the foreign config: %v", err)
	}

	delete(probe.routes, peerURL("100.64.0.3"))
	d.now = func() time.Time { return time.Now().Add(time.Hour) }
	d.runCycle(t.Context())

	if _, err := os.Stat(filepath.Join(cfg.TraefikDynamicDir, "tailscale-peer-machine-c.yaml")); !os.IsNotExist(err) {
		t.Error("tailscale-peer-machine-c.yaml survived, want a machine that stopped answering withdrawn")
	}
	if _, err := os.Stat(filepath.Join(cfg.TraefikDynamicDir, "tailscale-peer-machine-b.yaml")); err != nil {
		t.Errorf("tailscale-peer-machine-b.yaml was removed: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("a file owned by another service was removed: %v", err)
	}
}

func TestResolveOwnership(t *testing.T) {
	shared := hostRoute("app.loc")
	pathRoute := traefikapi.Route{
		Rule:     "Host(`app.loc`) && (PathPrefix(`/api/`) || Path(`/api`))",
		Priority: 10004,
		Hosts:    []traefikapi.Host{{Value: "app.loc"}},
	}

	tests := []struct {
		name       string
		results    []probeResult
		local      *localRoutes
		wantRoutes map[string]int
		wantOwner  map[string]string
	}{
		{
			name: "a local container wins",
			results: []probeResult{
				{candidate: candidate{id: "nodekey:01", name: "machine-b", address: "100.64.0.2"}, status: statusOK, routes: []traefikapi.Route{shared}},
			},
			local:      servedLocally("app.loc"),
			wantRoutes: map[string]int{"machine-b": 0},
			wantOwner:  map[string]string{"app.loc": localOwner},
		},
		{
			name: "the first machine in name order wins",
			results: []probeResult{
				{candidate: candidate{id: "nodekey:01", name: "machine-b", address: "100.64.0.2"}, status: statusOK, routes: []traefikapi.Route{shared}},
				{candidate: candidate{id: "nodekey:02", name: "machine-c", address: "100.64.0.3"}, status: statusOK, routes: []traefikapi.Route{shared}},
			},
			local:      servedLocally(),
			wantRoutes: map[string]int{"machine-b": 1, "machine-c": 0},
			wantOwner:  map[string]string{"app.loc": "machine-b"},
		},
		{
			name: "one machine keeps both routes of a hostname it mounts a path on",
			results: []probeResult{
				{candidate: candidate{id: "nodekey:01", name: "machine-b", address: "100.64.0.2"}, status: statusOK, routes: []traefikapi.Route{shared, pathRoute}},
			},
			local:      servedLocally(),
			wantRoutes: map[string]int{"machine-b": 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owned, collisions := resolveOwnership(tt.results, tt.local)

			for _, machine := range owned {
				if want, checked := tt.wantRoutes[machine.name]; checked && len(machine.routes) != want {
					t.Errorf("%s kept %d routes, want %d", machine.name, len(machine.routes), want)
				}
			}
			for host, owner := range tt.wantOwner {
				var reported bool
				for _, collision := range collisions {
					if collision.Host == host && collision.ServedBy == owner {
						reported = true
					}
				}
				if !reported {
					t.Errorf("collisions = %+v, want %s reported as served by %s", collisions, host, owner)
				}
			}
		})
	}
}

// Two machines claiming one hostname must resolve the same way on every cycle,
// whatever order they were probed in.
func TestOwnershipDoesNotDependOnProbeOrder(t *testing.T) {
	first := probeResult{candidate: candidate{id: "nodekey:01", name: "machine-b", address: "100.64.0.2"}, status: statusOK, routes: []traefikapi.Route{hostRoute("app.loc")}}
	second := probeResult{candidate: candidate{id: "nodekey:02", name: "machine-c", address: "100.64.0.3"}, status: statusOK, routes: []traefikapi.Route{hostRoute("app.loc")}}

	forward, _ := resolveOwnership(sortResults([]probeResult{first, second}), servedLocally())
	reverse, _ := resolveOwnership(sortResults([]probeResult{second, first}), servedLocally())

	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("ownership differs with probe order: %+v vs %+v", forward, reverse)
	}
}

func sortResults(results []probeResult) []probeResult {
	candidates := make([]candidate, 0, len(results))
	for _, result := range results {
		candidates = append(candidates, result.candidate)
	}
	sorted := sortCandidates(candidates)

	ordered := make([]probeResult, 0, len(results))
	for _, c := range sorted {
		for _, result := range results {
			if result.name == c.name {
				ordered = append(ordered, result)
			}
		}
	}
	return ordered
}

func TestPeerTraefikConfig(t *testing.T) {
	routes := []traefikapi.Route{
		hostRoute("app.loc"),
		{Rule: "Host(`app.loc`) && (PathPrefix(`/api/`) || Path(`/api`))", Priority: 10004, Hosts: []traefikapi.Host{{Value: "app.loc"}}},
	}

	cfg := peerTraefikConfig("machine-b", "100.64.0.2", routes)

	service := cfg.HTTP.Services["tailscale-peer-machine-b"]
	if service == nil || len(service.LoadBalancer.Servers) != 1 {
		t.Fatalf("Services = %+v, want one service with one server", cfg.HTTP.Services)
	}
	if got, want := service.LoadBalancer.Servers[0].URL, "http://100.64.0.2:80"; got != want {
		t.Errorf("server URL = %q, want %q", got, want)
	}
	if len(cfg.HTTP.Routers) != 4 {
		t.Fatalf("Routers = %+v, want a pair per route", cfg.HTTP.Routers)
	}

	plain := cfg.HTTP.Routers["tailscale-peer-machine-b-1"]
	secure := cfg.HTTP.Routers["tailscale-peer-machine-b-tls-1"]
	if plain == nil || secure == nil {
		t.Fatalf("Routers = %+v, want a plain and a TLS router per route", cfg.HTTP.Routers)
	}
	if plain.Rule != routes[1].Rule || secure.Rule != routes[1].Rule {
		t.Errorf("rule = %q / %q, want the peer's rule copied verbatim", plain.Rule, secure.Rule)
	}
	if plain.Priority != routes[1].Priority || secure.Priority != routes[1].Priority {
		t.Errorf("priority = %d / %d, want the peer's own ordering preserved", plain.Priority, secure.Priority)
	}
	if secure.TLS == nil {
		t.Error("TLS router has no TLS section, want encryption terminated locally")
	}
	if plain.TLS != nil {
		t.Error("plain router has a TLS section")
	}
}

func TestPeerSlug(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"machine-b", "machine-b"},
		{"Machine.B", "machine-b"},
		{"100.64.0.2", "100-64-0-2"},
		{"--", "peer"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := peerSlug(tt.in); got != tt.want {
				t.Errorf("peerSlug(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Neither service can remove the other's files. The per-container layer owns
// twelve-character hexadecimal names, which is why this pattern is anchored on
// a prefix those names cannot carry.
func TestConfigFileOwnershipIsDisjoint(t *testing.T) {
	dinghyPattern := regexp.MustCompile(`^[0-9a-f]{12}\.yaml$`)

	ours := []string{"tailscale-peer-machine-b.yaml", "tailscale-peer-100-64-0-2.yaml"}
	theirs := []string{"0123456789ab.yaml", "abcdefabcdef.yaml"}
	neither := []string{"auto-tls.yml", "middlewares.yml", "tailscale-peer-machine-b.yaml.tmp", "peer-machine-b.yaml"}

	for _, name := range ours {
		if !isPeerConfigFile(name) {
			t.Errorf("isPeerConfigFile(%q) = false, want true", name)
		}
		if dinghyPattern.MatchString(name) {
			t.Errorf("the per-container pattern matches %q", name)
		}
	}
	for _, name := range theirs {
		if isPeerConfigFile(name) {
			t.Errorf("isPeerConfigFile(%q) = true, want false", name)
		}
	}
	for _, name := range neither {
		if isPeerConfigFile(name) || dinghyPattern.MatchString(name) {
			t.Errorf("%q is claimed by a service that does not own it", name)
		}
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	interval := 10 * time.Second

	if got := backoff(interval, 1); got != interval {
		t.Errorf("backoff after one failure = %s, want %s", got, interval)
	}
	if got := backoff(interval, 3); got != 40*time.Second {
		t.Errorf("backoff after three failures = %s, want 40s", got)
	}
	if got := backoff(interval, 30); got != maxProbeBackoff {
		t.Errorf("backoff after thirty failures = %s, want %s", got, maxProbeBackoff)
	}
}

// Pinned against the service's behaviour rather than its rendering.
func TestFailingMachineIsProbedLessOftenEachTime(t *testing.T) {
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{"http://http-proxy:8080": nil}}
	cfg := &config.TailscaleConfig{
		Enabled:         true,
		Source:          config.TailscaleSourceSocket,
		RefreshInterval: 10 * time.Second,
	}

	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)

	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now := start
	d.now = func() time.Time { return now }

	target := peerURL("100.64.0.2")
	var probedAt []time.Duration
	var waits []time.Duration

	// Two minutes of cycles at the refresh interval's own resolution.
	for tick := range 25 {
		now = start.Add(time.Duration(tick) * 5 * time.Second)

		before := 0
		for _, called := range probe.calls {
			if called == target {
				before++
			}
		}

		report := d.runCycle(t.Context())

		after := 0
		for _, called := range probe.calls {
			if called == target {
				after++
			}
		}
		if after == before {
			continue
		}
		probedAt = append(probedAt, now.Sub(start))

		for _, peer := range report.Peers {
			if peer.Address != "100.64.0.2" {
				continue
			}
			if peer.Status != statusUnreachable {
				t.Fatalf("machine status = %q, want %q", peer.Status, statusUnreachable)
			}
			// The retry the report advertises must be ahead of the cycle that
			// produced it, or it is describing the past.
			if !peer.RetryAt.After(report.UpdatedAt) {
				t.Fatalf("retryAt %s is not after the cycle at %s", peer.RetryAt, report.UpdatedAt)
			}
			waits = append(waits, peer.RetryAt.Sub(report.UpdatedAt))
		}
	}

	if len(probedAt) < 3 {
		t.Fatalf("probed at %v, want the machine tried at least three times in two minutes", probedAt)
	}

	// Skipped rather than probed every cycle, and less often each time.
	for i := 1; i < len(probedAt); i++ {
		gap := probedAt[i] - probedAt[i-1]
		if gap <= cfg.RefreshInterval && i > 1 {
			t.Errorf("probes %d and %d are %s apart, want the gap to keep growing: %v", i-1, i, gap, probedAt)
		}
	}
	for i := 1; i < len(waits); i++ {
		if waits[i] <= waits[i-1] {
			t.Errorf("advertised waits did not grow: %v", waits)
		}
	}
}

// Without the local routing table a cycle writes nothing and keeps what it had.
func TestLocalReadFailureKeepsPreviousRoutes(t *testing.T) {
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{
		"http://http-proxy:8080": nil,
		peerURL("100.64.0.2"):    {hostRoute("app.loc")},
	}}
	cfg := &config.TailscaleConfig{Enabled: true, Source: config.TailscaleSourceSocket}

	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)
	d.runCycle(t.Context())

	written := filepath.Join(cfg.TraefikDynamicDir, "tailscale-peer-machine-b.yaml")
	if _, err := os.Stat(written); err != nil {
		t.Fatalf("expected the first cycle to write %s: %v", written, err)
	}

	probe.errs = map[string]error{"http://http-proxy:8080": fmt.Errorf("connection refused")}
	report := d.runCycle(t.Context())

	if report.LocalError == "" {
		t.Error("LocalError is empty, want the failure reported")
	}
	if _, err := os.Stat(written); err != nil {
		t.Errorf("%s was removed after a local read failure: %v", written, err)
	}
}

// Two machines called the same thing each keep their own file.
func TestMachinesSharingAHostnameKeepSeparateConfigurations(t *testing.T) {
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{
		"http://http-proxy:8080": nil,
		peerURL("100.64.0.10"):   {hostRoute("first.loc")},
		peerURL("100.64.0.20"):   {hostRoute("second.loc")},
	}}
	cfg := &config.TailscaleConfig{Enabled: true, Source: config.TailscaleSourceSocket}

	d := testDiscovery(t, cfg, fakeSource{document: duplicateHostnames}, probe)
	report := d.runCycle(t.Context())

	entries, err := os.ReadDir(cfg.TraefikDynamicDir)
	if err != nil {
		t.Fatalf("failed to read the dynamic directory: %v", err)
	}
	written := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		written[entry.Name()] = struct{}{}
	}
	if len(written) != 2 {
		t.Fatalf("wrote %v, want one file per machine", written)
	}

	hosts := map[string]string{}
	for _, peer := range report.Peers {
		if len(peer.Hosts) == 1 {
			hosts[peer.Address] = peer.Hosts[0]
		}
	}
	if hosts["100.64.0.10"] != "first.loc" || hosts["100.64.0.20"] != "second.loc" {
		t.Errorf("hostnames by address = %v, want each machine to keep its own", hosts)
	}
	if len(report.Collisions) != 0 {
		t.Errorf("Collisions = %+v, want two different machines not to be treated as one", report.Collisions)
	}
}

// The same document produces the same winner on every cycle.
func TestDuplicateHostnamesResolveTheSameWayEveryCycle(t *testing.T) {
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{
		"http://http-proxy:8080": nil,
		peerURL("100.64.0.10"):   {hostRoute("app.loc")},
		peerURL("100.64.0.20"):   {hostRoute("app.loc")},
	}}
	cfg := &config.TailscaleConfig{Enabled: true, Source: config.TailscaleSourceSocket}

	d := testDiscovery(t, cfg, fakeSource{document: duplicateHostnames}, probe)

	var winners []string
	for range 5 {
		report := d.runCycle(t.Context())
		for _, peer := range report.Peers {
			if len(peer.Hosts) > 0 {
				winners = append(winners, peer.Address)
			}
		}
		if len(report.Collisions) != 1 {
			t.Fatalf("Collisions = %+v, want the second claim reported", report.Collisions)
		}
	}

	for _, winner := range winners {
		if winner != winners[0] {
			t.Fatalf("winners = %v, want the same machine on every cycle", winners)
		}
	}
}

// A machine answering without declaring itself contributes nothing.
func TestMachineThatDoesNotDeclareItselfIsNotUsed(t *testing.T) {
	probe := &fakeProbe{
		routes: map[string][]traefikapi.Route{"http://http-proxy:8080": nil},
		errs: map[string]error{
			peerURL("100.64.0.2"): fmt.Errorf("%w: /api/http/middlewares", traefikapi.ErrUndeclared),
		},
	}
	cfg := &config.TailscaleConfig{Enabled: true, Source: config.TailscaleSourceSocket}

	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)
	report := d.runCycle(t.Context())

	for _, peer := range report.Peers {
		if peer.Name != "machine-b" {
			continue
		}
		if peer.Status != statusUndeclared {
			t.Fatalf("machine-b status = %q, want %q", peer.Status, statusUndeclared)
		}
		if len(peer.Hosts) != 0 {
			t.Errorf("machine-b contributed %v, want nothing", peer.Hosts)
		}
	}

	entries, err := os.ReadDir(cfg.TraefikDynamicDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("failed to read the dynamic directory: %v", err)
	}
	for _, entry := range entries {
		if isPeerConfigFile(entry.Name()) {
			t.Errorf("wrote %s for a machine that did not declare itself", entry.Name())
		}
	}
}

// Stopping the service withdraws what it forwarded.
func TestShutdownWithdrawsEveryPeerRoute(t *testing.T) {
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{
		"http://http-proxy:8080": nil,
		peerURL("100.64.0.2"):    {hostRoute("app.loc")},
	}}
	cfg := &config.TailscaleConfig{Enabled: true, Source: config.TailscaleSourceSocket}

	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)
	d.runCycle(t.Context())

	written := filepath.Join(cfg.TraefikDynamicDir, "tailscale-peer-machine-b.yaml")
	if _, err := os.Stat(written); err != nil {
		t.Fatalf("expected the cycle to write %s: %v", written, err)
	}

	if err := d.reconcileConfigs(nil); err != nil {
		t.Fatalf("reconcileConfigs() error = %v", err)
	}
	if _, err := os.Stat(written); !os.IsNotExist(err) {
		t.Errorf("%s survived shutdown, want every forwarded hostname withdrawn", written)
	}
}

// A rule that could not be accepted is reported rather than dropped.
func TestRefusedRulesAreReported(t *testing.T) {
	probe := &fakeProbe{
		routes: map[string][]traefikapi.Route{
			"http://http-proxy:8080": nil,
			peerURL("100.64.0.2"):    {hostRoute("app.loc")},
		},
		rejected: map[string][]string{
			peerURL("100.64.0.2"): {"Host(`peer.loc`) || PathPrefix(`/`)"},
		},
	}
	cfg := &config.TailscaleConfig{Enabled: true, Source: config.TailscaleSourceSocket}

	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)
	report := d.runCycle(t.Context())

	var reported bool
	for _, peer := range report.Peers {
		if peer.Name == "machine-b" && len(peer.RejectedRules) == 1 {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("Peers = %+v, want the refused rule reported", report.Peers)
	}
	if !strings.Contains(report.Render(), "Rules refused for not naming a host") {
		t.Errorf("the rendered report does not mention the refusal:\n%s", report.Render())
	}
}

// A failed write must not withdraw the last good configuration.
func TestAFailedWriteKeepsThePreviousConfiguration(t *testing.T) {
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{
		"http://http-proxy:8080": nil,
		peerURL("100.64.0.2"):    {hostRoute("app.loc")},
	}}
	cfg := &config.TailscaleConfig{Enabled: true, Source: config.TailscaleSourceSocket}

	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)
	d.runCycle(t.Context())

	written := filepath.Join(cfg.TraefikDynamicDir, "tailscale-peer-machine-b.yaml")
	if _, err := os.Stat(written); err != nil {
		t.Fatalf("expected the first cycle to write %s: %v", written, err)
	}

	// A directory where the temporary file has to go makes the write fail
	// without touching the file already there.
	if err := os.Mkdir(written+".tmp", 0o755); err != nil {
		t.Fatalf("failed to block the write: %v", err)
	}

	d.now = func() time.Time { return time.Now().Add(time.Hour) }
	d.runCycle(t.Context())

	if _, err := os.Stat(written); err != nil {
		t.Errorf("%s was removed after a failed write: %v", written, err)
	}
}

// A third machine can normalise onto the second one's disambiguated name.
func TestSlugsStayUniqueWhenTheDisambiguationCollides(t *testing.T) {
	taken := map[string]string{}

	first := uniqueSlug(taken, "id-1", "machine-b", "100.64.0.10")
	second := uniqueSlug(taken, "id-2", "machine-b", "100.64.0.20")
	// A machine literally named after the second one's disambiguated slug.
	third := uniqueSlug(taken, "id-3", "machine-b-100-64-0-20", "100.64.0.30")

	slugs := map[string]bool{first: true, second: true, third: true}
	if len(slugs) != 3 {
		t.Fatalf("slugs = %q, %q, %q, want three distinct names", first, second, third)
	}
	for _, slug := range []string{first, second, third} {
		if slug == "" {
			t.Fatal("a machine was left without a slug")
		}
	}

	// Stable across repeated resolution of the same machines.
	again := map[string]string{}
	if uniqueSlug(again, "id-1", "machine-b", "100.64.0.10") != first {
		t.Error("the first machine's slug is not stable")
	}
}

// A pattern is never equal to a name, so it has to be matched against them.
func TestAPeerWildcardCoveringALocalHostnameLosesToIt(t *testing.T) {
	local := servedLocally("app.loc", "api.loc")
	results := []probeResult{{
		candidate: candidate{id: "nodekey:01", name: "machine-b", address: "100.64.0.2"},
		status:    statusOK,
		routes:    []traefikapi.Route{wildcardRoute(`^.*\.loc$`)},
	}}

	owned, collisions := resolveOwnership(results, local)

	for _, machine := range owned {
		if len(machine.routes) != 0 {
			t.Errorf("%s kept %d routes, want its wildcard suppressed", machine.name, len(machine.routes))
		}
	}
	if len(collisions) != 1 || collisions[0].ServedBy != localOwner {
		t.Fatalf("collisions = %+v, want the wildcard reported as served locally", collisions)
	}
}

// A wildcard covering nothing local is a legitimate route.
func TestAPeerWildcardCoveringNothingLocalIsKept(t *testing.T) {
	local := servedLocally("app.other", "api.other")
	results := []probeResult{{
		candidate: candidate{id: "nodekey:01", name: "machine-b", address: "100.64.0.2"},
		status:    statusOK,
		routes:    []traefikapi.Route{wildcardRoute(`^.*\.loc$`)},
	}}

	owned, collisions := resolveOwnership(results, local)

	if len(owned) != 1 || len(owned[0].routes) != 1 {
		t.Fatalf("owned = %+v, want the wildcard kept", owned)
	}
	if len(collisions) != 0 {
		t.Errorf("collisions = %+v, want none", collisions)
	}
}

// The same in the other direction: a local wildcard covers a peer's plain name.
func TestALocalWildcardSuppressesAPeerHostname(t *testing.T) {
	local := &localRoutes{literals: map[string]struct{}{}, patternText: map[string]struct{}{}}
	pattern, err := compileHostPattern(`^.*\.loc$`)
	if err != nil {
		t.Fatalf("compileHostPattern() error = %v", err)
	}
	local.patterns = append(local.patterns, pattern)
	local.patternText[`^.*\.loc$`] = struct{}{}

	results := []probeResult{{
		candidate: candidate{id: "nodekey:01", name: "machine-b", address: "100.64.0.2"},
		status:    statusOK,
		routes:    []traefikapi.Route{hostRoute("app.loc")},
	}}

	_, collisions := resolveOwnership(results, local)

	if len(collisions) != 1 || collisions[0].Host != "app.loc" {
		t.Fatalf("collisions = %+v, want app.loc reported as served locally", collisions)
	}
}

// A peer-supplied pattern is bounded before it is compiled.
func TestAnAbsurdHostPatternIsRefused(t *testing.T) {
	if _, err := compileHostPattern(strings.Repeat("a", traefikapi.MaxHostPatternLength+1)); err == nil {
		t.Fatal("compileHostPattern() error = nil, want an over-long pattern refused")
	}
	if _, err := compileHostPattern(`^.*\.loc$`); err != nil {
		t.Errorf("compileHostPattern() error = %v, want an ordinary pattern compiled", err)
	}
}

// The service creates it on the path the CLI never touches: a plain compose up.
func TestTheStateDirectoryIsCreatedOwnerOnly(t *testing.T) {
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{
		"http://http-proxy:8080": nil,
		peerURL("100.64.0.2"):    {hostRoute("app.loc")},
	}}
	cfg := &config.TailscaleConfig{Enabled: true, Source: config.TailscaleSourceSocket}

	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)
	if err := os.RemoveAll(filepath.Dir(cfg.StateFile)); err != nil {
		t.Fatalf("failed to clear the state directory: %v", err)
	}

	report := d.runCycle(t.Context())
	if err := d.writeReport(report); err != nil {
		t.Fatalf("writeReport() error = %v", err)
	}

	info, err := os.Stat(filepath.Dir(cfg.StateFile))
	if err != nil {
		t.Fatalf("state directory was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("state directory mode = %o, want 700", perm)
	}

	for _, name := range []string{cfg.StateFile, renderedStateFile(cfg.StateFile)} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatalf("%s was not written: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm&0o044 == 0 {
			t.Errorf("%s mode = %o, want it readable by a user who does not own it", name, perm)
		}
	}
}

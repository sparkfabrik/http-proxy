package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	"github.com/sparkfabrik/http-proxy/pkg/logger"
	"gopkg.in/yaml.v3"
)

func testLayer() *CompatibilityLayer {
	return &CompatibilityLayer{
		logger: logger.New("test"),
		config: &CompatibilityConfig{TraefikDynamicDir: "/tmp"},
		claims: make(map[routeClaim]claimHolder),
	}
}

func inspectWithIP(name, ip string) types.ContainerJSON {
	return types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{Name: name},
		Config:            &container.Config{},
		NetworkSettings: &types.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"default": {IPAddress: ip},
			},
		},
	}
}

func TestParseVirtualHosts(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []virtualHost
	}{
		{"empty", "", nil},
		{"single", "app.loc", []virtualHost{{hostname: "app.loc"}}},
		{"single with port", "app.loc:8080", []virtualHost{{hostname: "app.loc", port: "8080"}}},
		{"multiple", "app.loc,api.loc", []virtualHost{{hostname: "app.loc"}, {hostname: "api.loc"}}},
		{"whitespace trimmed", " app.loc , api.loc ", []virtualHost{{hostname: "app.loc"}, {hostname: "api.loc"}}},
		{"empty entries skipped", "app.loc,,api.loc,", []virtualHost{{hostname: "app.loc"}, {hostname: "api.loc"}}},
		{"non-numeric colon not a port", "app.loc:abc", []virtualHost{{hostname: "app.loc:abc"}}},
		{"out-of-range port not a port", "app.loc:70000", []virtualHost{{hostname: "app.loc:70000"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseVirtualHosts(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("parseVirtualHosts(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("entry %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIsPort(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"80", true},
		{"65535", true},
		{"1", true},
		{"0", false},
		{"65536", false},
		{"-1", false},
		{"abc", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isPort(tt.in); got != tt.want {
			t.Errorf("isPort(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestIsWildcardHost(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"app.loc", false},
		{"*.app.loc", true},
		{"~^api\\..*\\.loc$", true},
	}
	for _, tt := range tests {
		if got := isWildcardHost(tt.in); got != tt.want {
			t.Errorf("isWildcardHost(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestConvertWildcardToRegex(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"wildcard", "*.app.loc", `^.*\.app\.loc$`},
		{"regex passthrough", "~^api\\.loc$", `^api\.loc$`},
		{"plain host escaped", "app.loc", `^app\.loc$`},
		{"too long rejected", string(make([]byte, 254)), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := convertWildcardToRegex(tt.in); got != tt.want {
				t.Errorf("convertWildcardToRegex(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestConvertWildcardToRegexRejectsTooManyWildcards(t *testing.T) {
	if got := convertWildcardToRegex("*.*.*.*.*.*.loc"); got != "" {
		t.Errorf("expected empty result for excessive wildcards, got %q", got)
	}
}

func TestGenerateServiceName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"/my-app", "my-app"},
		{"/my_app", "my-app"},
		{"/My.App", "My-App"},
		{"/a--b", "a-b"},
		{"/-app-", "app"},
		{"/", "service"},
		{"/!@#", "service"},
	}
	for _, tt := range tests {
		if got := generateServiceName(tt.in); got != tt.want {
			t.Errorf("generateServiceName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTCPPortNumber(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"80/tcp", 80},
		{"8080/tcp", 8080},
		{"53/udp", 0},
		{"abc/tcp", 0},
		{"80", 0},
	}
	for _, tt := range tests {
		if got := tcpPortNumber(tt.in); got != tt.want {
			t.Errorf("tcpPortNumber(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestGetEffectivePort(t *testing.T) {
	empty := types.ContainerJSON{Config: &container.Config{}}

	// Host-level port wins over VIRTUAL_PORT.
	if got := getEffectivePort([]virtualHost{{hostname: "a", port: "9000"}}, "8080", empty); got != "9000" {
		t.Errorf("host port should win, got %q", got)
	}
	// VIRTUAL_PORT used when no host port.
	if got := getEffectivePort([]virtualHost{{hostname: "a"}}, "8080", empty); got != "8080" {
		t.Errorf("VIRTUAL_PORT should be used, got %q", got)
	}
	// Falls back to 80 when nothing specified.
	if got := getEffectivePort([]virtualHost{{hostname: "a"}}, "", empty); got != "80" {
		t.Errorf("default should be 80, got %q", got)
	}
}

func TestGetContainerIPDeterministic(t *testing.T) {
	inspect := types.ContainerJSON{
		NetworkSettings: &types.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"zeta":  {IPAddress: "172.0.0.9"},
				"alpha": {IPAddress: "172.0.0.1"},
				"beta":  {IPAddress: "172.0.0.2"},
			},
		},
	}
	// Lowest network name ("alpha") must always win, regardless of map order.
	for i := 0; i < 20; i++ {
		if got := getContainerIP(inspect); got != "172.0.0.1" {
			t.Fatalf("getContainerIP = %q, want 172.0.0.1 (deterministic)", got)
		}
	}
}

func TestGetContainerIPSkipsEmpty(t *testing.T) {
	inspect := types.ContainerJSON{
		NetworkSettings: &types.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"alpha": {IPAddress: ""},
				"beta":  {IPAddress: "172.0.0.2"},
			},
		},
	}
	if got := getContainerIP(inspect); got != "172.0.0.2" {
		t.Errorf("getContainerIP = %q, want 172.0.0.2", got)
	}
}

func TestGetContainerIPNilSettings(t *testing.T) {
	if got := getContainerIP(types.ContainerJSON{}); got != "" {
		t.Errorf("getContainerIP with nil settings = %q, want empty", got)
	}
}

func TestGetDefaultPortLowestExposed(t *testing.T) {
	inspect := types.ContainerJSON{
		Config: &container.Config{
			ExposedPorts: nat.PortSet{
				"8080/tcp": struct{}{},
				"80/tcp":   struct{}{},
				"443/tcp":  struct{}{},
				"53/udp":   struct{}{},
			},
		},
	}
	for i := 0; i < 20; i++ {
		if got := getDefaultPort(inspect); got != "80" {
			t.Fatalf("getDefaultPort = %q, want 80 (lowest exposed TCP)", got)
		}
	}
}

func TestGetDefaultPortFallsBackToBound(t *testing.T) {
	inspect := types.ContainerJSON{
		Config: &container.Config{},
		NetworkSettings: &types.NetworkSettings{
			NetworkSettingsBase: types.NetworkSettingsBase{
				Ports: nat.PortMap{
					"3000/tcp": nil,
					"2000/tcp": nil,
				},
			},
		},
	}
	if got := getDefaultPort(inspect); got != "2000" {
		t.Errorf("getDefaultPort = %q, want 2000 (lowest bound TCP)", got)
	}
}

func TestGetDefaultPortDefault(t *testing.T) {
	if got := getDefaultPort(types.ContainerJSON{Config: &container.Config{}}); got != "80" {
		t.Errorf("getDefaultPort = %q, want 80", got)
	}
}

func TestGenerateTraefikConfigSingleHost(t *testing.T) {
	cl := testLayer()
	inspect := inspectWithIP("/myapp", "172.0.0.5")
	info := ContainerInfo{Name: "myapp", VirtualHost: "myapp.loc", VirtualPort: "8080"}

	cfg := cl.generateTraefikConfig(inspect, info)

	// One HTTP and one HTTPS router for the single host.
	if got := len(cfg.HTTP.Routers); got != 2 {
		t.Fatalf("router count = %d, want 2", got)
	}
	httpRouter, ok := cfg.HTTP.Routers["myapp-0"]
	if !ok {
		t.Fatalf("missing http router myapp-0; got %v", cfg.HTTP.Routers)
	}
	if httpRouter.Rule != "Host(`myapp.loc`)" {
		t.Errorf("http rule = %q, want Host(`myapp.loc`)", httpRouter.Rule)
	}
	tlsRouter, ok := cfg.HTTP.Routers["myapp-tls-0"]
	if !ok {
		t.Fatalf("missing tls router myapp-tls-0")
	}
	if tlsRouter.TLS == nil {
		t.Error("tls router should have TLS config")
	}

	svc, ok := cfg.HTTP.Services["myapp"]
	if !ok {
		t.Fatalf("missing service myapp")
	}
	if got := svc.LoadBalancer.Servers[0].URL; got != "http://172.0.0.5:8080" {
		t.Errorf("server URL = %q, want http://172.0.0.5:8080", got)
	}
}

func TestGenerateTraefikConfigWildcardUsesHostRegexp(t *testing.T) {
	cl := testLayer()
	inspect := inspectWithIP("/wild", "172.0.0.6")
	info := ContainerInfo{Name: "wild", VirtualHost: "*.wild.loc", VirtualPort: "80"}

	cfg := cl.generateTraefikConfig(inspect, info)

	router, ok := cfg.HTTP.Routers["wild-0"]
	if !ok {
		t.Fatalf("missing router wild-0")
	}
	if router.Rule != "HostRegexp(`^.*\\.wild\\.loc$`)" {
		t.Errorf("wildcard rule = %q, want HostRegexp(`^.*\\.wild\\.loc$`)", router.Rule)
	}
}

func TestIsDinghyConfigFile(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"0123456789ab.yaml", true},
		{"abcdef012345.yaml", true},
		{"auto-tls.yml", false},
		{"hsts.yaml", false},
		{"0123456789ab.yml", false},   // wrong extension
		{"0123456789a.yaml", false},   // 11 chars, too short
		{"0123456789abc.yaml", false}, // 13 chars, too long
		{"0123456789AB.yaml", false},  // uppercase not produced by FormatDockerID
		{"0123456789ag.yaml", false},  // non-hex character
		{"0123456789ab.yaml.tmp", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isDinghyConfigFile(tt.in); got != tt.want {
			t.Errorf("isDinghyConfigFile(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestReconcileConfigsRemovesOnlyOrphans(t *testing.T) {
	dir := t.TempDir()

	// A recreated container leaves both its current file (kept) and a stale one
	// (orphaned). The shared directory also holds files this service must never
	// touch: the entrypoint's auto-tls.yml, a certificate config, and the
	// middlewares subdirectory.
	orphan := "aaaaaaaaaaaa.yaml"
	current := "bbbbbbbbbbbb.yaml"
	files := []string{orphan, current, "auto-tls.yml", "custom-cert.yaml"}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("http: {}\n"), 0o644); err != nil {
			t.Fatalf("failed to seed %s: %v", name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "middlewares"), 0o755); err != nil {
		t.Fatalf("failed to seed middlewares dir: %v", err)
	}

	cl := &CompatibilityLayer{
		logger: logger.New("test"),
		config: &CompatibilityConfig{TraefikDynamicDir: dir},
	}

	keep := map[string]struct{}{current: {}}
	if err := cl.reconcileConfigs(keep); err != nil {
		t.Fatalf("reconcileConfigs returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, orphan)); !os.IsNotExist(err) {
		t.Errorf("orphaned config %s should have been removed", orphan)
	}
	survivors := []string{current, "auto-tls.yml", "custom-cert.yaml", "middlewares"}
	for _, name := range survivors {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s should have survived reconciliation: %v", name, err)
		}
	}
}

func TestReconcileConfigsDryRunKeepsFiles(t *testing.T) {
	dir := t.TempDir()
	orphan := "cccccccccccc.yaml"
	if err := os.WriteFile(filepath.Join(dir, orphan), []byte("http: {}\n"), 0o644); err != nil {
		t.Fatalf("failed to seed %s: %v", orphan, err)
	}

	cl := &CompatibilityLayer{
		logger: logger.New("test"),
		config: &CompatibilityConfig{TraefikDynamicDir: dir, DryRun: true},
	}

	if err := cl.reconcileConfigs(map[string]struct{}{}); err != nil {
		t.Fatalf("reconcileConfigs returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, orphan)); err != nil {
		t.Errorf("dry run must not remove %s: %v", orphan, err)
	}
}

func TestReconcileConfigsMissingDir(t *testing.T) {
	cl := &CompatibilityLayer{
		logger: logger.New("test"),
		config: &CompatibilityConfig{TraefikDynamicDir: filepath.Join(t.TempDir(), "does-not-exist")},
	}
	if err := cl.reconcileConfigs(map[string]struct{}{}); err != nil {
		t.Errorf("reconcileConfigs on missing dir should be a no-op, got %v", err)
	}
}

func TestGenerateTraefikConfigMultiHost(t *testing.T) {
	cl := testLayer()
	inspect := inspectWithIP("/multi", "172.0.0.7")
	info := ContainerInfo{Name: "multi", VirtualHost: "a.loc,b.loc", VirtualPort: "80"}

	cfg := cl.generateTraefikConfig(inspect, info)

	// Two hosts => two HTTP + two HTTPS routers, single shared service.
	if got := len(cfg.HTTP.Routers); got != 4 {
		t.Errorf("router count = %d, want 4", got)
	}
	if got := len(cfg.HTTP.Services); got != 1 {
		t.Errorf("service count = %d, want 1", got)
	}
}

func TestNormalizeVirtualPath(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"absent", "", "", false},
		{"simple", "/api", "/api", false},
		{"trailing separator is stripped", "/api/", "/api", false},
		{"nested", "/api/v1", "/api/v1", false},
		{"surrounding space is ignored", "  /api  ", "/api", false},
		{"root is the host itself", "/", "", false},
		{"repeated separators only", "///", "", false},
		{"no leading separator", "api", "", true},
		{"a list is not a path", "/api,/admin", "", true},
		{"query string", "/api?x=1", "", true},
		{"fragment", "/api#top", "", true},
		{"embedded space", "/api v1", "", true},
		{"empty segment", "/api//v1", "", true},
		{"backtick closes the matcher", "/api`) || Host(`evil.loc", "", true},
		{"double quote", "/api\"", "", true},
		{"single quote", "/api'", "", true},
		{"backslash", "/api\\x", "", true},
		{"dollar", "/api$x", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeVirtualPath(tt.input)
			if tt.wantErr && err == nil {
				t.Fatalf("normalizeVirtualPath(%q) = %q, want an error", tt.input, got)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("normalizeVirtualPath(%q) returned %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("normalizeVirtualPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// A rejected path must not take the container's host routes down with it.
func TestGenerateTraefikConfigRejectedPathKeepsHostRoutes(t *testing.T) {
	cl := testLayer()
	inspect := inspectWithIP("/bad", "172.0.0.9")
	info := ContainerInfo{Name: "bad", VirtualHost: "bad.loc", VirtualPort: "80", VirtualPath: "api"}

	cfg := cl.generateTraefikConfig(inspect, info)

	router, ok := cfg.HTTP.Routers["bad-0"]
	if !ok {
		t.Fatalf("missing router bad-0; got %v", cfg.HTTP.Routers)
	}
	if router.Rule != "Host(`bad.loc`)" {
		t.Errorf("rule = %q, want the plain host rule", router.Rule)
	}
	if router.Priority != 0 {
		t.Errorf("priority = %d, want 0 for a container with no usable path", router.Priority)
	}
}

func TestGenerateTraefikConfigMountedPath(t *testing.T) {
	cl := testLayer()
	inspect := inspectWithIP("/api", "172.0.0.7")
	info := ContainerInfo{Name: "api", VirtualHost: "app.loc", VirtualPort: "3000", VirtualPath: "/api"}

	cfg := cl.generateTraefikConfig(inspect, info)

	wantRule := "Host(`app.loc`) && (PathPrefix(`/api/`) || Path(`/api`))"
	wantPriority := pathPriorityBase + len("/api")

	for _, name := range []string{"api-0", "api-tls-0"} {
		router, ok := cfg.HTTP.Routers[name]
		if !ok {
			t.Fatalf("missing router %s; got %v", name, cfg.HTTP.Routers)
		}
		if router.Rule != wantRule {
			t.Errorf("%s rule = %q, want %q", name, router.Rule, wantRule)
		}
		// Both schemes must rank together, or a request could reach different
		// containers over http and https.
		if router.Priority != wantPriority {
			t.Errorf("%s priority = %d, want %d", name, router.Priority, wantPriority)
		}
	}
}

// A container without VIRTUAL_PATH must emit no priority at all, so its
// ordering against every other router on the machine is the one it always had.
func TestGenerateTraefikConfigHostOnlyEmitsNoPriority(t *testing.T) {
	cl := testLayer()
	inspect := inspectWithIP("/plain", "172.0.0.8")
	info := ContainerInfo{Name: "plain", VirtualHost: "plain.loc", VirtualPort: "80"}

	cfg := cl.generateTraefikConfig(inspect, info)

	for name, router := range cfg.HTTP.Routers {
		if router.Priority != 0 {
			t.Errorf("%s priority = %d, want 0", name, router.Priority)
		}
	}

	// The zero must also disappear from the emitted YAML. Traefik reads a
	// missing priority as "rank by rule length"; a literal 0 would mean the
	// same thing, but the field is only ever meaningful when it is non-zero,
	// and serialising it would misrepresent an untouched router as configured.
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "priority") {
		t.Errorf("host-only config mentions priority:\n%s", out)
	}
}

// The priority has to survive marshalling: with omitempty a zero would vanish
// and silently restore rule-length ordering.
func TestMountedPathPriorityIsSerialised(t *testing.T) {
	cl := testLayer()
	inspect := inspectWithIP("/api", "172.0.0.7")
	info := ContainerInfo{Name: "api", VirtualHost: "app.loc", VirtualPort: "3000", VirtualPath: "/api"}

	out, err := yaml.Marshal(cl.generateTraefikConfig(inspect, info))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := fmt.Sprintf("priority: %d", pathPriorityBase+len("/api"))
	if !strings.Contains(string(out), want) {
		t.Errorf("emitted config is missing %q:\n%s", want, out)
	}
}

// The ordering the feature depends on: a mounted path must outrank the host it
// sits on and any wildcard covering that host, and a longer path must outrank a
// shorter one. Host rules carry no priority, so Traefik ranks them by rule
// length, which is bounded well below the base.
func TestPathPriorityOrdering(t *testing.T) {
	host := len("Host(`app.loc`)")
	wildcard := len("HostRegexp(`^.*\\.a\\.very\\.long\\.example\\.domain\\.loc$`)")

	short := pathPriority("/api")
	long := pathPriority("/api/internal")

	if short <= host {
		t.Errorf("mounted path priority %d does not outrank the host rule length %d", short, host)
	}
	if short <= wildcard {
		t.Errorf("mounted path priority %d does not outrank the wildcard rule length %d", short, wildcard)
	}
	if long <= short {
		t.Errorf("longer path priority %d does not outrank shorter path priority %d", long, short)
	}
}

// VIRTUAL_PATH belongs to the container, so every host it names is mounted.
func TestGenerateTraefikConfigMountedPathAcrossHosts(t *testing.T) {
	cl := testLayer()
	inspect := inspectWithIP("/api", "172.0.0.10")
	info := ContainerInfo{Name: "api", VirtualHost: "a.loc,b.loc", VirtualPort: "3000", VirtualPath: "/api"}

	cfg := cl.generateTraefikConfig(inspect, info)

	if got := len(cfg.HTTP.Routers); got != 4 {
		t.Fatalf("router count = %d, want 4", got)
	}
	for i, host := range []string{"a.loc", "b.loc"} {
		name := fmt.Sprintf("api-%d", i)
		router, ok := cfg.HTTP.Routers[name]
		if !ok {
			t.Fatalf("missing router %s; got %v", name, cfg.HTTP.Routers)
		}
		want := fmt.Sprintf("Host(`%s`) && (PathPrefix(`/api/`) || Path(`/api`))", host)
		if router.Rule != want {
			t.Errorf("%s rule = %q, want %q", name, router.Rule, want)
		}
	}
}

// A wildcard host keeps its meaning when a path is mounted on it.
func TestGenerateTraefikConfigMountedPathOnWildcard(t *testing.T) {
	cl := testLayer()
	inspect := inspectWithIP("/api", "172.0.0.11")
	info := ContainerInfo{Name: "api", VirtualHost: "*.app.loc", VirtualPort: "3000", VirtualPath: "/api"}

	cfg := cl.generateTraefikConfig(inspect, info)

	router, ok := cfg.HTTP.Routers["api-0"]
	if !ok {
		t.Fatalf("missing router api-0; got %v", cfg.HTTP.Routers)
	}
	if !strings.HasPrefix(router.Rule, "HostRegexp(`") {
		t.Errorf("rule = %q, want a HostRegexp matcher", router.Rule)
	}
	if !strings.HasSuffix(router.Rule, "&& (PathPrefix(`/api/`) || Path(`/api`))") {
		t.Errorf("rule = %q, want the path matcher appended", router.Rule)
	}
}

func TestRecordClaimsDetectsDuplicates(t *testing.T) {
	cl := testLayer()
	hosts := []virtualHost{{hostname: "app.loc"}}

	first := ContainerInfo{ID: "aaa", Name: "first"}
	second := ContainerInfo{ID: "bbb", Name: "second"}

	cl.recordClaims(first, hosts, "/api")
	if got := cl.claims[routeClaim{host: "app.loc", path: "/api"}].name; got != "first" {
		t.Fatalf("claim holder = %q, want first", got)
	}

	// The second container must not take the claim from the first: reporting
	// the collision is the point, and the holder is what the report names.
	cl.recordClaims(second, hosts, "/api")
	if got := cl.claims[routeClaim{host: "app.loc", path: "/api"}].name; got != "first" {
		t.Errorf("claim holder = %q, want it to stay with first", got)
	}

	// A different path on the same host is not a collision.
	cl.recordClaims(second, hosts, "/admin")
	if got := cl.claims[routeClaim{host: "app.loc", path: "/admin"}].name; got != "second" {
		t.Errorf("claim holder for /admin = %q, want second", got)
	}
}

// A container replacing another on the same route must not report a collision
// against the container it replaced.
func TestReleaseClaimsFreesTheRoute(t *testing.T) {
	cl := testLayer()
	hosts := []virtualHost{{hostname: "app.loc"}}

	cl.recordClaims(ContainerInfo{ID: "aaa", Name: "first"}, hosts, "/api")
	cl.releaseClaims("aaa")

	if _, held := cl.claims[routeClaim{host: "app.loc", path: "/api"}]; held {
		t.Fatal("route is still claimed after the holder was released")
	}

	cl.recordClaims(ContainerInfo{ID: "bbb", Name: "second"}, hosts, "/api")
	if got := cl.claims[routeClaim{host: "app.loc", path: "/api"}].name; got != "second" {
		t.Errorf("claim holder = %q, want second", got)
	}
}

// Two containers claiming a bare host collide on the same key as two claiming
// one path, so the existing single-host case is covered by the same mechanism.
func TestRecordClaimsCoversBareHosts(t *testing.T) {
	cl := testLayer()
	hosts := []virtualHost{{hostname: "app.loc"}}

	cl.recordClaims(ContainerInfo{ID: "aaa", Name: "first"}, hosts, "")
	cl.recordClaims(ContainerInfo{ID: "bbb", Name: "second"}, hosts, "")

	if got := cl.claims[routeClaim{host: "app.loc"}].name; got != "first" {
		t.Errorf("claim holder = %q, want it to stay with first", got)
	}
}

func TestContainerDirectoryPrefersTheComposeWorkingDir(t *testing.T) {
	inspect := types.ContainerJSON{
		Config: &container.Config{Labels: map[string]string{
			"com.docker.compose.project.working_dir": "/home/dev/project",
		}},
		Mounts: []types.MountPoint{{Type: "bind", Source: "/somewhere/else"}},
	}

	if got := containerDirectory(inspect); got != "/home/dev/project" {
		t.Errorf("expected the compose working directory, got %q", got)
	}
}

func TestContainerDirectoryFallsBackToTheFirstBindMount(t *testing.T) {
	inspect := types.ContainerJSON{
		Config: &container.Config{Labels: map[string]string{}},
		Mounts: []types.MountPoint{
			{Type: "volume", Source: "a-named-volume"},
			{Type: "bind", Source: "/home/dev/run-without-compose"},
			{Type: "bind", Source: "/later/one"},
		},
	}

	if got := containerDirectory(inspect); got != "/home/dev/run-without-compose" {
		t.Errorf("expected the first bind mount, got %q", got)
	}
}

func TestContainerDirectoryIsEmptyWhenThereIsNothingToShow(t *testing.T) {
	inspect := types.ContainerJSON{
		Config: &container.Config{Labels: map[string]string{}},
		Mounts: []types.MountPoint{{Type: "volume", Source: "a-named-volume"}},
	}

	if got := containerDirectory(inspect); got != "" {
		t.Errorf("expected no directory, got %q", got)
	}
}

func TestRecordHostsWritesOneRowPerHostname(t *testing.T) {
	cl := NewCompatibilityLayer(&CompatibilityConfig{TraefikDynamicDir: t.TempDir()})
	info := ContainerInfo{
		Name:        "app-1",
		VirtualHost: "one.spark.loc,two.spark.loc:8080",
		Directory:   "/home/dev/app",
	}
	cl.recordHosts("abc123", info, "virtual-host", virtualHostNames(info.VirtualHost))

	rows := cl.hosts["abc123"]
	if len(rows) != 2 {
		t.Fatalf("expected a row per hostname, got %d: %+v", len(rows), rows)
	}
	if rows[0].hostname != "one.spark.loc" || rows[1].hostname != "two.spark.loc" {
		t.Errorf("hostnames were not split as the router parses them: %+v", rows)
	}
	if rows[1].directory != "/home/dev/app" || rows[1].routing != "virtual-host" {
		t.Errorf("a row lost its directory or routing: %+v", rows[1])
	}
}

func TestRecordHostsForgetsAContainerWithNoHostnames(t *testing.T) {
	cl := NewCompatibilityLayer(&CompatibilityConfig{TraefikDynamicDir: t.TempDir()})
	cl.recordHosts("abc123", ContainerInfo{Name: "app-1"}, "virtual-host", []string{"one.spark.loc"})
	cl.recordHosts("abc123", ContainerInfo{Name: "app-1"}, "traefik-labels", nil)

	if _, ok := cl.hosts["abc123"]; ok {
		t.Errorf("a container serving no hostname is still recorded: %+v", cl.hosts)
	}
}

func TestWriteHostsFileIsSortedAndTabSeparated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.tsv")
	cl := NewCompatibilityLayer(&CompatibilityConfig{TraefikDynamicDir: dir, HostsStateFile: path})

	cl.recordHosts("b", ContainerInfo{Name: "zeta", Directory: "/z"}, "virtual-host", []string{"zeta.spark.loc"})
	cl.recordHosts("a", ContainerInfo{Name: "alpha"}, "traefik-labels", []string{"alpha.spark.loc"})

	if err := cl.writeHostsFile(); err != nil {
		t.Fatalf("writing the hosts file failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading it back failed: %v", err)
	}
	want := "alpha.spark.loc\talpha\t\ttraefik-labels\nzeta.spark.loc\tzeta\t/z\tvirtual-host\n"
	if string(data) != want {
		t.Errorf("hosts file is not what the CLI parses:\n got %q\nwant %q", data, want)
	}
}

func TestWriteHostsFileLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	cl := NewCompatibilityLayer(&CompatibilityConfig{TraefikDynamicDir: dir, HostsStateFile: filepath.Join(dir, "hosts.tsv")})
	cl.recordHosts("a", ContainerInfo{Name: "alpha"}, "virtual-host", []string{"alpha.spark.loc"})

	if err := cl.writeHostsFile(); err != nil {
		t.Fatalf("writing failed: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

func TestWriteHostsFileAlsoPublishesParseableJSON(t *testing.T) {
	dir := t.TempDir()
	cl := NewCompatibilityLayer(&CompatibilityConfig{
		TraefikDynamicDir: dir,
		HostsStateFile:    filepath.Join(dir, "hosts.tsv"),
		HostsJSONFile:     filepath.Join(dir, "hosts.json"),
	})
	cl.recordHosts("a", ContainerInfo{Name: "app-1", Directory: "/home/dev/app"}, "virtual-host", []string{"one.spark.loc"})
	cl.recordHosts("b", ContainerInfo{Name: "other-1"}, "traefik-labels", []string{"two.spark.loc"})

	if err := cl.writeHostsFile(); err != nil {
		t.Fatalf("writing failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "hosts.json"))
	if err != nil {
		t.Fatalf("reading the json back failed: %v", err)
	}

	var entries []hostEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("hosts --json would not parse: %v\n%s", err, data)
	}
	if len(entries) != 2 {
		t.Fatalf("expected two entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].Hostname != "one.spark.loc" || entries[0].Directory != "/home/dev/app" {
		t.Errorf("the first entry lost a field: %+v", entries[0])
	}
	if entries[1].Directory != "" {
		t.Errorf("a container with no directory should omit it, got %q", entries[1].Directory)
	}
	if entries[1].Routing != "traefik-labels" {
		t.Errorf("routing was not published: %+v", entries[1])
	}
}

func TestLabelHostnamesReadsTheRouterRules(t *testing.T) {
	hosts := labelHostnames(map[string]string{
		"traefik.http.routers.app.rule":       "Host(`app.spark.loc`) || Host(`www.spark.loc`)",
		"traefik.http.routers.api.rule":       "Host(\"api.spark.loc\") && PathPrefix(`/v1`)",
		"traefik.http.routers.app.entrypoint": "websecure",
		"com.docker.compose.project":          "not-a-rule",
	})

	want := []string{"api.spark.loc", "app.spark.loc", "www.spark.loc"}
	if len(hosts) != len(want) {
		t.Fatalf("expected %v, got %v", want, hosts)
	}
	for i := range want {
		if hosts[i] != want[i] {
			t.Errorf("expected %v, got %v", want, hosts)
			break
		}
	}
}

func TestLabelHostnamesDeduplicates(t *testing.T) {
	hosts := labelHostnames(map[string]string{
		"traefik.http.routers.a.rule": "Host(`same.spark.loc`)",
		"traefik.http.routers.b.rule": "Host(`same.spark.loc`)",
	})

	if len(hosts) != 1 {
		t.Errorf("a hostname claimed by two routers should appear once, got %v", hosts)
	}
}

func TestLabelHostnamesTakesEveryHostnameInAMatcher(t *testing.T) {
	cases := []struct {
		name string
		rule string
		want []string
	}{
		{"one hostname", "Host(`a.spark.loc`)", []string{"a.spark.loc"}},
		{"two, spaced", "Host(`a.spark.loc`, `b.spark.loc`)", []string{"a.spark.loc", "b.spark.loc"}},
		{"two, unspaced", "Host(`a.spark.loc`,`b.spark.loc`)", []string{"a.spark.loc", "b.spark.loc"}},
		{"double quoted", `Host("a.spark.loc", "b.spark.loc")`, []string{"a.spark.loc", "b.spark.loc"}},
		{"with another matcher", "Host(`a.spark.loc`) && PathPrefix(`/api`)", []string{"a.spark.loc"}},
		{"two matchers", "Host(`a.spark.loc`) || Host(`b.spark.loc`)", []string{"a.spark.loc", "b.spark.loc"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := labelHostnames(map[string]string{"traefik.http.routers.app.rule": c.rule})
			if !slices.Equal(got, c.want) {
				t.Errorf("rule %s: got %v, want %v", c.rule, got, c.want)
			}
		})
	}
}

// Asserts recordHosts's half of the contract only. That the caller always calls
// it, so disabling a container clears its rows, is not covered: the call site is
// in processContainer, which needs a Docker client this package cannot fake.
func TestRecordingNothingClearsAContainersRows(t *testing.T) {
	cl := NewCompatibilityLayer(&CompatibilityConfig{TraefikDynamicDir: t.TempDir()})
	info := ContainerInfo{Name: "app-1"}

	cl.recordHosts("abc", info, "traefik-labels", []string{"a.spark.loc"})
	if len(cl.hosts["abc"]) != 1 {
		t.Fatalf("expected the row to be recorded first, got %v", cl.hosts["abc"])
	}

	// What the caller passes when the container is no longer exposed.
	cl.recordHosts("abc", info, "traefik-labels", nil)
	if rows, ok := cl.hosts["abc"]; ok {
		t.Errorf("rows survived the container being disabled: %v", rows)
	}
}

func TestAHostnameThatSanitizesToNothingIsNotARow(t *testing.T) {
	cl := NewCompatibilityLayer(&CompatibilityConfig{TraefikDynamicDir: t.TempDir()})

	cl.recordHosts("abc", ContainerInfo{Name: "app-1"}, "traefik-labels",
		[]string{"\x01\x02", "\t", "real.spark.loc"})

	rows := cl.hosts["abc"]
	if len(rows) != 1 || rows[0].hostname != "real.spark.loc" {
		t.Errorf("expected only the real hostname, got %v", rows)
	}
}

func TestSanitizeFieldStripsC1Controls(t *testing.T) {
	// U+009B is a control-sequence introducer, so a terminal acts on it.
	if got := sanitizeField("a\u009b31mb"); got != "a31mb" {
		t.Errorf("C1 control survived: %q", got)
	}
}

func TestRecordedFieldsCarryNoControlCharacters(t *testing.T) {
	cl := NewCompatibilityLayer(&CompatibilityConfig{TraefikDynamicDir: t.TempDir()})
	info := ContainerInfo{
		Name:      "app\t1",
		Directory: "/home/dev/a\x1b[31mred\nb",
	}

	cl.recordHosts("abc", info, "virtual-host", []string{"a.loc\tb.loc"})

	rows := cl.hosts["abc"]
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %d", len(rows))
	}
	for name, got := range map[string]string{
		"hostname":  rows[0].hostname,
		"container": rows[0].container,
		"directory": rows[0].directory,
	} {
		if strings.ContainsAny(got, "\t\n\r\x1b") {
			t.Errorf("%s still carries a control character: %q", name, got)
		}
	}
	if rows[0].container != "app 1" {
		t.Errorf("a tab should become a space, got %q", rows[0].container)
	}
}

func TestOnlyAnExposedContainerCountsAsServed(t *testing.T) {
	// Traefik runs exposedByDefault false, so a container that did not opt in is
	// not served and must not be reported as serving its rules.
	for _, c := range []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"opted in", map[string]string{"traefik.enable": "true"}, true},
		{"capitalised", map[string]string{"traefik.enable": "True"}, true},
		{"upper case", map[string]string{"traefik.enable": "TRUE"}, true},
		{"one", map[string]string{"traefik.enable": "1"}, true},
		{"t", map[string]string{"traefik.enable": "t"}, true},
		{"opted out", map[string]string{"traefik.enable": "false"}, false},
		{"zero", map[string]string{"traefik.enable": "0"}, false},
		{"not a boolean", map[string]string{"traefik.enable": "yes"}, false},
		{"no enable label", map[string]string{"traefik.http.routers.app.rule": "Host(`a.loc`)"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := traefikExposed(c.labels); got != c.want {
				t.Errorf("traefikExposed(%v) = %v, want %v", c.labels, got, c.want)
			}
		})
	}
}

func TestLabelHostnamesIsEmptyWithoutARule(t *testing.T) {
	if hosts := labelHostnames(map[string]string{"traefik.enable": "true"}); len(hosts) != 0 {
		t.Errorf("expected no hostnames, got %v", hosts)
	}
}

func TestLabelRoutedContainerIsRecordedFromItsRules(t *testing.T) {
	cl := NewCompatibilityLayer(&CompatibilityConfig{TraefikDynamicDir: t.TempDir()})
	labels := map[string]string{"traefik.http.routers.app.rule": "Host(`labelled.spark.loc`)"}

	// A label-routed container usually carries no VIRTUAL_HOST at all.
	cl.recordHosts("abc", ContainerInfo{Name: "app-1", Directory: "/home/dev/app"}, "traefik-labels", labelHostnames(labels))

	rows := cl.hosts["abc"]
	if len(rows) != 1 || rows[0].hostname != "labelled.spark.loc" {
		t.Fatalf("a label-routed container contributed nothing usable: %+v", rows)
	}
	if rows[0].routing != "traefik-labels" {
		t.Errorf("routing was not recorded: %+v", rows[0])
	}
}

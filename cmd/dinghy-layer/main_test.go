package main

import (
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	"github.com/sparkfabrik/http-proxy/pkg/logger"
)

func testLayer() *CompatibilityLayer {
	return &CompatibilityLayer{
		logger: logger.New("test"),
		config: &CompatibilityConfig{TraefikDynamicDir: "/tmp"},
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

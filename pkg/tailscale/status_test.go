package tailscale

import (
	"reflect"
	"strings"
	"testing"
)

const tailnetStatus = `{
  "Self": {"HostName": "machine-a", "Online": true, "UserID": 1, "TailscaleIPs": ["100.64.0.1", "fd7a::1"]},
  "Peer": {
    "nodekey:01": {"HostName": "machine-b", "Online": true, "UserID": 1, "TailscaleIPs": ["100.64.0.2", "fd7a::2"]},
    "nodekey:02": {"HostName": "machine-c", "Online": false, "UserID": 1, "TailscaleIPs": ["100.64.0.3"]},
    "nodekey:03": {"HostName": "machine-d", "Online": true, "UserID": 2, "TailscaleIPs": ["100.64.0.4"]},
    "nodekey:04": {"HostName": "machine-a", "Online": true, "UserID": 1, "TailscaleIPs": ["100.64.0.5"]},
    "nodekey:05": {"HostName": "machine-e", "Online": true, "UserID": 1, "TailscaleIPs": []},
    "nodekey:06": {"HostName": "machine-f", "Online": true, "UserID": 1, "TailscaleIPs": ["100.64.0.6"]},
    "nodekey:07": {"HostName": "self-mirror", "Online": true, "UserID": 1, "TailscaleIPs": ["100.64.0.1", "fd7a::1"]}
  }
}`

func parseTestStatus(t *testing.T, document string) *Status {
	t.Helper()
	status, err := ParseStatus(strings.NewReader(document))
	if err != nil {
		t.Fatalf("ParseStatus() error = %v", err)
	}
	return status
}

func TestPeersClassifiesAndSorts(t *testing.T) {
	got := parseTestStatus(t, tailnetStatus).Peers()

	want := []Peer{
		{ID: "nodekey:04", Name: "machine-a", Address: "100.64.0.5"},
		{ID: "nodekey:01", Name: "machine-b", Address: "100.64.0.2"},
		{ID: "nodekey:02", Name: "machine-c", Address: "100.64.0.3", SkipReason: SkipOffline},
		{ID: "nodekey:03", Name: "machine-d", Address: "100.64.0.4", SkipReason: SkipOtherUser},
		{ID: "nodekey:05", Name: "machine-e", SkipReason: SkipNoAddress},
		{ID: "nodekey:06", Name: "machine-f", Address: "100.64.0.6"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Peers() = %+v, want %+v", got, want)
	}
}

// TestAnotherUserIsExcludedWhateverIsConfigured guards the trust boundary of
// peer routing. The filter is deliberately out of reach of configuration: this
// package reads no environment, so setting everything the service understands
// changes nothing here. A future setting that reached this filter would fail
// this test rather than quietly widening who may be probed.
func TestAnotherUserIsExcludedWhateverIsConfigured(t *testing.T) {
	for _, key := range []string{
		"HTTP_PROXY_TAILSCALE_ENABLED",
		"HTTP_PROXY_TAILSCALE_SOURCE",
		"HTTP_PROXY_TAILSCALE_ADDRESSES",
		"HTTP_PROXY_TAILSCALE_SOCKET",
		"HTTP_PROXY_TAILSCALE_STATUS_FILE",
		"HTTP_PROXY_TAILSCALE_REFRESH_INTERVAL",
		"HTTP_PROXY_TAILSCALE_STATE_FILE",
	} {
		t.Setenv(key, "all")
	}

	for _, peer := range parseTestStatus(t, tailnetStatus).Peers() {
		if peer.Name != "machine-d" {
			continue
		}
		if peer.SkipReason != SkipOtherUser {
			t.Fatalf("machine-d skip reason = %q, want %q", peer.SkipReason, SkipOtherUser)
		}
		if peer.Probeable() {
			t.Fatal("machine-d is probeable, want another user's machine never probed")
		}
	}
}

// Tailscale does not make hostnames unique, so a machine sharing this one's
// hostname is a real machine with its own routes, and only an address it holds
// identifies this machine itself.
func TestSelfIsExcludedByAddressNotByHostname(t *testing.T) {
	peers := parseTestStatus(t, tailnetStatus).Peers()

	var sawSameName, sawSelfMirror bool
	for _, peer := range peers {
		if peer.Name == "machine-a" {
			sawSameName = true
		}
		if peer.Name == "self-mirror" {
			sawSelfMirror = true
		}
	}
	if !sawSameName {
		t.Error("a machine sharing this machine's hostname was dropped, want it kept as a peer")
	}
	if sawSelfMirror {
		t.Error("a node holding this machine's own address was kept, want this machine excluded")
	}
}

// Two machines with one hostname keep separate identities, and their order is
// fixed rather than depending on how the document decoded.
func TestDuplicateHostnamesKeepTheirIdentities(t *testing.T) {
	const document = `{
  "Self": {"HostName": "machine-a", "Online": true, "UserID": 1, "TailscaleIPs": ["100.64.0.1"]},
  "Peer": {
    "nodekey:20": {"HostName": "machine-b", "Online": true, "UserID": 1, "TailscaleIPs": ["100.64.0.20"]},
    "nodekey:10": {"HostName": "machine-b", "Online": true, "UserID": 1, "TailscaleIPs": ["100.64.0.10"]}
  }
}`

	first := parseTestStatus(t, document).Peers()
	for range 20 {
		if got := parseTestStatus(t, document).Peers(); !reflect.DeepEqual(got, first) {
			t.Fatalf("Peers() = %+v, want the stable order %+v", got, first)
		}
	}

	want := []Peer{
		{ID: "nodekey:10", Name: "machine-b", Address: "100.64.0.10"},
		{ID: "nodekey:20", Name: "machine-b", Address: "100.64.0.20"},
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("Peers() = %+v, want %+v", first, want)
	}
}

func TestParseStatusRejects(t *testing.T) {
	tests := []struct {
		name     string
		document string
	}{
		{"not json", "not json"},
		{"no self node", `{"Peer": {}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseStatus(strings.NewReader(tt.document)); err == nil {
				t.Fatal("ParseStatus() error = nil, want an error")
			}
		})
	}
}

func TestFirstIPv4(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{"none", nil, ""},
		{"ipv6 only", []string{"fd7a::1"}, ""},
		{"ipv4 after ipv6", []string{"fd7a::1", "100.64.0.2"}, "100.64.0.2"},
		{"garbage skipped", []string{"nonsense", "100.64.0.2"}, "100.64.0.2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstIPv4(tt.in); got != tt.want {
				t.Errorf("firstIPv4(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

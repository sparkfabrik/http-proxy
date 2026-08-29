package traefikapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

const routersBody = `[
  {"name": "b-app-0@file", "provider": "file", "rule": "Host(` + "`b.loc`" + `)", "priority": 49, "entryPoints": ["http"], "status": "enabled"},
  {"name": "a-app-0@file", "provider": "file", "rule": "Host(` + "`a.loc`" + `)", "priority": 49, "entryPoints": ["http"], "status": "enabled"}
]`

func TestRoutersSortsByName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != RoutersPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(routersBody))
	}))
	defer server.Close()

	routers, err := New(server.Client(), server.URL).Routers(t.Context())
	if err != nil {
		t.Fatalf("Routers() error = %v", err)
	}
	if len(routers) != 2 || routers[0].Name != "a-app-0@file" || routers[1].Name != "b-app-0@file" {
		t.Fatalf("Routers() = %+v, want a-app-0@file before b-app-0@file", routers)
	}
}

func TestRoutersErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"not a proxy", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }},
		{"body is not json", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			if _, err := New(server.Client(), server.URL).Routers(t.Context()); err == nil {
				t.Fatal("Routers() error = nil, want an error")
			}
		})
	}
}

func TestRoutes(t *testing.T) {
	routers := []Router{
		{Name: "app-0@file", Provider: "file", Rule: "Host(`app.loc`)", Priority: 49, EntryPoints: []string{"http"}, Status: "enabled"},
		{Name: "app-tls-0@file", Provider: "file", Rule: "Host(`app.loc`)", Priority: 49, EntryPoints: []string{"https"}, Status: "enabled"},
		{Name: "api-0@file", Provider: "file", Rule: "Host(`app.loc`) && (PathPrefix(`/api/`) || Path(`/api`))", Priority: 10004, EntryPoints: []string{"http"}, Status: "enabled"},
		{Name: "dashboard@internal", Provider: "internal", Rule: "PathPrefix(`/api`)", Priority: 9, EntryPoints: []string{"http"}, Status: "enabled"},
		{Name: "tailscale-peer-machine-b-0@file", Provider: "file", Rule: "Host(`remote.loc`)", Priority: 49, EntryPoints: []string{"http"}, Status: "enabled"},
		{Name: "broken-0@file", Provider: "file", Rule: "Host(`nope.loc`)", Priority: 49, EntryPoints: []string{"http"}, Status: "disabled"},
		{Name: "pathonly-0@file", Provider: "file", Rule: "PathPrefix(`/only`)", Priority: 12, EntryPoints: []string{"http"}, Status: "enabled"},
	}

	want := []Route{
		{Rule: "Host(`app.loc`)", Priority: 49, Hosts: []string{"app.loc"}},
		{Rule: "Host(`app.loc`) && (PathPrefix(`/api/`) || Path(`/api`))", Priority: 10004, Hosts: []string{"app.loc"}},
	}

	got := Routes(routers)
	if !reflect.DeepEqual(got.Routes, want) {
		t.Fatalf("Routes() = %+v, want %+v", got.Routes, want)
	}
	if len(got.Rejected) != 0 {
		t.Errorf("Rejected = %v, want none", got.Rejected)
	}
}

func TestExtractHosts(t *testing.T) {
	tests := []struct {
		name string
		rule string
		want []string
	}{
		{"single host", "Host(`app.loc`)", []string{"app.loc"}},
		{"several arguments", "Host(`a.loc`, `b.loc`)", []string{"a.loc", "b.loc"}},
		{"alternation", "Host(`a.loc`) || Host(`b.loc`)", []string{"a.loc", "b.loc"}},
		{"with a path", "Host(`app.loc`) && (PathPrefix(`/api/`) || Path(`/api`))", []string{"app.loc"}},
		{"regexp host", "HostRegexp(`^.*\\.loc$`)", []string{"^.*\\.loc$"}},
		{"regexp and host", "HostRegexp(`^.*\\.loc$`) || Host(`app.loc`)", []string{"^.*\\.loc$", "app.loc"}},
		{"no host matcher", "PathPrefix(`/api`)", nil},
		{"header matcher is not a host", "HeaderRegexp(`X-Host`, `.*`)", nil},
		{"empty rule", "", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractHosts(tt.rule); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtractHosts(%q) = %q, want %q", tt.rule, got, tt.want)
			}
		})
	}
}

func TestRoutersReportsUnreachableSeparately(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	_, err := NewWithTimeout(url, time.Second).Routers(t.Context())
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Routers() error = %v, want it to wrap ErrUnreachable", err)
	}

	answering := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer answering.Close()

	_, err = New(answering.Client(), answering.URL).Routers(t.Context())
	if err == nil {
		t.Fatal("Routers() error = nil, want an error")
	}
	if errors.Is(err, ErrUnreachable) {
		t.Errorf("Routers() error = %v, want a machine that answered not to be reported as unreachable", err)
	}
}

// A regular expression hostname may contain a parenthesis of its own, which a
// scan stopping at the first `)` would truncate. The rule is copied verbatim
// either way, but a truncated hostname would be reported under a corrupted
// name and would never be recognised as colliding.
func TestExtractHostsKeepsAParenthesisedRegexp(t *testing.T) {
	tests := []struct {
		name string
		rule string
		want []string
	}{
		{"grouped alternation", "HostRegexp(`^(a|b)\\.loc$`)", []string{"^(a|b)\\.loc$"}},
		{"grouped with a path", "HostRegexp(`^(a|b)\\.loc$`) && (PathPrefix(`/api/`) || Path(`/api`))", []string{"^(a|b)\\.loc$"}},
		{"grouped then plain", "HostRegexp(`^(a|b)\\.loc$`) || Host(`app.loc`)", []string{"^(a|b)\\.loc$", "app.loc"}},
		{"a matcher ending in host is not one", "XHost(`nope.loc`)", nil},
		{"a host inside another matcher's argument", "HeaderRegexp(`X-Host`, `.*`)", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractHosts(tt.rule); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtractHosts(%q) = %q, want %q", tt.rule, got, tt.want)
			}
		})
	}
}

// A negated matcher serves everything except that hostname. Recording it as a
// claim would have the machine own the one hostname it does not serve, and the
// machine that genuinely serves it dropped as a collision.
func TestExtractHostsIgnoresANegatedMatcher(t *testing.T) {
	tests := []struct {
		name string
		rule string
		want []string
	}{
		{"negated alone", "!Host(`a.loc`)", nil},
		{"negated with a space", "! Host(`a.loc`)", nil},
		{"negated regexp", "!HostRegexp(`^a\\.loc$`)", nil},
		{"a claim and a negation", "Host(`a.loc`) && !Host(`b.loc`)", []string{"a.loc"}},
		{"a negation and a claim", "!Host(`b.loc`) && Host(`a.loc`)", []string{"a.loc"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractHosts(tt.rule); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtractHosts(%q) = %q, want %q", tt.rule, got, tt.want)
			}
		})
	}
}

func declarationHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != MiddlewaresPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}
}

// A machine is adopted only when it says it is this proxy. The port alone
// identifies a Traefik, which an unrelated one on the tailnet also is.
func TestDeclares(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "declares itself",
			body: `[{"name": "spark-http-proxy@file", "provider": "file", "status": "enabled", "type": "headers"}]`,
			want: true,
		},
		{
			name: "an unrelated traefik",
			body: `[{"name": "compress@file", "provider": "file", "status": "enabled", "type": "compress"}]`,
			want: false,
		},
		{
			name: "no middlewares at all",
			body: `[]`,
			want: false,
		},
		{
			name: "the declaration is disabled",
			body: `[{"name": "spark-http-proxy@file", "provider": "file", "status": "disabled", "type": "headers"}]`,
			want: false,
		},
		{
			name: "a name that only looks like the declaration",
			body: `[{"name": "spark-http-proxy-lookalike@file", "provider": "file", "status": "enabled", "type": "headers"}]`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(declarationHandler(tt.body))
			defer server.Close()

			got, err := New(server.Client(), server.URL).Declares(t.Context())
			if err != nil {
				t.Fatalf("Declares() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Declares() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeclaresReportsAnUnreachableMachine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	if _, err := NewWithTimeout(url, time.Second).Declares(t.Context()); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Declares() error = %v, want it to wrap ErrUnreachable", err)
	}
}

// A rule is copied into a local router verbatim, so an alternative that names
// no host matches every request this machine receives. Hostname extraction sees
// only the constrained side, so local precedence never recognises the shadowing
// as a collision: the rule has to be refused instead.
func TestRoutesRefusesAnUnconstrainedAlternative(t *testing.T) {
	tests := []struct {
		name     string
		rule     string
		accepted bool
	}{
		{"a bare path alternative hijacks everything", "Host(`peer.loc`) || PathPrefix(`/`)", false},
		{"an ordinary multi-host rule", "Host(`a.loc`) || Host(`b.loc`)", true},
		{"a negated host constrains nothing", "Host(`a.loc`) || !Host(`b.loc`)", false},
		{"a path inside a constrained branch", "Host(`app.loc`) && (PathPrefix(`/api/`) || Path(`/api`))", true},
		{"a grouped alternation with a path", "(Host(`a.loc`) || Host(`b.loc`)) && PathPrefix(`/api/`)", true},
		{"a regexp host on one side", "HostRegexp(`^.*\\.loc$`) || Host(`b.loc`)", true},
		{"an alternative with a header matcher only", "Host(`a.loc`) || HeadersRegexp(`X-Any`, `.*`)", false},
		{"unbalanced parentheses are not understood", "Host(`a.loc`) || (Host(`b.loc`)", false},
		{"an unclosed backtick is not understood", "Host(`a.loc`) || Host(`b.loc)", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routers := []Router{{
				Name: "peer-0@file", Provider: "file", Rule: tt.rule,
				Priority: 49, EntryPoints: []string{"http"}, Status: "enabled",
			}}

			got := Routes(routers)

			if tt.accepted {
				if len(got.Routes) != 1 {
					t.Fatalf("Routes = %+v, want the rule accepted", got)
				}
				if len(got.Rejected) != 0 {
					t.Errorf("Rejected = %v, want none", got.Rejected)
				}
				return
			}
			if len(got.Routes) != 0 {
				t.Fatalf("Routes = %+v, want the rule refused", got.Routes)
			}
			if len(got.Rejected) != 1 || got.Rejected[0] != tt.rule {
				t.Errorf("Rejected = %v, want the rule reported", got.Rejected)
			}
		})
	}
}

func TestTopLevelAlternatives(t *testing.T) {
	tests := []struct {
		name  string
		rule  string
		want  []string
		valid bool
	}{
		{"no alternation", "Host(`a.loc`)", []string{"Host(`a.loc`)"}, true},
		{"top level", "Host(`a.loc`) || Host(`b.loc`)", []string{"Host(`a.loc`) ", " Host(`b.loc`)"}, true},
		{"nested is not top level", "Host(`a.loc`) && (Path(`/x`) || Path(`/y`))", []string{"Host(`a.loc`) && (Path(`/x`) || Path(`/y`))"}, true},
		{"inside backticks is not an operator", "HostRegexp(`^(a||b)\\.loc$`)", []string{"HostRegexp(`^(a||b)\\.loc$`)"}, true},
		{"unbalanced", "Host(`a.loc`))", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := topLevelAlternatives(tt.rule)
			if ok != tt.valid {
				t.Fatalf("topLevelAlternatives() ok = %v, want %v", ok, tt.valid)
			}
			if tt.valid && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("topLevelAlternatives() = %q, want %q", got, tt.want)
			}
		})
	}
}

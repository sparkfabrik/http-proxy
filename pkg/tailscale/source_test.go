package tailscale

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func statusHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != StatusPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestSocketSourceReadsTheDocument(t *testing.T) {
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		statusHandler(tailnetStatus)(w, r)
	}))
	defer server.Close()

	status, err := NewHTTPSource(server.Client(), server.URL).Status(t.Context())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if len(status.Peers()) != 6 {
		t.Errorf("Peers() = %d machines, want 6", len(status.Peers()))
	}
	if gotHost != socketHost {
		t.Errorf("Host header = %q, want %q", gotHost, socketHost)
	}
}

func TestSocketSourceErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"daemon refuses", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }},
		{"body is not json", statusHandler(`not json`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			if _, err := NewHTTPSource(server.Client(), server.URL).Status(t.Context()); err == nil {
				t.Fatal("Status() error = nil, want an error")
			}
		})
	}
}

func writeStatusFile(t *testing.T, document string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tailscale-status.json")
	if err := os.WriteFile(path, []byte(document), 0644); err != nil {
		t.Fatalf("failed to write the status file: %v", err)
	}
	written := time.Now().Add(-age)
	if err := os.Chtimes(path, written, written); err != nil {
		t.Fatalf("failed to age the status file: %v", err)
	}
	return path
}

func TestFileSourceReadsAFreshDocument(t *testing.T) {
	path := writeStatusFile(t, tailnetStatus, 5*time.Second)

	status, err := NewFileSource(path, time.Minute).Status(t.Context())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if len(status.Peers()) != 6 {
		t.Errorf("Peers() = %d machines, want 6", len(status.Peers()))
	}
}

// A host that stopped refreshing the document must withdraw its machines rather
// than keep forwarding to whatever the document last said.
func TestFileSourceRejectsAStaleDocument(t *testing.T) {
	path := writeStatusFile(t, tailnetStatus, 10*time.Minute)

	if _, err := NewFileSource(path, time.Minute).Status(t.Context()); err == nil {
		t.Fatal("Status() error = nil, want a stale document to be refused")
	}
}

func TestFileSourceReportsAMissingDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.json")

	if _, err := NewFileSource(path, time.Minute).Status(t.Context()); err == nil {
		t.Fatal("Status() error = nil, want a missing document to be reported")
	}
}

package tailscale

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// StatusPath is the local API endpoint returning the tailnet status document.
const StatusPath = "/localapi/v0/status"

// socketHost is the Host header the daemon expects on local API requests. The
// unix socket has no meaningful authority, and the daemon rejects a request
// carrying an unexpected one.
const socketHost = "local-tailscaled.sock"

// Source produces the tailnet status document. Only the transport differs
// between implementations: the document, and therefore the filter applied to
// it, is the same one everywhere.
type Source interface {
	Status(ctx context.Context) (*Status, error)
}

// SocketSource reads the document from the daemon's local API. The socket is
// world-readable, so no privilege is needed.
type SocketSource struct {
	httpClient *http.Client
	baseURL    string
}

// NewSocketSource returns a source talking to the daemon over its unix socket.
func NewSocketSource(socketPath string, timeout time.Duration) *SocketSource {
	dialer := &net.Dialer{Timeout: timeout}
	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		},
	}
	return NewHTTPSource(httpClient, "http://"+socketHost)
}

// NewHTTPSource returns a source talking to baseURL with the given HTTP client.
// Tests point it at an httptest server; production uses NewSocketSource.
func NewHTTPSource(httpClient *http.Client, baseURL string) *SocketSource {
	return &SocketSource{httpClient: httpClient, baseURL: baseURL}
}

// Status fetches the tailnet status document from the daemon.
func (s *SocketSource) Status(ctx context.Context) (*Status, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+StatusPath, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build status request: %w", err)
	}
	req.Host = socketHost

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query the tailscale daemon: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tailscale daemon returned status %d", resp.StatusCode)
	}

	status, err := ParseStatus(resp.Body)
	if err != nil {
		return nil, err
	}
	status.UpdatedAt = time.Now()
	return status, nil
}

// FileSource reads a document a host wrote, for a platform whose daemon exposes
// no socket a container can reach.
type FileSource struct {
	path   string
	maxAge time.Duration
}

// NewFileSource returns a source reading the document at path. A document older
// than maxAge is treated as no document rather than as an empty tailnet, so a
// host that stopped refreshing it withdraws its peers instead of freezing them.
func NewFileSource(path string, maxAge time.Duration) *FileSource {
	return &FileSource{path: path, maxAge: maxAge}
}

// Status reads the tailnet status document from the file the host writes. The
// time the document was written is the file's modification time, which is what
// the writing command sets and what a stale document is judged by.
func (s *FileSource) Status(context.Context) (*Status, error) {
	info, err := os.Stat(s.path)
	if err != nil {
		return nil, fmt.Errorf("failed to read the tailnet status file: %w", err)
	}

	age := time.Since(info.ModTime())
	if age > s.maxAge {
		return nil, fmt.Errorf("tailnet status file is stale: written %s ago, tolerating %s", age.Truncate(time.Second), s.maxAge)
	}

	file, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("failed to open the tailnet status file: %w", err)
	}
	defer file.Close()

	status, err := ParseStatus(file)
	if err != nil {
		return nil, err
	}
	status.UpdatedAt = info.ModTime()
	return status, nil
}

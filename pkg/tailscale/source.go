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

// socketHost is the Host header the daemon expects on local API requests.
const socketHost = "local-tailscaled.sock"

// Source produces the tailnet status document.
type Source interface {
	Status(ctx context.Context) (*Status, error)
}

// SocketSource reads the document from the daemon's local API.
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

// FileSource reads a document the host wrote.
type FileSource struct {
	path   string
	maxAge time.Duration
}

// NewFileSource returns a source reading the document at path, treating one
// older than maxAge as no document.
func NewFileSource(path string, maxAge time.Duration) *FileSource {
	return &FileSource{path: path, maxAge: maxAge}
}

// Status reads the document from the file, aged by its modification time.
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

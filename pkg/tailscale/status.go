// Package tailscale reads the machines of a tailnet from the status document
// the local Tailscale daemon produces. It reads no environment.
package tailscale

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"time"
)

// Skip reasons for a machine found on the tailnet that is not probed.
const (
	SkipOffline   = "offline"
	SkipOtherUser = "belongs to another user"
	SkipNoAddress = "no IPv4 tailnet address"
)

// Peer is a machine found on the tailnet.
type Peer struct {
	// ID is the node key, unique where the hostname is not.
	ID string
	// Name is the Tailscale hostname, and orders two machines claiming one host.
	Name string
	// Address is the peer's IPv4 tailnet address.
	Address string
	// SkipReason says why a machine is not probed, and is empty when it may be.
	SkipReason string
}

// Probeable reports whether this machine may be probed for routes.
func (p Peer) Probeable() bool {
	return p.SkipReason == ""
}

// node is the subset of a status entry this package reads.
type node struct {
	HostName     string   `json:"HostName"`
	Online       bool     `json:"Online"`
	UserID       int64    `json:"UserID"`
	TailscaleIPs []string `json:"TailscaleIPs"`
}

// Status is the tailnet status document, the one `tailscale status --json`
// prints and the daemon's local API returns.
type Status struct {
	Self *node            `json:"Self"`
	Peer map[string]*node `json:"Peer"`

	// UpdatedAt is when the document was produced, set by the source: the
	// document itself does not say.
	UpdatedAt time.Time `json:"-"`
}

// ParseStatus decodes a status document, rejecting one without a self node.
func ParseStatus(r io.Reader) (*Status, error) {
	var status Status
	if err := json.NewDecoder(r).Decode(&status); err != nil {
		return nil, fmt.Errorf("failed to decode the tailnet status: %w", err)
	}
	if status.Self == nil {
		return nil, fmt.Errorf("tailnet status carries no self node")
	}
	return &status, nil
}

// Peers returns the machines of the tailnet other than this one, sorted by
// hostname, marking as probeable the online ones belonging to the same user.
func (s *Status) Peers() []Peer {
	peers := make([]Peer, 0, len(s.Peer))
	for id, n := range s.Peer {
		if n == nil {
			continue
		}
		// By address, since another machine may share this one's hostname.
		if sharesAddress(n.TailscaleIPs, s.Self.TailscaleIPs) {
			continue
		}
		peers = append(peers, Peer{
			ID:         id,
			Name:       n.HostName,
			Address:    firstIPv4(n.TailscaleIPs),
			SkipReason: skipReason(n, s.Self),
		})
	}

	slices.SortStableFunc(peers, func(a, b Peer) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return peers
}

// skipReason returns why a machine is not probed, or an empty string.
func skipReason(n *node, self *node) string {
	if n.UserID != self.UserID {
		return SkipOtherUser
	}
	if !n.Online {
		return SkipOffline
	}
	if firstIPv4(n.TailscaleIPs) == "" {
		return SkipNoAddress
	}
	return ""
}

// sharesAddress reports whether two nodes are the same machine.
func sharesAddress(addresses, others []string) bool {
	for _, address := range addresses {
		if slices.Contains(others, address) {
			return true
		}
	}
	return false
}

// firstIPv4 returns the first IPv4 address of a node.
func firstIPv4(addresses []string) string {
	for _, raw := range addresses {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			continue
		}
		if addr.Is4() {
			return addr.String()
		}
	}
	return ""
}

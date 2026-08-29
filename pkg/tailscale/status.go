// Package tailscale reads the machines of a tailnet from the status document
// the local Tailscale daemon produces, and reduces it to the machines that may
// be probed for routes.
//
// A source decides only how the document is obtained. The filter below runs
// over every document whatever its transport, so the restriction to the user's
// own machines cannot depend on the platform. This package reads no
// environment, so no setting reaches that filter either.
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
	// ID is the node key the status document keys the machine by. Tailscale
	// does not make hostnames unique, and a tailnet routinely carries several
	// machines called the same thing, so identity is the key and the hostname
	// is only a label.
	ID string
	// Name is the Tailscale hostname. It is the first ordering key when two
	// machines serve the same hostname, so every machine reaches the same
	// conclusion; ID breaks the tie when the names are equal too.
	Name string
	// Address is the peer's IPv4 tailnet address.
	Address string
	// SkipReason is empty for a machine that may be probed, and otherwise says
	// why it is not. Excluded machines are kept rather than dropped so the
	// command line can explain a machine that contributed nothing; a machine
	// carrying a reason is never probed.
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

	// UpdatedAt is when the document was produced, which a source sets. The
	// document itself does not say, and how old it is decides whether it can
	// still be trusted, so the source that obtained it is what knows.
	UpdatedAt time.Time `json:"-"`
}

// ParseStatus decodes a status document. A document without a self node is
// rejected: the user it names is the other half of the ownership test, and
// without it no machine can be accepted.
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
// hostname. Only machines that are online and belong to the same user as this
// one are probeable; the rest carry a skip reason.
//
// The same-user test is the trust boundary of peer routing. It is applied here,
// in the one place holding both user identities, for every source.
func (s *Status) Peers() []Peer {
	peers := make([]Peer, 0, len(s.Peer))
	for id, n := range s.Peer {
		if n == nil {
			continue
		}
		// This machine is excluded by address rather than by hostname: another
		// machine may legitimately carry the same hostname, and dropping it
		// would lose its routes silently.
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

// skipReason returns why a machine is not probed, or an empty string when it
// is. The user test comes first: another user's machine is excluded whatever
// else is true of it.
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

// sharesAddress reports whether two nodes hold any tailnet address in common,
// which on a tailnet means they are the same machine.
func sharesAddress(addresses, others []string) bool {
	for _, address := range addresses {
		if slices.Contains(others, address) {
			return true
		}
	}
	return false
}

// firstIPv4 returns the first IPv4 address of a node. The hop is a plain HTTP
// request to a literal address, and an IPv4 literal needs no bracketing.
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

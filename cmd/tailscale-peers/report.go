package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Render turns a cycle into the table the command line shows.
func (r *Report) Render() string {
	var b strings.Builder

	fmt.Fprintf(&b, "Tailnet peers, from the cycle at %s\n", r.UpdatedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "Source: %s", r.Source)
	if !r.SourceUpdatedAt.IsZero() {
		fmt.Fprintf(&b, ", tailnet status produced at %s", r.SourceUpdatedAt.Format(time.RFC3339))
	}
	b.WriteString("\n\n")

	if r.SourceError != "" {
		// Printed before the table.
		fmt.Fprintf(&b, "No machines were considered: the tailnet status could not be read (%s).\n", r.SourceError)
		fmt.Fprintf(&b, "This is the status source failing, not an empty tailnet. It is retried every %s.\n", r.RefreshInterval)
		return b.String()
	}

	if r.LocalError != "" {
		fmt.Fprintf(&b, "The local routing table could not be read (%s), so the previous peer routes were kept.\n", r.LocalError)
		fmt.Fprintf(&b, "No machine was probed this cycle, so this is not a verdict on any of them. It is usual for a few seconds after a restart, while the proxy is still starting.%s\n\n", r.retryHint())
		b.WriteString(renderTable(r.Peers, r.UpdatedAt))
		return b.String()
	}

	if len(r.Peers) == 0 {
		b.WriteString("No other machines are on this tailnet.\n")
		return b.String()
	}

	b.WriteString(renderTable(r.Peers, r.UpdatedAt))
	b.WriteString("\n")
	b.WriteString(renderSummary(r.Peers))

	if rejected := rejectedRules(r.Peers); len(rejected) > 0 {
		b.WriteString("\nRules refused for not naming a host on every alternative:\n")
		for _, line := range rejected {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}

	if len(r.Collisions) > 0 {
		b.WriteString("\nHostnames claimed more than once:\n")
		for _, collision := range r.Collisions {
			fmt.Fprintf(&b, "  %s is served by %s, and also offered by %s\n", collision.Host, collision.ServedBy, collision.AlsoOn)
		}
	}

	return b.String()
}

// rejectedRules lists the rules refused this cycle and who offered them.
func rejectedRules(peers []PeerReport) []string {
	var lines []string
	for _, peer := range peers {
		for _, rule := range peer.RejectedRules {
			lines = append(lines, fmt.Sprintf("%s: %s", peer.Name, rule))
		}
	}
	return lines
}

// Useful rows first; every status that is not ok lands in EXCLUDED.
var peerGroups = []struct {
	label    string
	lastCol  string
	statuses []string
}{
	{"PROXY", "HOSTNAMES", []string{statusOK}},
	{"EXCLUDED", "STATUS", []string{statusNoProxy, statusUndeclared, statusUnreachable, statusSkipped}},
}

// renderTable renders each non-empty group. An empty one is omitted entirely.
func renderTable(peers []PeerReport, cycle time.Time) string {
	proxy := make([]PeerReport, 0, len(peers))
	excluded := make([]PeerReport, 0, len(peers))

	for _, peer := range peers {
		if peer.Status == statusOK {
			proxy = append(proxy, peer)
			continue
		}
		excluded = append(excluded, peer)
	}

	var b strings.Builder
	writeGroup(&b, peerGroups[0].label, peerGroups[0].lastCol, proxy, cycle)
	writeGroup(&b, peerGroups[1].label, peerGroups[1].lastCol, excluded, cycle)

	return b.String()
}

// knownStatuses are the ones whose reason text explains itself in the table.
var knownStatuses = map[string]bool{
	statusOK:          true,
	statusNoProxy:     true,
	statusUndeclared:  true,
	statusUnreachable: true,
	statusSkipped:     true,
}

// peerStatusCell names the status itself when it is not one this table knows, so
// a status added later cannot appear as a bare reason or as nothing.
func peerStatusCell(peer PeerReport, cycle time.Time) string {
	detail := peerDetail(peer, cycle)
	if knownStatuses[peer.Status] {
		return detail
	}
	if peer.Reason == "" {
		return peer.Status
	}
	return peer.Status + ": " + detail
}

// writeGroup writes one labelled block, or nothing when it holds no machine.
// Widths are computed from the rows in this group alone, so a long name in one
// group does not pad the others.
func writeGroup(b *strings.Builder, label, lastCol string, peers []PeerReport, cycle time.Time) {
	if len(peers) == 0 {
		return
	}

	slices.SortStableFunc(peers, func(a, b PeerReport) int {
		return strings.Compare(a.Name, b.Name)
	})

	rows := make([][3]string, 0, len(peers))
	for _, peer := range peers {
		rows = append(rows, [3]string{peer.Name, peer.Address, peerStatusCell(peer, cycle)})
	}

	headers := [3]string{"MACHINE", "ADDRESS", lastCol}
	widths := [3]int{}
	for i, header := range headers {
		widths[i] = len(header)
	}
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], len(cell))
		}
	}

	if b.Len() > 0 {
		b.WriteString("\n")
	}
	fmt.Fprintf(b, "%s\n", label)
	writeRow(b, headers, widths)
	for _, row := range rows {
		writeRow(b, row, widths)
	}
}

func writeRow(b *strings.Builder, row [3]string, widths [3]int) {
	// One indent per row, then the original padding between columns, so the
	// last column stays separated from the one before it.
	b.WriteString("  ")
	for i, cell := range row {
		if i == len(row)-1 {
			b.WriteString(cell)
			break
		}
		fmt.Fprintf(b, "%-*s  ", widths[i], cell)
	}
	b.WriteString("\n")
}

// peerDetail is what a machine contributed, or why it contributed nothing,
// showing a retry as the wait ahead of it.
func peerDetail(peer PeerReport, cycle time.Time) string {
	if len(peer.Hosts) > 0 {
		return strings.Join(peer.Hosts, ", ")
	}

	detail := "no hostnames"
	if peer.Reason != "" {
		detail = compactReason(peer.Reason)
	}
	if wait := peer.RetryAt.Sub(cycle); wait > 0 {
		detail = fmt.Sprintf("%s, retrying in %s", detail, wait.Round(time.Second))
	}
	return detail
}

// compactReason shortens a probe failure for the table.
func compactReason(reason string) string {
	for _, known := range []string{
		"connection refused",
		"no route to host",
		"network is unreachable",
		"i/o timeout",
	} {
		if strings.Contains(reason, known) {
			return known
		}
	}
	if strings.Contains(reason, "Client.Timeout") || strings.Contains(reason, "context deadline exceeded") {
		return "timed out"
	}
	if strings.Contains(reason, "does not declare itself") {
		return "answered, but does not declare itself as this proxy"
	}

	return reason
}

// renderSummary counts what the cycle found and names the ordinary outcomes.
func renderSummary(peers []PeerReport) string {
	var running, hostnames int
	for _, peer := range peers {
		if peer.Status == statusOK {
			running++
			hostnames += len(peer.Hosts)
		}
	}
	excluded := len(peers) - running

	var b strings.Builder
	if running == 0 {
		fmt.Fprintf(&b, "\n%s, none running this proxy, %d excluded.\n", plural(len(peers), "machine"), excluded)
		return b.String()
	}

	fmt.Fprintf(&b, "\n%s, %d running this proxy forwarding %s, %d excluded.\n",
		plural(len(peers), "machine"), running, plural(hostnames, "hostname"), excluded)

	return b.String()
}

func plural(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// summaryStateFile holds what `status` needs, in a form that needs no parser.
func summaryStateFile(stateFile string) string {
	return strings.TrimSuffix(stateFile, filepath.Ext(stateFile)) + "-summary"
}

// completedStateFile holds the timestamp of the last completed cycle and nothing else.
func completedStateFile(stateFile string) string {
	return strings.TrimSuffix(stateFile, filepath.Ext(stateFile)) + "-completed-at"
}

// retryHint names the wait the reader should expect before the next attempt.
func (r *Report) retryHint() string {
	if r.RefreshInterval == "" {
		return ""
	}
	return " The next attempt is in a few seconds, backing off to " + r.RefreshInterval + "."
}

// renderedStateFile is the state file's rendered twin, written beside it.
func renderedStateFile(stateFile string) string {
	return strings.TrimSuffix(stateFile, filepath.Ext(stateFile)) + ".txt"
}

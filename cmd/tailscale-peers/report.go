package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Render turns a cycle into the table the command line shows.
//
// The service renders it rather than the command parsing the state file,
// because a formatter in the shell would be a second implementation of what a
// peer contributed and could disagree with the one that acted. It also keeps
// the command free of a JSON parser, which is not a dependency this project has.
func (r *Report) Render() string {
	var b strings.Builder

	fmt.Fprintf(&b, "Tailnet peers, from the cycle at %s\n", r.UpdatedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "Source: %s", r.Source)
	if !r.SourceUpdatedAt.IsZero() {
		fmt.Fprintf(&b, ", tailnet status produced at %s", r.SourceUpdatedAt.Format(time.RFC3339))
	}
	b.WriteString("\n\n")

	if r.SourceError != "" {
		// Said before the table, because an empty table after a daemon blip
		// reads like a fault rather than like a source that stopped answering.
		fmt.Fprintf(&b, "No machines were considered: the tailnet status could not be read (%s).\n", r.SourceError)
		fmt.Fprintf(&b, "This is the status source failing, not an empty tailnet. It is retried every %s.\n", r.RefreshInterval)
		return b.String()
	}

	if r.LocalError != "" {
		fmt.Fprintf(&b, "The local routing table could not be read (%s), so the previous peer routes were kept.\n\n", r.LocalError)
	}

	if len(r.Peers) == 0 {
		b.WriteString("No other machines are on this tailnet.\n")
		return b.String()
	}

	b.WriteString(renderTable(r.Peers, r.UpdatedAt))
	b.WriteString("\n")
	b.WriteString(renderSummary(r.Peers))

	if len(r.Collisions) > 0 {
		b.WriteString("\nHostnames claimed more than once:\n")
		for _, collision := range r.Collisions {
			fmt.Fprintf(&b, "  %s is served by %s, and also offered by %s\n", collision.Host, collision.ServedBy, collision.AlsoOn)
		}
	}

	return b.String()
}

func renderTable(peers []PeerReport, cycle time.Time) string {
	rows := make([][4]string, 0, len(peers))
	for _, peer := range peers {
		rows = append(rows, [4]string{peer.Name, peer.Address, peer.Status, peerDetail(peer, cycle)})
	}

	headers := [4]string{"MACHINE", "ADDRESS", "STATUS", "DETAIL"}
	widths := [4]int{}
	for i, header := range headers {
		widths[i] = len(header)
	}
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], len(cell))
		}
	}

	var b strings.Builder
	writeRow(&b, headers, widths)
	for _, row := range rows {
		writeRow(&b, row, widths)
	}
	return b.String()
}

func writeRow(b *strings.Builder, row [4]string, widths [4]int) {
	for i, cell := range row {
		if i == len(row)-1 {
			b.WriteString(cell)
			break
		}
		fmt.Fprintf(b, "%-*s  ", widths[i], cell)
	}
	b.WriteString("\n")
}

// peerDetail is what a machine contributed, or why it contributed nothing.
//
// A retry is shown as the wait rather than as an absolute time. An absolute one
// printed to the second sits beside a cycle timestamp rounded the same way, so a
// short wait renders as the same instant as the cycle that scheduled it and
// reads as though the backoff were not advancing.
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

// compactReason shortens a probe failure for the table. A Go transport error
// carries the whole request URL, which is three quarters of a line of noise
// repeated once per machine, and on a tailnet most machines fail. The state
// file keeps the full text for anyone who needs it.
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

// renderSummary keeps a tailnet full of phones, routers and televisions from
// reading as a fault: on a real tailnet most machines run no proxy, and that is
// the ordinary case rather than an error to investigate.
func renderSummary(peers []PeerReport) string {
	var forwarding, hostnames, noProxy, undeclared, unreachable, skipped int
	for _, peer := range peers {
		switch peer.Status {
		case statusOK:
			hostnames += len(peer.Hosts)
			if len(peer.Hosts) > 0 {
				forwarding++
			}
		case statusNoProxy:
			noProxy++
		case statusUndeclared:
			undeclared++
		case statusUnreachable:
			unreachable++
		case statusSkipped:
			skipped++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n%s considered, %s forwarding %s.\n",
		plural(len(peers), "machine"), plural(forwarding, "machine"), plural(hostnames, "hostname"))

	if noProxy+unreachable > 0 {
		fmt.Fprintf(&b, "%s did not answer as a proxy, which is usual on a tailnet carrying phones, routers and the like.\n",
			plural(noProxy+unreachable, "machine"))
	}
	if undeclared > 0 {
		fmt.Fprintf(&b, "%s answered but did not declare itself as this proxy, so its routes were not used.\n",
			plural(undeclared, "machine"))
	}
	if skipped > 0 {
		fmt.Fprintf(&b, "%s excluded, with the reason in the table.\n", plural(skipped, "machine"))
	}

	return b.String()
}

func plural(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// renderedStateFile is the state file's rendered twin, written beside it so the
// command line can show a cycle without parsing anything.
func renderedStateFile(stateFile string) string {
	return strings.TrimSuffix(stateFile, filepath.Ext(stateFile)) + ".txt"
}

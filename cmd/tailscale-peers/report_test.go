package main

import (
	"strings"
	"testing"
	"time"
)

func testReport() *Report {
	return &Report{
		UpdatedAt:       time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Enabled:         true,
		RefreshInterval: "10s",
		Source:          "file",
		SourceUpdatedAt: time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC),
		Peers: []PeerReport{
			{Name: "machine-b", Address: "100.64.0.2", Status: statusOK, Hosts: []string{"app.loc", "api.loc"}},
			{Name: "machine-c", Address: "100.64.0.3", Status: statusUnreachable, Reason: "machine did not answer: dial tcp: connect: connection refused",
				RetryAt: time.Date(2026, 1, 2, 3, 4, 25, 0, time.UTC)},
			{Name: "machine-d", Address: "100.64.0.4", Status: statusNoProxy, Reason: "traefik api returned status 404"},
			{Name: "machine-e", Address: "100.64.0.5", Status: statusSkipped, Reason: "belongs to another user"},
		},
		Collisions: []Collision{{Host: "shared.loc", ServedBy: "local", AlsoOn: "machine-b"}},
	}
}

func TestRenderListsEveryMachineAndItsReason(t *testing.T) {
	rendered := testReport().Render()

	for _, want := range []string{
		"machine-b", "100.64.0.2", "app.loc, api.loc",
		"machine-c", "connection refused, retrying in 20s",
		"machine-d", "traefik api returned status 404",
		"machine-e", "belongs to another user",
		"2026-01-02T03:04:05Z", "tailnet status produced at 2026-01-02T03:04:00Z",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered report is missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderNamesCollisionsAndTheirWinner(t *testing.T) {
	rendered := testReport().Render()

	if !strings.Contains(rendered, "shared.loc is served by local, and also offered by machine-b") {
		t.Errorf("rendered report does not name the collision:\n%s", rendered)
	}
}

// Most machines on a tailnet are phones, routers and televisions. A report of
// them has to read as ordinary rather than as a fault.
func TestRenderTreatsMachinesWithoutAProxyAsUsual(t *testing.T) {
	rendered := testReport().Render()

	if !strings.Contains(rendered, "4 machines considered, 1 machine forwarding 2 hostnames.") {
		t.Errorf("rendered report lacks its summary line:\n%s", rendered)
	}
	if !strings.Contains(rendered, "usual on a tailnet") {
		t.Errorf("rendered report does not say that machines without a proxy are usual:\n%s", rendered)
	}
}

// A source that stopped answering must not look like a tailnet with nobody on
// it, which is what an empty table alone would suggest.
func TestRenderLeadsWithASourceFailure(t *testing.T) {
	report := testReport()
	report.SourceError = "dial unix /var/run/tailscale/tailscaled.sock: no such file"
	report.Peers = nil

	rendered := report.Render()

	if !strings.Contains(rendered, "the tailnet status could not be read") {
		t.Errorf("rendered report does not lead with the source failure:\n%s", rendered)
	}
	if !strings.Contains(rendered, "not an empty tailnet") {
		t.Errorf("rendered report does not distinguish a failure from an empty tailnet:\n%s", rendered)
	}
	if !strings.Contains(rendered, "retried every 10s") {
		t.Errorf("rendered report does not say the source is retried:\n%s", rendered)
	}
}

func TestRenderReportsAnUnreadableLocalTable(t *testing.T) {
	report := testReport()
	report.LocalError = "connection refused"

	rendered := report.Render()

	if !strings.Contains(rendered, "previous peer routes were kept") {
		t.Errorf("rendered report does not explain what happened to the routes:\n%s", rendered)
	}
}

func TestRenderedStateFileSitsBesideTheStateFile(t *testing.T) {
	if got, want := renderedStateFile("/state/tailscale-peers.json"), "/state/tailscale-peers.txt"; got != want {
		t.Errorf("renderedStateFile() = %q, want %q", got, want)
	}
}

// Most machines on a tailnet fail the probe, so their reason is repeated once
// per row. The table shows the part that tells them apart; the state file keeps
// the whole error.
func TestCompactReason(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "refused",
			in:   `machine did not answer: Get "http://100.100.0.20:30000/api/http/middlewares": dial tcp 100.100.0.20:30000: connect: connection refused`,
			want: "connection refused",
		},
		{
			name: "timed out",
			in:   `machine did not answer: Get "http://100.100.0.31:30000/api/http/middlewares": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`,
			want: "timed out",
		},
		{
			name: "undeclared",
			in:   "machine does not declare itself as spark-http-proxy",
			want: "answered, but does not declare itself as this proxy",
		},
		{
			name: "anything else is left alone",
			in:   "offline",
			want: "offline",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compactReason(tt.in); got != tt.want {
				t.Errorf("compactReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The retry is shown as the wait, not as an absolute time: printed to the
// second, an absolute retry sits beside a cycle timestamp rounded the same way,
// so a short wait renders as the same instant as the cycle that scheduled it and
// reads as though the backoff were not advancing.
func TestRenderShowsTheWaitBeforeTheNextAttempt(t *testing.T) {
	cycle := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	report := &Report{
		UpdatedAt:       cycle,
		Enabled:         true,
		RefreshInterval: "10s",
		Source:          "socket",
		Peers: []PeerReport{
			{Name: "machine-b", Address: "100.100.0.11", Status: statusUnreachable,
				Reason:  "machine did not answer: connection refused",
				RetryAt: cycle.Add(90 * time.Second)},
		},
	}

	rendered := report.Render()

	if !strings.Contains(rendered, "connection refused, retrying in 1m30s") {
		t.Errorf("rendered report does not show the wait:\n%s", rendered)
	}
	if strings.Contains(rendered, cycle.Format(time.RFC3339)+"  ") {
		t.Errorf("the retry is rendered as an absolute time beside the cycle time:\n%s", rendered)
	}
}

// A retry already in the past is not a retry. Only a future one is shown.
func TestRenderOmitsARetryThatIsNotAhead(t *testing.T) {
	cycle := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	report := &Report{
		UpdatedAt:       cycle,
		Enabled:         true,
		RefreshInterval: "10s",
		Source:          "socket",
		Peers: []PeerReport{
			{Name: "machine-b", Address: "100.100.0.11", Status: statusUnreachable,
				Reason: "machine did not answer: connection refused"},
		},
	}

	if strings.Contains(report.Render(), "retrying in") {
		t.Errorf("a machine with no scheduled retry claims one:\n%s", report.Render())
	}
}

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

func TestRenderSummarisesInOneLine(t *testing.T) {
	rendered := testReport().Render()

	if !strings.Contains(rendered, "4 machines, 1 running this proxy forwarding 2 hostnames, 3 excluded.") {
		t.Errorf("rendered report lacks its summary line:\n%s", rendered)
	}
	for _, gone := range []string{"considered", "usual on a tailnet", "reason in the table"} {
		if strings.Contains(rendered, gone) {
			t.Errorf("the summary still carries %q:\n%s", gone, rendered)
		}
	}
}

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

func TestRenderSaysAnAbortedCycleIsNotAVerdict(t *testing.T) {
	r := &Report{
		UpdatedAt:       time.Now().UTC(),
		RefreshInterval: "1m0s",
		LocalError:      "machine did not answer: connection refused",
		Peers: []PeerReport{
			{Name: "desktop", Address: "100.64.0.1", Status: statusSkipped, Reason: "not probed this cycle"},
			{Name: "laptop", Address: "100.64.0.2", Status: statusSkipped, Reason: "not probed this cycle"},
		},
	}

	out := r.Render()

	if strings.Contains(out, "excluded") {
		t.Errorf("an aborted cycle called its machines excluded, which claims a judgement that never happened:\n%s", out)
	}
	if strings.Contains(out, "0 machines forwarding") {
		t.Errorf("an aborted cycle reported zero forwarding as a discovery result:\n%s", out)
	}
	// The reader has to be told another attempt is coming and roughly when, as
	// the status-source failure path already does.
	if !strings.Contains(out, "next attempt") || !strings.Contains(out, r.RefreshInterval) {
		t.Errorf("an aborted cycle did not say when it would try again:\n%s", out)
	}
}

func TestRenderStillSummarisesACompletedCycle(t *testing.T) {
	r := &Report{
		UpdatedAt: time.Now().UTC(),
		Peers: []PeerReport{
			{Name: "desktop", Address: "100.64.0.1", Status: statusOK, Hosts: []string{"app.loc"}},
			{Name: "phone", Address: "100.64.0.2", Status: statusSkipped, Reason: "offline"},
		},
	}

	out := r.Render()

	if !strings.Contains(out, "1 running this proxy forwarding 1 hostname") {
		t.Errorf("a completed cycle lost its counts:\n%s", out)
	}
	if !strings.Contains(out, "excluded") {
		t.Errorf("a completed cycle stopped reporting genuinely excluded machines:\n%s", out)
	}
}

// peerAt returns a machine with the given name and status, for grouping tests.
func peerAt(name, address, status string) PeerReport {
	return PeerReport{Name: name, Address: address, Status: status, Reason: "reason for " + name}
}

func TestRenderGroupsUsefulRowsFirst(t *testing.T) {
	cycle := time.Now()
	r := &Report{UpdatedAt: cycle, Source: "socket", Peers: []PeerReport{
		peerAt("zeta", "100.0.0.1", statusSkipped),
		peerAt("alpha", "100.0.0.2", statusUnreachable),
		{Name: "beta", Address: "100.0.0.3", Status: statusOK, Hosts: []string{"beta.spark.loc"}},
	}}

	out := r.Render()
	proxy := strings.Index(out, "PROXY")
	excluded := strings.Index(out, "EXCLUDED")

	if proxy < 0 || excluded < 0 {
		t.Fatalf("a group is missing:\n%s", out)
	}
	if proxy > excluded {
		t.Errorf("groups are not in order, the useful rows come first:\n%s", out)
	}
}

func TestRenderOmitsEmptyGroups(t *testing.T) {
	// A tailnet where nothing runs this proxy, which is the usual state.
	r := &Report{UpdatedAt: time.Now(), Source: "socket", Peers: []PeerReport{
		peerAt("alpha", "100.0.0.1", statusUnreachable),
	}}

	out := r.Render()
	if strings.Contains(out, "PROXY\n") {
		t.Errorf("an empty group printed its heading, which reads as a failure to find something:\n%s", out)
	}
	if !strings.Contains(out, "EXCLUDED") {
		t.Errorf("the group holding the only machine is missing:\n%s", out)
	}
}

func TestRenderSizesEachGroupOnItsOwn(t *testing.T) {
	// A very long name in one group must not pad the columns of another.
	long := "a-machine-with-a-very-long-name-indeed"
	r := &Report{UpdatedAt: time.Now(), Source: "socket", Peers: []PeerReport{
		peerAt(long, "100.0.0.1", statusOK),
		peerAt("b", "100.0.0.2", statusSkipped),
	}}

	out := r.Render()
	// Sized on its own group, the address sits just past the MACHINE header's
	// width. Sized on every row, it would sit past the long name instead.
	perGroup := 2 + len("MACHINE") + 2
	overall := 2 + len(long) + 2

	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  b ") && strings.Contains(line, "100.0.0.2") {
			at := strings.Index(line, "100.0.0.2")
			if at != perGroup {
				t.Errorf("the address is at %d, expected %d for a group sized on itself and %d if sized on every row:\n%q",
					at, perGroup, overall, line)
			}
			return
		}
	}
	t.Errorf("the short row was not found:\n%s", out)
}

func TestRenderSortsWithinAGroup(t *testing.T) {
	r := &Report{UpdatedAt: time.Now(), Source: "socket", Peers: []PeerReport{
		peerAt("charlie", "100.0.0.1", statusUnreachable),
		peerAt("alpha", "100.0.0.2", statusUnreachable),
		peerAt("bravo", "100.0.0.3", statusUnreachable),
	}}

	out := r.Render()
	a, b, c := strings.Index(out, "alpha"), strings.Index(out, "bravo"), strings.Index(out, "charlie")
	if !(a < b && b < c) {
		t.Errorf("rows within a group are not alphabetical, so a machine is hard to find:\n%s", out)
	}
}

func TestRenderKeepsAStatusNoGroupNames(t *testing.T) {
	// The summary counts every machine, so a status the groups do not name must
	// still be shown rather than disappearing from the table.
	r := &Report{UpdatedAt: time.Now(), Source: "socket", Peers: []PeerReport{
		peerAt("oddity", "100.0.0.1", "something new"),
	}}

	out := r.Render()
	if !strings.Contains(out, "oddity") {
		t.Errorf("a machine with an unrecognised status vanished from the table:\n%s", out)
	}
	if !strings.Contains(out, "EXCLUDED") {
		t.Errorf("a machine with an unrecognised status is not in the excluded group:\n%s", out)
	}
}

func TestRenderKeepsTheReasonForExcludedMachines(t *testing.T) {
	// renderSummary says the reason is in the table, so it has to be there.
	r := &Report{UpdatedAt: time.Now(), Source: "socket", Peers: []PeerReport{
		{Name: "alpha", Address: "100.0.0.1", Status: statusSkipped, Reason: "belongs to another account"},
	}}

	out := r.Render()
	if !strings.Contains(out, "another account") {
		t.Errorf("the summary promises the reason is in the table and it is not:\n%s", out)
	}
}

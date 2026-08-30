package main

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkfabrik/http-proxy/pkg/config"
	"github.com/sparkfabrik/http-proxy/pkg/traefikapi"
)

// cycleTime reads the timestamp the last completed cycle wrote.
func cycleTime(t *testing.T, path string) time.Time {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return time.Time{}
	}
	return report.UpdatedAt
}

// awaitCycleAfter waits for a cycle later than the one given.
func awaitCycleAfter(t *testing.T, path string, previous time.Time, within time.Duration) (time.Time, bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if at := cycleTime(t, path); at.After(previous) {
			return at, true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return time.Time{}, false
}

// runService starts the loop and returns once it has completed a first cycle.
func runService(t *testing.T, d *discovery, hup chan os.Signal) *sync.WaitGroup {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() { run(ctx, d, hup) })
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	if _, ran := awaitCycleAfter(t, d.config.StateFile, time.Time{}, 2*time.Second); !ran {
		t.Fatal("the service wrote no report on start")
	}
	return &wg
}

func TestASignalRunsACycleWithoutWaitingForTheInterval(t *testing.T) {
	cfg := &config.TailscaleConfig{Enabled: true, RefreshInterval: time.Hour}
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{}}
	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)

	hup := make(chan os.Signal, 1)
	runService(t, d, hup)
	first := cycleTime(t, d.config.StateFile)

	hup <- syscall.SIGHUP

	if _, ran := awaitCycleAfter(t, d.config.StateFile, first, 2*time.Second); !ran {
		t.Fatal("a signal did not run a cycle: the loop waited for its interval")
	}
}

func TestASignalResetsTheInterval(t *testing.T) {
	const interval = time.Second
	cfg := &config.TailscaleConfig{Enabled: true, RefreshInterval: interval}
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{}}
	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)

	hup := make(chan os.Signal, 1)
	runService(t, d, hup)
	first := cycleTime(t, d.config.StateFile)

	// Signalling part-way through the interval. The forced cycle must land now
	// rather than on the original schedule, and the interval must start again
	// from the forced cycle rather than run out what was left of it.
	time.Sleep(300 * time.Millisecond)
	hup <- syscall.SIGHUP

	forced, ran := awaitCycleAfter(t, d.config.StateFile, first, 200*time.Millisecond)
	if !ran {
		t.Fatal("the signalled cycle did not run promptly")
	}

	next, ran := awaitCycleAfter(t, d.config.StateFile, forced, 2*interval)
	if !ran {
		t.Fatal("the loop stopped after the signalled cycle")
	}
	if gap := next.Sub(forced); gap < 800*time.Millisecond {
		t.Fatalf("the interval was not restarted: the next cycle came %s after the forced one, which is what was left of the original interval", gap)
	}
}

func TestTheCompletedFileHoldsTheTimestampAndNothingElse(t *testing.T) {
	cfg := &config.TailscaleConfig{Enabled: true, RefreshInterval: time.Hour}
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{}}
	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)

	written := time.Now().UTC()
	if err := d.writeReport(&Report{UpdatedAt: written, Source: "socket"}); err != nil {
		t.Fatalf("writing the report: %v", err)
	}

	raw, err := os.ReadFile(completedStateFile(cfg.StateFile))
	if err != nil {
		t.Fatalf("reading the completed file: %v", err)
	}
	if got, want := string(raw), written.Format(time.RFC3339Nano); got != want {
		t.Errorf("the completed file holds %q, the CLI expects exactly %q", got, want)
	}
}

func TestTheCompletedFileDoesNotAdvanceWhenAnEarlierWriteFails(t *testing.T) {
	cfg := &config.TailscaleConfig{Enabled: true, RefreshInterval: time.Hour}
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{}}
	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)

	first := time.Now().UTC()
	if err := d.writeReport(&Report{UpdatedAt: first, Source: "socket"}); err != nil {
		t.Fatalf("writing the first report: %v", err)
	}

	// A rendered path that cannot be written, so the report write fails while
	// the state file succeeds: the barrier must not move past it.
	if err := os.Remove(renderedStateFile(cfg.StateFile)); err != nil {
		t.Fatalf("clearing the rendered report: %v", err)
	}
	if err := os.Mkdir(renderedStateFile(cfg.StateFile), 0o755); err != nil {
		t.Fatalf("blocking the rendered report: %v", err)
	}

	second := first.Add(time.Minute)
	if err := d.writeReport(&Report{UpdatedAt: second, Source: "socket"}); err == nil {
		t.Fatal("writing the report succeeded with an unwritable rendered path")
	}

	raw, err := os.ReadFile(completedStateFile(cfg.StateFile))
	if err != nil {
		t.Fatalf("reading the completed file: %v", err)
	}
	if got := string(raw); got != first.Format(time.RFC3339Nano) {
		t.Errorf("the barrier advanced to %q while the report was never written", got)
	}
}

func TestAFailedLocalReadIsRetriedWithoutWaitingTheInterval(t *testing.T) {
	// The local API is unreachable here, which is the startup race: Traefik is
	// not listening yet, so the cycle aborts and nothing is probed.
	cfg := &config.TailscaleConfig{Enabled: true, RefreshInterval: time.Hour}
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{}}
	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)

	hup := make(chan os.Signal, 1)
	runService(t, d, hup)
	first := cycleTime(t, d.config.StateFile)

	if _, ran := awaitCycleAfter(t, d.config.StateFile, first, 10*time.Second); !ran {
		t.Fatal("a cycle that could not read the local routing table waited the full interval before trying again")
	}
}

func TestACompletedCycleWaitsTheInterval(t *testing.T) {
	const interval = time.Second
	cfg := &config.TailscaleConfig{Enabled: true, RefreshInterval: interval, LocalAPIURL: "http://local"}
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{"http://local": {}}}
	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)

	hup := make(chan os.Signal, 1)
	runService(t, d, hup)
	first := cycleTime(t, d.config.StateFile)

	next, ran := awaitCycleAfter(t, d.config.StateFile, first, 3*interval)
	if !ran {
		t.Fatal("the loop stopped after a completed cycle")
	}
	if gap := next.Sub(first); gap < interval-100*time.Millisecond {
		t.Fatalf("a completed cycle was retried early, after %s, so the fast retry is not limited to failures", gap)
	}
}

func TestTheBackoffStaysBoundedHoweverLongTheFailureLasts(t *testing.T) {
	// The last of these is the largest duration there is. An interval near it
	// leaves room for the carried backoff to overflow while still being below
	// the interval, which a one-minute interval never reveals.
	for _, interval := range []time.Duration{time.Minute, time.Hour, 24 * time.Hour, math.MaxInt64} {
		retry := localRetryInitial

		// Far beyond the point where doubling a time.Duration overflows int64,
		// which it does after 34 consecutive failures from a two second start.
		for cycle := 1; cycle <= 1000; cycle++ {
			var wait time.Duration
			wait, retry = nextWait(true, retry, interval)

			if wait <= 0 {
				t.Fatalf("interval %v, cycle %d: wait is %v, so the timer fires at once and the loop spins", interval, cycle, wait)
			}
			if wait > interval {
				t.Fatalf("interval %v, cycle %d: wait is %v, longer than the interval", interval, cycle, wait)
			}
		}
	}
}

func TestTheBackoffStartsOverAfterASuccess(t *testing.T) {
	const interval = time.Minute
	retry := localRetryInitial
	for range 5 {
		_, retry = nextWait(true, retry, interval)
	}

	wait, next := nextWait(false, retry, interval)
	if wait != interval {
		t.Errorf("a completed cycle waited %v rather than the interval %v", wait, interval)
	}
	if next != localRetryInitial {
		t.Errorf("the backoff carried %v into the next failure instead of starting over", next)
	}
}

func readSummary(t *testing.T, stateFile string) string {
	t.Helper()
	raw, err := os.ReadFile(summaryStateFile(stateFile))
	if err != nil {
		t.Fatalf("reading the summary: %v", err)
	}
	return string(raw)
}

func TestTheSummaryDescribesACompletedCycle(t *testing.T) {
	cfg := &config.TailscaleConfig{Enabled: true, RefreshInterval: time.Hour}
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{}}
	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)

	report := &Report{
		UpdatedAt: time.Now().UTC(),
		Peers: []PeerReport{
			{Name: "desktop", Address: "100.64.0.1", Status: statusOK, Hosts: []string{"app.loc", "api.loc"}},
			{Name: "phone", Address: "100.64.0.2", Status: statusSkipped, Reason: "offline"},
		},
	}
	if err := d.writeReport(report); err != nil {
		t.Fatalf("writing the report: %v", err)
	}

	want := "ok\n2 1\ndesktop\tapp.loc,api.loc\n"
	if got := readSummary(t, cfg.StateFile); got != want {
		t.Errorf("summary is\n%q\nwant\n%q", got, want)
	}
}

func TestASummaryWithNothingForwardedIsStillOk(t *testing.T) {
	cfg := &config.TailscaleConfig{Enabled: true, RefreshInterval: time.Hour}
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{}}
	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)

	report := &Report{
		UpdatedAt: time.Now().UTC(),
		Peers: []PeerReport{
			{Name: "phone", Address: "100.64.0.2", Status: statusSkipped, Reason: "offline"},
		},
	}
	if err := d.writeReport(report); err != nil {
		t.Fatalf("writing the report: %v", err)
	}

	// Nothing forwarded is the usual state on a tailnet with one proxy, and it
	// is a completed cycle rather than a state of its own.
	want := "ok\n1 0\n"
	if got := readSummary(t, cfg.StateFile); got != want {
		t.Errorf("summary is\n%q\nwant\n%q", got, want)
	}
}

func TestAnAbortedCycleKeepsWhatIsStillInPlace(t *testing.T) {
	cfg := &config.TailscaleConfig{Enabled: true, RefreshInterval: time.Hour}
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{}}
	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)

	forwarding := &Report{
		UpdatedAt: time.Now().UTC(),
		Peers: []PeerReport{
			{Name: "desktop", Address: "100.64.0.1", Status: statusOK, Hosts: []string{"app.loc"}},
		},
	}
	if err := d.writeReport(forwarding); err != nil {
		t.Fatalf("writing the first report: %v", err)
	}

	aborted := &Report{
		UpdatedAt:  time.Now().UTC().Add(time.Minute),
		LocalError: "machine did not answer: connection refused",
		Peers: []PeerReport{
			{Name: "desktop", Address: "100.64.0.1", Status: statusSkipped, Reason: "not probed this cycle"},
		},
	}
	if err := d.writeReport(aborted); err != nil {
		t.Fatalf("writing the aborted report: %v", err)
	}

	// Those routes are still in place, so only the token changes.
	want := "aborted\n1 1\ndesktop\tapp.loc\n"
	if got := readSummary(t, cfg.StateFile); got != want {
		t.Errorf("summary is\n%q\nwant\n%q", got, want)
	}
}

func TestTheBarrierDoesNotAdvanceWhenTheSummaryCannotBeWritten(t *testing.T) {
	cfg := &config.TailscaleConfig{Enabled: true, RefreshInterval: time.Hour}
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{}}
	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)

	first := time.Now().UTC()
	if err := d.writeReport(&Report{UpdatedAt: first}); err != nil {
		t.Fatalf("writing the first report: %v", err)
	}

	// A summary path that cannot be written, so the barrier must not move past
	// a cycle whose summary never landed.
	if err := os.Remove(summaryStateFile(cfg.StateFile)); err != nil {
		t.Fatalf("clearing the summary: %v", err)
	}
	if err := os.Mkdir(summaryStateFile(cfg.StateFile), 0o755); err != nil {
		t.Fatalf("blocking the summary: %v", err)
	}

	if err := d.writeReport(&Report{UpdatedAt: first.Add(time.Minute)}); err == nil {
		t.Fatal("writing the report succeeded with an unwritable summary path")
	}

	raw, err := os.ReadFile(completedStateFile(cfg.StateFile))
	if err != nil {
		t.Fatalf("reading the completed file: %v", err)
	}
	if got := string(raw); got != first.Format(time.RFC3339Nano) {
		t.Errorf("the barrier advanced to %q while the summary was never written", got)
	}
}

func TestAnUnreadableSummaryIsNotReplacedWithZeroes(t *testing.T) {
	cfg := &config.TailscaleConfig{Enabled: true, RefreshInterval: time.Hour}
	probe := &fakeProbe{routes: map[string][]traefikapi.Route{}}
	d := testDiscovery(t, cfg, fakeSource{document: tailnetStatus}, probe)

	forwarding := &Report{
		UpdatedAt: time.Now().UTC(),
		Peers: []PeerReport{
			{Name: "desktop", Address: "100.64.0.1", Status: statusOK, Hosts: []string{"app.loc"}},
		},
	}
	if err := d.writeReport(forwarding); err != nil {
		t.Fatalf("writing the first report: %v", err)
	}

	// Unreadable rather than absent: a transient I/O or permission failure must
	// not be taken for "there were no routes".
	path := summaryStateFile(cfg.StateFile)
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("making the summary unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	aborted := &Report{UpdatedAt: time.Now().UTC().Add(time.Minute), LocalError: "connection refused"}
	err := d.writeReport(aborted)

	if err == nil {
		_ = os.Chmod(path, 0o644)
		raw, _ := os.ReadFile(path)
		t.Fatalf("an unreadable summary was replaced rather than reported, leaving:\n%q", string(raw))
	}
}

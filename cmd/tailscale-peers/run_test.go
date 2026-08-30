package main

import (
	"context"
	"encoding/json"
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

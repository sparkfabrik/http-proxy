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

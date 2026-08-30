// Package main implements tailnet peer discovery: it asks a status source which
// machines belong to this user, reads the routing table each one publishes, and
// forwards a hostname this machine does not serve to the machine that does.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sparkfabrik/http-proxy/pkg/config"
	"github.com/sparkfabrik/http-proxy/pkg/logger"
	"github.com/sparkfabrik/http-proxy/pkg/tailscale"
)

func main() {
	cfg := config.LoadTailscale()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid configuration: %v\n", err)
		os.Exit(1)
	}

	log := logger.NewWithEnv("tailscale-peers")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()

	source, err := newSource(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to select a peer source: %v\n", err)
		os.Exit(1)
	}

	d := newDiscovery(cfg, log, source, httpProbe)

	// Clears the routes a previous enabled run left behind.
	if !cfg.Enabled {
		log.Info("Peer routing is disabled",
			"hint", "set HTTP_PROXY_TAILSCALE_ENABLED=true to enable it")
		if err := d.reconcileConfigs(nil); err != nil {
			log.Error("Failed to remove peer configuration", "error", err)
		}
		if err := d.writeReport(&Report{UpdatedAt: time.Now().UTC(), Source: cfg.Source}); err != nil {
			log.Error("Failed to write the peer report", "error", err)
		}
		<-ctx.Done()
		return
	}

	log.Info("Starting peer discovery",
		"source", cfg.Source,
		"refresh_interval", cfg.RefreshInterval.String(),
		"status_max_age", cfg.StatusMaxAge.String())

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	run(ctx, d, hup)

	// Withdraws every route on the way out.
	if err := d.reconcileConfigs(nil); err != nil {
		log.Error("Failed to withdraw peer routes on shutdown", "error", err)
	}
	log.Info("Shutting down gracefully")
}

// newSource builds the source of tailnet status documents.
func newSource(cfg *config.TailscaleConfig) (tailscale.Source, error) {
	switch cfg.Source {
	case config.TailscaleSourceSocket:
		return tailscale.NewSocketSource(cfg.TailscaleSocket, probeTimeout), nil
	case config.TailscaleSourceFile:
		return tailscale.NewFileSource(cfg.StatusFile, cfg.StatusMaxAge), nil
	default:
		return nil, fmt.Errorf("unknown peer source %q", cfg.Source)
	}
}

// The first wait after a cycle that could not read the local routing table.
const localRetryInitial = 2 * time.Second

// nextWait returns how long to wait after a cycle and the backoff to carry.
func nextWait(failed bool, retry, interval time.Duration) (wait, next time.Duration) {
	if !failed {
		return interval, localRetryInitial
	}
	// Never double past the interval: an unbounded retry overflows int64
	// nanoseconds after 34 failures and turns the wait negative, which fires
	// the timer at once and spins the loop.
	next = interval
	if retry < interval/2 {
		next = retry * 2
	}
	return min(retry, interval), next
}

// run polls until the context is cancelled, and runs a cycle at once on a signal.
func run(ctx context.Context, d *discovery, hup <-chan os.Signal) {
	timer := time.NewTimer(d.config.RefreshInterval)
	defer timer.Stop()

	retry := localRetryInitial

	for {
		report := d.runCycle(ctx)
		if err := d.writeReport(report); err != nil {
			d.logger.Error("Failed to write the peer report", "error", err, "state_file", d.config.StateFile)
		}

		// A cycle that read nothing locally probed nothing, so it is retried
		// before the interval rather than after it. A forced cycle does not
		// clear the backoff: the signal does not fix whatever is failing, and a
		// cycle that succeeds clears it anyway.
		var wait time.Duration
		wait, retry = nextWait(report.LocalError != "", retry, d.config.RefreshInterval)

		timer.Stop()
		timer.Reset(wait)

		select {
		case <-ctx.Done():
			return
		case <-hup:
		case <-timer.C:
		}
	}
}

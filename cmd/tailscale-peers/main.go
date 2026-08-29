// Package main implements tailnet peer discovery. It asks a tailnet status source
// which machines belong to this user, reads the routing table each one already
// publishes, and writes Traefik dynamic configuration forwarding a hostname
// this machine does not serve to the machine that does.
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

	// Disabled is not the same as not running: the service still clears the
	// routes a previous enabled run left in the shared volume, so disabling it
	// and restarting leaves no forwarded hostnames behind.
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

	run(ctx, d)

	// Withdraw on the way out. Stopping this service has to stop the
	// forwarding it set up, without waiting for the proxy to restart: the
	// entrypoint's cleanup only runs at startup, so a forwarded hostname would
	// otherwise stay reachable until then.
	if err := d.reconcileConfigs(nil); err != nil {
		log.Error("Failed to withdraw peer routes on shutdown", "error", err)
	}
	log.Info("Shutting down gracefully")
}

// newSource builds the source of tailnet status documents. Both sources produce
// the same document, so the ownership filter downstream is one implementation.
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

// run polls until the context is cancelled. The trigger for a change is a
// container starting on another machine, which no local event stream observes,
// so the interval is the only way to notice it.
func run(ctx context.Context, d *discovery) {
	ticker := time.NewTicker(d.config.RefreshInterval)
	defer ticker.Stop()

	for {
		report := d.runCycle(ctx)
		if err := d.writeReport(report); err != nil {
			d.logger.Error("Failed to write the peer report", "error", err, "state_file", d.config.StateFile)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

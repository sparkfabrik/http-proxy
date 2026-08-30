package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sparkfabrik/http-proxy/pkg/config"
	"github.com/sparkfabrik/http-proxy/pkg/logger"
	"github.com/sparkfabrik/http-proxy/pkg/tailscale"
	"github.com/sparkfabrik/http-proxy/pkg/traefikapi"
	"gopkg.in/yaml.v3"
)

const (
	// peerAPIPort is where a proxy publishes its read-only routing table.
	peerAPIPort = 30000

	// peerForwardPort is the plain entrypoint a forwarded request is sent to.
	peerForwardPort = 80

	probeTimeout = 2 * time.Second

	// maxProbeBackoff caps the wait, which runs 1, 2, 4, 8 then 15 minutes.
	maxProbeBackoff = 15 * time.Minute

	// Traefik has to read the dynamic directory.
	configFilePermissions = 0644
	configDirPermissions  = 0755

	// Private directory, readable files.
	stateFilePermissions = 0644
	stateDirPermissions  = 0700
)

// Outcomes reported for a machine that discovery considered.
const (
	statusOK          = "ok"
	statusNoProxy     = "no proxy"
	statusUndeclared  = "not this proxy"
	statusUnreachable = "unreachable"
	statusSkipped     = "skipped"
)

// localOwner names the local machine in a collision report.
const localOwner = "local"

// probeFunc reads the routable routes of the proxy answering at baseURL.
type probeFunc func(ctx context.Context, baseURL string) (traefikapi.Result, error)

// candidate is a machine about to be probed, or the reason it is not, keyed by
// node key, which is unique per machine.
type candidate struct {
	id         string
	name       string
	address    string
	skipReason string
}

// probeState is what is known about a machine between cycles.
type probeState struct {
	failures    int
	nextAttempt time.Time
	status      string
	reason      string
}

// Report is the outcome of one cycle, written where the command line reads it.
type Report struct {
	UpdatedAt       time.Time `json:"updatedAt"`
	Enabled         bool      `json:"enabled"`
	RefreshInterval string    `json:"refreshInterval"`
	Source          string    `json:"source"`
	// SourceUpdatedAt is when the status document the cycle used was produced.
	SourceUpdatedAt time.Time    `json:"sourceUpdatedAt,omitzero"`
	SourceError     string       `json:"sourceError,omitempty"`
	LocalError      string       `json:"localError,omitempty"`
	Peers           []PeerReport `json:"peers"`
	Collisions      []Collision  `json:"collisions,omitempty"`
}

// PeerReport is one machine's contribution, or why it made none.
type PeerReport struct {
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	// RetryAt is always after the cycle that produced the report.
	RetryAt time.Time `json:"retryAt,omitzero"`
	Hosts   []string  `json:"hosts,omitempty"`
	// RejectedRules holds rules refused for not being host-constrained.
	RejectedRules []string `json:"rejectedRules,omitempty"`
}

// Collision names the machine serving a hostname and the one that also offered it.
type Collision struct {
	Host     string `json:"host"`
	ServedBy string `json:"servedBy"`
	AlsoOn   string `json:"alsoOn"`
}

// discovery holds one cycle's collaborators and the state that outlives it.
type discovery struct {
	config *config.TailscaleConfig
	logger *logger.Logger
	source tailscale.Source
	probe  probeFunc
	states map[string]*probeState
	now    func() time.Time

	sourceFailing bool
}

func newDiscovery(cfg *config.TailscaleConfig, log *logger.Logger, source tailscale.Source, probe probeFunc) *discovery {
	return &discovery{
		config: cfg,
		logger: log,
		source: source,
		probe:  probe,
		states: make(map[string]*probeState),
		now:    time.Now,
	}
}

// httpProbe asks the machine what it is, then reads its routing table.
func httpProbe(ctx context.Context, baseURL string) (traefikapi.Result, error) {
	client := traefikapi.NewWithTimeout(baseURL, probeTimeout)

	declared, err := client.Declares(ctx)
	if err != nil {
		return traefikapi.Result{}, err
	}
	if !declared {
		return traefikapi.Result{}, traefikapi.ErrUndeclared
	}

	routers, err := client.Routers(ctx)
	if err != nil {
		return traefikapi.Result{}, err
	}
	return traefikapi.Routes(routers), nil
}

// runCycle discovers, probes, resolves ownership and rewrites the configuration.
func (d *discovery) runCycle(ctx context.Context) *Report {
	report := &Report{
		UpdatedAt:       d.now().UTC(),
		Enabled:         true,
		RefreshInterval: d.config.RefreshInterval.String(),
		Source:          d.config.Source,
	}

	candidates, sourceUpdatedAt, sourceErr := d.discover(ctx)
	report.SourceUpdatedAt = sourceUpdatedAt
	if sourceErr != nil {
		report.SourceError = sourceErr.Error()
	}

	// Writes nothing and keeps the previous files without the local table.
	local, err := d.localHosts(ctx)
	if err != nil {
		report.LocalError = err.Error()
		d.logger.Error("Failed to read the local routing table, keeping the previous peer routes",
			"error", err, "url", d.config.LocalAPIURL)
		report.Peers = candidateReports(candidates)
		return report
	}

	probed := d.probeCandidates(ctx, candidates)
	owned, collisions := resolveOwnership(probed, local)

	keep := make(map[string]struct{}, len(owned))
	for _, result := range owned {
		if len(result.routes) == 0 || result.slug == "" {
			continue
		}
		fileName := peerConfigFileName(result.slug)

		// Reserved before the write: a failed write must not withdraw the last
		// good configuration.
		keep[fileName] = struct{}{}

		if err := d.writeConfig(fileName, peerTraefikConfig(result.slug, result.address, result.routes)); err != nil {
			d.logger.Error("Failed to write peer configuration", "error", err, "peer", result.name)
		}
	}

	if err := d.reconcileConfigs(keep); err != nil {
		d.logger.Error("Failed to reconcile peer configuration", "error", err)
	}

	report.Peers = peerReports(probed, owned)
	report.Collisions = collisions
	return report
}

// discover returns the machines to consider and when their document was
// produced, and returns none when the source produces no document.
func (d *discovery) discover(ctx context.Context) ([]candidate, time.Time, error) {
	status, err := d.source.Status(ctx)
	if err != nil {
		d.reportSourceFailure(err)
		return nil, time.Time{}, err
	}
	d.reportSourceRecovery()

	peers := status.Peers()
	candidates := make([]candidate, 0, len(peers))
	for _, peer := range peers {
		candidates = append(candidates, candidate{
			id:         peer.ID,
			name:       peer.Name,
			address:    peer.Address,
			skipReason: peer.SkipReason,
		})
	}
	return sortCandidates(candidates), status.UpdatedAt.UTC(), nil
}

// reportSourceFailure logs a failing source once, then stops repeating it.
func (d *discovery) reportSourceFailure(err error) {
	if d.sourceFailing {
		d.logger.Debug("Peer source is still failing", "error", err)
		return
	}
	d.sourceFailing = true
	d.logger.Error("Failed to read the tailnet status, contributing no peer routes this cycle",
		"error", err, "source", d.config.Source)
}

func (d *discovery) reportSourceRecovery() {
	if !d.sourceFailing {
		return
	}
	d.sourceFailing = false
	d.logger.Info("Peer discovery recovered", "source", d.config.Source)
}

// sortCandidates orders machines by name, then by identity.
func sortCandidates(candidates []candidate) []candidate {
	slices.SortStableFunc(candidates, func(a, b candidate) int {
		return cmp.Or(cmp.Compare(a.name, b.name), cmp.Compare(a.id, b.id))
	})
	return candidates
}

// probeResult is what one machine gave this cycle.
type probeResult struct {
	candidate
	status   string
	reason   string
	retryAt  time.Time
	routes   []traefikapi.Route
	rejected []string
}

func (d *discovery) probeCandidates(ctx context.Context, candidates []candidate) []probeResult {
	results := make([]probeResult, 0, len(candidates))
	for _, c := range candidates {
		results = append(results, d.probeCandidate(ctx, c))
	}
	return results
}

func (d *discovery) probeCandidate(ctx context.Context, c candidate) probeResult {
	if c.skipReason != "" {
		delete(d.states, c.id)
		return probeResult{candidate: c, status: statusSkipped, reason: c.skipReason}
	}

	state := d.states[c.id]
	if state != nil && d.now().Before(state.nextAttempt) {
		return probeResult{
			candidate: c,
			status:    state.status,
			reason:    state.reason,
			retryAt:   state.nextAttempt.UTC(),
		}
	}

	result, err := d.probe(ctx, fmt.Sprintf("http://%s:%d", c.address, peerAPIPort))
	if err != nil {
		status := d.recordFailure(c, err)
		return probeResult{candidate: c, status: status, reason: err.Error(), retryAt: d.states[c.id].nextAttempt.UTC()}
	}

	for _, rule := range result.Rejected {
		d.logger.Warn("Refusing a peer rule that is not constrained by a hostname",
			"peer", c.name, "address", c.address, "rule", rule,
			"hint", "every alternative of the rule has to name a host, or it would match requests this machine serves")
	}

	delete(d.states, c.id)
	return probeResult{candidate: c, status: statusOK, routes: result.Routes, rejected: result.Rejected}
}

// recordFailure pushes a failing machine further out and reports its status.
func (d *discovery) recordFailure(c candidate, err error) string {
	status := statusNoProxy
	switch {
	case errors.Is(err, traefikapi.ErrUnreachable):
		status = statusUnreachable
	case errors.Is(err, traefikapi.ErrUndeclared):
		status = statusUndeclared
	}

	state := d.states[c.id]
	if state == nil {
		state = &probeState{}
		d.states[c.id] = state
	}
	state.failures++
	state.status = status
	state.reason = err.Error()
	state.nextAttempt = d.now().Add(backoff(d.config.RefreshInterval, state.failures))

	d.logger.Debug("Peer probe failed",
		"peer", c.name, "address", c.address, "status", status, "failures", state.failures, "error", err)

	return status
}

// backoff doubles the interval per consecutive failure, up to a ceiling.
func backoff(interval time.Duration, failures int) time.Duration {
	delay := interval
	for range failures - 1 {
		delay *= 2
		if delay >= maxProbeBackoff {
			return maxProbeBackoff
		}
	}
	return delay
}

// ownedRoutes is what one machine keeps after ownership is resolved.
type ownedRoutes struct {
	id      string
	name    string
	slug    string
	address string
	hosts   []string
	routes  []traefikapi.Route
}

// owner is the machine holding a hostname.
type owner struct {
	id   string
	name string
}

// resolveOwnership drops routes for a hostname already served and reports each
// drop, giving it to the local machine, then to the first peer in name order.
func resolveOwnership(results []probeResult, local *localRoutes) ([]ownedRoutes, []Collision) {
	owners := make(map[string]owner)
	slugs := make(map[string]string)
	owned := make([]ownedRoutes, 0, len(results))
	var collisions []Collision

	for _, result := range results {
		if result.status != statusOK {
			continue
		}

		machine := ownedRoutes{
			id:      result.id,
			name:    result.name,
			slug:    uniqueSlug(slugs, result.id, result.name, result.address),
			address: result.address,
		}

		for _, route := range result.routes {
			if host, holder, taken := claimedElsewhere(route.Hosts, local, owners, result.id); taken {
				collisions = append(collisions, Collision{Host: host, ServedBy: holder, AlsoOn: result.name})
				continue
			}
			for _, host := range route.Hosts {
				if _, claimed := owners[host.Value]; !claimed {
					owners[host.Value] = owner{id: result.id, name: result.name}
					machine.hosts = append(machine.hosts, host.Value)
				}
			}
			machine.routes = append(machine.routes, route)
		}

		owned = append(owned, machine)
	}

	return owned, collisions
}

// claimedElsewhere reports the first hostname another machine already serves,
// counting a machine's own second claim as its own.
func claimedElsewhere(hosts []traefikapi.Host, local *localRoutes, owners map[string]owner, machineID string) (string, string, bool) {
	for _, host := range hosts {
		if local != nil && local.serves(host) {
			return host.Value, localOwner, true
		}
		if holder, claimed := owners[host.Value]; claimed && holder.id != machineID {
			return host.Value, holder.name, true
		}
	}
	return "", "", false
}

// localRoutes is what this machine serves: literal hostnames and compiled
// wildcards.
type localRoutes struct {
	literals map[string]struct{}
	patterns []*regexp.Regexp
	// patternText matches a peer offering the same wildcard verbatim.
	patternText map[string]struct{}
}

// serves reports whether this machine answers for a hostname a peer claims,
// literal or wildcard on either side.
func (l *localRoutes) serves(host traefikapi.Host) bool {
	if host.IsRegexp {
		if _, taken := l.patternText[host.Value]; taken {
			return true
		}
		// Matches the pattern against each hostname.
		pattern, err := compileHostPattern(host.Value)
		if err != nil {
			return false
		}
		for literal := range l.literals {
			if pattern.MatchString(literal) {
				return true
			}
		}
		return false
	}

	if _, taken := l.literals[host.Value]; taken {
		return true
	}
	for _, pattern := range l.patterns {
		if pattern.MatchString(host.Value) {
			return true
		}
	}
	return false
}

// compileHostPattern compiles a peer-supplied pattern, refusing an absurd one.
func compileHostPattern(pattern string) (*regexp.Regexp, error) {
	if len(pattern) > traefikapi.MaxHostPatternLength {
		return nil, fmt.Errorf("host pattern is longer than %d characters", traefikapi.MaxHostPatternLength)
	}
	return regexp.Compile(pattern)
}

// localHosts returns what the local containers serve, read from the local
// proxy's API, without the routes a previous cycle forwarded.
func (d *discovery) localHosts(ctx context.Context) (*localRoutes, error) {
	result, err := d.probe(ctx, d.config.LocalAPIURL)
	if err != nil {
		return nil, err
	}

	local := &localRoutes{
		literals:    make(map[string]struct{}),
		patternText: make(map[string]struct{}),
	}

	for _, route := range result.Routes {
		for _, host := range route.Hosts {
			if !host.IsRegexp {
				local.literals[host.Value] = struct{}{}
				continue
			}
			local.patternText[host.Value] = struct{}{}
			pattern, err := compileHostPattern(host.Value)
			if err != nil {
				d.logger.Warn("Ignoring a local wildcard hostname that will not compile",
					"pattern", host.Value, "error", err)
				continue
			}
			local.patterns = append(local.patterns, pattern)
		}
	}

	return local, nil
}

// peerTraefikConfig builds one machine's forwarding configuration: one service,
// and a router per entrypoint for each route.
func peerTraefikConfig(slug, address string, routes []traefikapi.Route) *config.TraefikConfig {
	traefikConfig := config.NewTraefikConfig()
	serviceName := traefikapi.PeerRouterPrefix + slug

	for i, route := range routes {
		// Copies the peer's rule and priority unchanged.
		traefikConfig.HTTP.Routers[fmt.Sprintf("%s-%d", serviceName, i)] = &config.Router{
			Rule:        route.Rule,
			Service:     serviceName,
			Priority:    route.Priority,
			EntryPoints: []string{"http"},
		}
		traefikConfig.HTTP.Routers[fmt.Sprintf("%s-tls-%d", serviceName, i)] = &config.Router{
			Rule:        route.Rule,
			Service:     serviceName,
			Priority:    route.Priority,
			EntryPoints: []string{"https"},
			TLS:         &config.RouterTLSConfig{},
		}
	}

	traefikConfig.HTTP.Services[serviceName] = &config.Service{
		LoadBalancer: &config.LoadBalancer{
			Servers: []config.Server{
				{URL: fmt.Sprintf("http://%s:%d", address, peerForwardPort)},
			},
		},
	}

	return traefikConfig
}

var unsafeSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

// peerSlug reduces a name to characters safe in a file and router name.
func peerSlug(name string) string {
	slug := strings.Trim(unsafeSlugChars.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if slug == "" {
		return "peer"
	}
	return slug
}

// uniqueSlug gives each machine a distinct slug, falling back to its address.
func uniqueSlug(taken map[string]string, id, name, address string) string {
	candidates := []string{peerSlug(name), peerSlug(name) + "-" + peerSlug(address)}

	// Bounded by the machines already holding a slug.
	for i := 2; i <= len(taken)+2; i++ {
		candidates = append(candidates, fmt.Sprintf("%s-%s-%d", peerSlug(name), peerSlug(address), i))
	}

	for _, slug := range candidates {
		if holder, used := taken[slug]; !used || holder == id {
			taken[slug] = id
			return slug
		}
	}

	// Unreachable with the bound above.
	return ""
}

func peerConfigFileName(slug string) string {
	return fmt.Sprintf("%s%s.yaml", traefikapi.PeerRouterPrefix, slug)
}

// peerConfigFilePattern matches the files this service owns. It cannot match
// the per-container layer's twelve-character hexadecimal names.
var peerConfigFilePattern = regexp.MustCompile(`^tailscale-peer-[a-z0-9-]+\.yaml$`)

func isPeerConfigFile(name string) bool {
	return peerConfigFilePattern.MatchString(name)
}

func (d *discovery) writeConfig(fileName string, cfg *config.TraefikConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal peer configuration: %w", err)
	}
	if err := os.MkdirAll(d.config.TraefikDynamicDir, configDirPermissions); err != nil {
		return fmt.Errorf("failed to create the traefik dynamic directory: %w", err)
	}
	return writeFileAtomically(filepath.Join(d.config.TraefikDynamicDir, fileName), data, configFilePermissions)
}

// writeReport records a cycle outside the directory the proxy watches.
func (d *discovery) writeReport(report *Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal the peer report: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(d.config.StateFile), stateDirPermissions); err != nil {
		return fmt.Errorf("failed to create the state directory: %w", err)
	}
	if err := writeFileAtomically(d.config.StateFile, data, stateFilePermissions); err != nil {
		return err
	}
	if err := writeFileAtomically(renderedStateFile(d.config.StateFile), []byte(report.Render()), stateFilePermissions); err != nil {
		return err
	}
	if err := d.writeSummary(report); err != nil {
		return err
	}
	// Written last, so a reader that sees it change knows the rest is on disk.
	return writeFileAtomically(completedStateFile(d.config.StateFile),
		[]byte(report.UpdatedAt.Format(time.RFC3339Nano)), stateFilePermissions)
}

// writeFileAtomically writes through a temporary file and a rename.
// writeSummary records what `status` reads: a state token, three counts, and a
// line per forwarding machine. A cycle that probed nothing keeps the previous
// counts and machines, because those routes are still in place.
func (d *discovery) writeSummary(report *Report) error {
	path := summaryStateFile(d.config.StateFile)

	if report.LocalError != "" {
		previous, err := os.ReadFile(path)
		if err != nil {
			return writeFileAtomically(path, []byte("aborted\n0 0 0 0\n"), stateFilePermissions)
		}
		return writeFileAtomically(path, retokenise(previous, "aborted"), stateFilePermissions)
	}

	var b strings.Builder
	var probed, forwarding, hostnames int
	var machines strings.Builder
	for _, peer := range report.Peers {
		// A skipped machine was never contacted, so nothing about it is known.
		if peer.Status != statusSkipped {
			probed++
		}
		if peer.Status != statusOK || len(peer.Hosts) == 0 {
			continue
		}
		forwarding++
		hostnames += len(peer.Hosts)
		fmt.Fprintf(&machines, "%s\t%s\n", peer.Name, strings.Join(peer.Hosts, ","))
	}

	b.WriteString("ok\n")
	fmt.Fprintf(&b, "%d %d %d %d\n", len(report.Peers), probed, forwarding, hostnames)
	b.WriteString(machines.String())

	return writeFileAtomically(path, []byte(b.String()), stateFilePermissions)
}

// retokenise replaces the state token on the first line, keeping the rest.
func retokenise(summary []byte, token string) []byte {
	_, rest, found := strings.Cut(string(summary), "\n")
	if !found {
		return []byte(token + "\n")
	}
	return []byte(token + "\n" + rest)
}

func writeFileAtomically(path string, data []byte, permissions os.FileMode) error {
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, permissions); err != nil {
		return fmt.Errorf("failed to write %s: %w", temp, err)
	}
	if err := os.Rename(temp, path); err != nil {
		os.Remove(temp)
		return fmt.Errorf("failed to rename %s: %w", temp, err)
	}
	return nil
}

// reconcileConfigs removes the files of machines that contributed nothing.
func (d *discovery) reconcileConfigs(keep map[string]struct{}) error {
	entries, err := os.ReadDir(d.config.TraefikDynamicDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read the traefik dynamic directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isPeerConfigFile(name) {
			continue
		}
		if _, ok := keep[name]; ok {
			continue
		}

		if err := os.Remove(filepath.Join(d.config.TraefikDynamicDir, name)); err != nil {
			d.logger.Error("Failed to remove stale peer configuration", "error", err, "config_file", name)
			continue
		}
		d.logger.Info("Removed stale peer configuration", "config_file", name)
	}

	return nil
}

// candidateReports describes machines not probed this cycle.
func candidateReports(candidates []candidate) []PeerReport {
	reports := make([]PeerReport, 0, len(candidates))
	for _, c := range candidates {
		status := statusSkipped
		reason := c.skipReason
		if reason == "" {
			reason = "not probed this cycle"
		}
		reports = append(reports, PeerReport{Name: c.name, Address: c.address, Status: status, Reason: reason})
	}
	return reports
}

// peerReports pairs what each machine answered with what it contributed.
func peerReports(results []probeResult, owned []ownedRoutes) []PeerReport {
	hosts := make(map[string][]string, len(owned))
	for _, machine := range owned {
		hosts[machine.id] = machine.hosts
	}

	reports := make([]PeerReport, 0, len(results))
	for _, result := range results {
		reports = append(reports, PeerReport{
			Name:          result.name,
			Address:       result.address,
			Status:        result.status,
			Reason:        result.reason,
			RetryAt:       result.retryAt,
			Hosts:         hosts[result.id],
			RejectedRules: result.rejected,
		})
	}
	return reports
}

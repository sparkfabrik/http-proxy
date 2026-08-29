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
	// The hop travels inside the tailnet and the peer terminates TLS for its
	// own containers, so no certificate authority is shared between machines.
	peerForwardPort = 80

	// probeTimeout bounds a single routing-table read. Most machines on a
	// tailnet run no proxy, so this is the common path rather than the
	// exceptional one.
	probeTimeout = 2 * time.Second

	// maxProbeBackoff caps how far a repeatedly failing machine is pushed out.
	// With the default cycle the waits run 1, 2, 4, 8 then 15 minutes, so a
	// machine that starts running the proxy is found within a quarter of an hour.
	maxProbeBackoff = 15 * time.Minute

	// The Traefik dynamic directory is a volume Traefik has to read, so its
	// files stay readable.
	configFilePermissions = 0644
	configDirPermissions  = 0755

	// The state directory is a trust input rather than a scratch area: the
	// status document in it decides which machines traffic is forwarded to, and
	// the report names every machine on the tailnet with its address and the
	// hostnames it serves. Owner only, whichever of the CLI and this service
	// creates it first.
	stateFilePermissions = 0600
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

// localOwner names the local machine in a collision report, where the other
// side of the collision is named by its Tailscale hostname.
const localOwner = "local"

// probeFunc reads the routable routes of the proxy answering at baseURL.
type probeFunc func(ctx context.Context, baseURL string) (traefikapi.Result, error)

// candidate is a machine about to be probed, or the reason it is not.
//
// Identity is the node key, not the hostname: a tailnet routinely carries
// several machines with the same hostname, and everything keyed by machine
// here would collapse them into one.
type candidate struct {
	id         string
	name       string
	address    string
	skipReason string
}

// probeState carries what is known about a machine between cycles: how many
// times in a row it has failed, when it may be probed again, and what to
// report about it while it is being left alone.
type probeState struct {
	failures    int
	nextAttempt time.Time
	status      string
	reason      string
}

// Report is the outcome of one cycle, written where the command line can read
// it. It is the only account of what discovery did: the command formats this
// rather than repeating the discovery itself, so the two cannot disagree.
type Report struct {
	UpdatedAt       time.Time `json:"updatedAt"`
	Enabled         bool      `json:"enabled"`
	RefreshInterval string    `json:"refreshInterval"`
	Source          string    `json:"source"`
	// SourceUpdatedAt is when the status document the cycle used was produced.
	// On a platform where a host writes that document, its age is the answer to
	// a hostname having gone missing, so the command line needs it.
	SourceUpdatedAt time.Time    `json:"sourceUpdatedAt,omitzero"`
	SourceError     string       `json:"sourceError,omitempty"`
	LocalError      string       `json:"localError,omitempty"`
	Peers           []PeerReport `json:"peers"`
	Collisions      []Collision  `json:"collisions,omitempty"`
}

// PeerReport is one machine's contribution, including the machines that
// contributed nothing and why.
type PeerReport struct {
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	// RetryAt is when a machine that failed its probe will be tried again. It
	// is always after the cycle that produced the report, and the gap is the
	// backoff a repeatedly failing machine has earned.
	RetryAt time.Time `json:"retryAt,omitzero"`
	Hosts   []string  `json:"hosts,omitempty"`
	// RejectedRules holds the machine's rules that were refused for not being
	// constrained by a hostname.
	RejectedRules []string `json:"rejectedRules,omitempty"`
}

// Collision is a hostname claimed by more than one machine, naming the machine
// that serves it and the one whose claim was dropped.
type Collision struct {
	Host     string `json:"host"`
	ServedBy string `json:"servedBy"`
	AlsoOn   string `json:"alsoOn"`
}

// discovery holds one cycle's collaborators and the state that outlives a
// cycle: the per-machine backoff and whether the source was already failing.
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

// httpProbe reads a proxy's routing table and reduces it to routable routes.
//
// The machine is asked what it is before it is asked what it serves: port 30000
// identifies a Traefik rather than this proxy, and an unrelated one on the
// tailnet must not have its routes adopted. Asking first also means a machine
// that is not this proxy costs one request rather than two.
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

// runCycle discovers the machines, probes the ones eligible for it, resolves
// who owns each hostname, and rewrites the forwarding configuration.
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

	// A cycle that cannot tell what the local containers serve would forward a
	// hostname this machine answers itself, so it writes nothing and leaves the
	// previous cycle's files in place until the local proxy answers again.
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

		// Reserved before the write, not after. A transient write failure must
		// not have reconciliation remove the last good configuration for a
		// machine whose ownership was verified: withdrawing is for ownership
		// that could not be checked, not for a disk error.
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

// discover returns the machines to consider this cycle and when the document
// they came from was produced.
//
// A source that produces no document leaves nothing to compare a machine's
// owner against, so that cycle contributes nothing rather than probing anyway.
// There is no source, setting or failure path that forwards to a machine
// without the ownership filter having run on that cycle.
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

// reportSourceFailure says a source is failing once, then stops repeating it.
// The service keeps polling either way.
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

// sortCandidates orders machines by name and then by identity, so which of two
// machines claiming a hostname keeps it does not depend on map iteration or on
// who answered first. The identity breaks the tie between two machines with the
// same hostname, which would otherwise compare equal and swap places between
// cycles, churning file and router names for no reason.
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

// recordFailure pushes a failing machine further out on each consecutive
// failure and returns what to report about it.
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

// backoff doubles the refresh interval per consecutive failure, up to a ceiling.
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

// owner is the machine holding a hostname. The name is what a report shows, the
// identity is what decides whether a second claim is the same machine.
type owner struct {
	id   string
	name string
}

// resolveOwnership drops the routes of a hostname that is already served, and
// reports every drop. A hostname served locally wins over every machine; among
// machines, the first in name order wins, so every machine on the tailnet
// reaches the same conclusion.
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

// claimedElsewhere reports the first hostname of a route that another machine
// already serves, and which machine that is. A machine's own second claim of a
// hostname is not a collision: a container mounted under a path of a hostname
// produces one route for the path and one for the host it sits on.
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

// localRoutes is what this machine serves itself: the literal hostnames, and
// the wildcards, compiled so a peer's hostname can be tested against them.
type localRoutes struct {
	literals map[string]struct{}
	patterns []*regexp.Regexp
	// patternText keeps the wildcards as written, so a peer offering the same
	// wildcard collides with the local one rather than being tested against it.
	patternText map[string]struct{}
}

// serves reports whether this machine already answers for a hostname a peer
// claims, and covers the four combinations of literal and wildcard on each side.
func (l *localRoutes) serves(host traefikapi.Host) bool {
	if host.IsRegexp {
		if _, taken := l.patternText[host.Value]; taken {
			return true
		}
		// A peer wildcard that matches something served here would shadow it,
		// and comparing pattern text alone would never notice: a pattern is
		// never equal to a hostname.
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

// compileHostPattern compiles a hostname pattern, refusing an absurd one. Go's
// regexp is RE2, so a pattern cannot backtrack catastrophically, but a peer
// supplies this text and a bound costs nothing.
func compileHostPattern(pattern string) (*regexp.Regexp, error) {
	if len(pattern) > traefikapi.MaxHostPatternLength {
		return nil, fmt.Errorf("host pattern is longer than %d characters", traefikapi.MaxHostPatternLength)
	}
	return regexp.Compile(pattern)
}

// localHosts returns what the local containers serve, read from the local
// proxy's own API. Routes forwarded to a machine are excluded by the same
// filter that keeps a machine from offering what it does not serve itself, so
// the routes written by the previous cycle do not read as locally served.
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

// peerTraefikConfig builds the forwarding configuration for one machine: one
// service, since every route on a machine has the same backend, and the pair of
// routers the proxy emits for a container, one per entrypoint.
func peerTraefikConfig(slug, address string, routes []traefikapi.Route) *config.TraefikConfig {
	traefikConfig := config.NewTraefikConfig()
	serviceName := traefikapi.PeerRouterPrefix + slug

	for i, route := range routes {
		// The rule and the priority are copied rather than rebuilt. A rule
		// carrying a path matcher keeps working across machines without this
		// service knowing about paths, and the priority the peer reports is the
		// ordering the peer itself applies.
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

// peerSlug reduces a machine name to characters safe in a file name and in a
// Traefik router name.
func peerSlug(name string) string {
	slug := strings.Trim(unsafeSlugChars.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if slug == "" {
		return "peer"
	}
	return slug
}

// uniqueSlug keeps two machines that reduce to the same slug in separate files.
// Tailscale does not make hostnames unique, so two machines genuinely called the
// same thing are the ordinary case rather than a corner one, and the second one
// would otherwise overwrite the first's file and lose its routes silently. The
// address disambiguates, being unique on a tailnet by construction.
func uniqueSlug(taken map[string]string, id, name, address string) string {
	candidates := []string{peerSlug(name), peerSlug(name) + "-" + peerSlug(address)}

	// A third machine can normalise onto the disambiguated name too, so the
	// search continues rather than stopping at the address. It is bounded by
	// the number of machines already holding a slug, since each attempt can
	// collide with at most one of them.
	for i := 2; i <= len(taken)+2; i++ {
		candidates = append(candidates, fmt.Sprintf("%s-%s-%d", peerSlug(name), peerSlug(address), i))
	}

	for _, slug := range candidates {
		if holder, used := taken[slug]; !used || holder == id {
			taken[slug] = id
			return slug
		}
	}

	// Unreachable with the bound above, and a machine without a file is better
	// than two machines sharing one.
	return ""
}

func peerConfigFileName(slug string) string {
	return fmt.Sprintf("%s%s.yaml", traefikapi.PeerRouterPrefix, slug)
}

// peerConfigFilePattern matches the files this service owns. It cannot match
// the twelve-character hexadecimal names the per-container layer reconciles, so
// neither service can remove the other's files, and neither touches the
// generated certificate configuration or the built-in middlewares.
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

// writeReport records the outcome of a cycle where the command line reads it.
// It is written outside the directory the proxy watches: the file provider
// parses every file it finds there, and would report this one as broken
// configuration.
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
	return writeFileAtomically(renderedStateFile(d.config.StateFile), []byte(report.Render()), stateFilePermissions)
}

// writeFileAtomically writes through a temporary file and a rename, so neither
// the proxy watching the dynamic directory nor the command line reading the
// state file ever observes a half-written file.
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

// reconcileConfigs removes the files of machines that contributed nothing this
// cycle, so a machine that went offline stops being forwarded to rather than
// leaving a route pointing at an address that no longer answers.
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

// candidateReports describes machines that were not probed this cycle.
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

// peerReports pairs what each machine answered with what it ended up
// contributing, so a machine whose every hostname was taken elsewhere is still
// listed with the hostnames it offered dropped.
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

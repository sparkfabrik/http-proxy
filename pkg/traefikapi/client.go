// Package traefikapi reads a Traefik routing table from the read-only API a
// proxy already publishes, and reduces it to the routes worth forwarding to.
package traefikapi

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

// RoutersPath is the API endpoint listing the HTTP routers of a proxy.
const RoutersPath = "/api/http/routers"

// MiddlewaresPath is the API endpoint listing the HTTP middlewares of a proxy,
// which is where a proxy's declaration of itself is found.
const MiddlewaresPath = "/api/http/middlewares"

// ProxyDeclarationName is the middleware a proxy publishes to say what it is.
// Port 30000 identifies a Traefik, not this proxy.
const ProxyDeclarationName = "spark-http-proxy"

// PeerRouterPrefix names a forwarded router, its generated file and the
// entrypoint's cleanup glob.
const PeerRouterPrefix = "tailscale-peer-"

// ErrUnreachable reports a machine that did not answer at all, as opposed to
// one that answered with something else.
var ErrUnreachable = errors.New("machine did not answer")

// ErrUndeclared reports a machine answering as a Traefik that does not declare
// itself as this proxy. The check fails closed: no declaration, no routes.
var ErrUndeclared = errors.New("machine does not declare itself as spark-http-proxy")

// Router is the subset of a Traefik router the forwarding decision needs.
type Router struct {
	Name        string   `json:"name"`
	Provider    string   `json:"provider"`
	Rule        string   `json:"rule"`
	Priority    int      `json:"priority"`
	EntryPoints []string `json:"entryPoints"`
	Status      string   `json:"status"`
}

// Middleware is the subset of a Traefik middleware the declaration check needs.
type Middleware struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
}

// Host is a hostname a rule claims, flagged when it is a pattern.
type Host struct {
	Value    string
	IsRegexp bool
}

// Route is a router reduced to what a forwarding router copies from it: the
// rule and the ordering, plus the hostnames it claims.
type Route struct {
	Rule     string
	Priority int
	Hosts    []Host
}

// MaxHostPatternLength bounds a peer-supplied pattern before it is compiled.
const MaxHostPatternLength = 512

// Client reads the routing table of one proxy.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// New returns a client reading the API at baseURL with the given HTTP client.
func New(httpClient *http.Client, baseURL string) *Client {
	return &Client{httpClient: httpClient, baseURL: baseURL}
}

// NewWithTimeout returns a client with its own HTTP client.
func NewWithTimeout(baseURL string, timeout time.Duration) *Client {
	return New(&http.Client{Timeout: timeout}, baseURL)
}

// Declares reads the middlewares endpoint and reports whether the machine
// identifies itself as this proxy.
func (c *Client) Declares(ctx context.Context) (bool, error) {
	var middlewares []Middleware
	if err := c.get(ctx, MiddlewaresPath, &middlewares); err != nil {
		return false, err
	}

	for _, middleware := range middlewares {
		name, _, _ := strings.Cut(middleware.Name, "@")
		if name == ProxyDeclarationName && middleware.Provider == "file" && middleware.Status == "enabled" {
			return true, nil
		}
	}
	return false, nil
}

// Routers returns the HTTP routers the proxy reports, sorted by name.
func (c *Client) Routers(ctx context.Context) ([]Router, error) {
	var routers []Router
	if err := c.get(ctx, RoutersPath, &routers); err != nil {
		return nil, err
	}

	slices.SortFunc(routers, func(a, b Router) int { return cmp.Compare(a.Name, b.Name) })
	return routers, nil
}

// get decodes one API endpoint into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("failed to build the %s request: %w", path, err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("traefik api returned status %d for %s", resp.StatusCode, path)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("failed to decode %s: %w", path, err)
	}
	return nil
}

// Result is what a machine's routing table reduces to: the routes worth
// forwarding, and the rules that were refused.
type Result struct {
	Routes []Route
	// Rejected holds rules refused for not being host-constrained.
	Rejected []string
}

// Routes keeps the routers naming a hostname the machine serves itself, and
// refuses any rule a hostname does not bound.
func Routes(routers []Router) Result {
	result := Result{Routes: make([]Route, 0, len(routers))}

	for _, router := range routers {
		if router.Status != "enabled" {
			continue
		}
		if router.Provider == "internal" {
			continue
		}
		if strings.HasPrefix(router.Name, PeerRouterPrefix) {
			continue
		}
		if !slices.Contains(router.EntryPoints, "http") {
			continue
		}
		hosts := ExtractHosts(router.Rule)
		if len(hosts) == 0 {
			continue
		}
		if !hostConstrained(router.Rule) {
			result.Rejected = append(result.Rejected, router.Rule)
			continue
		}
		result.Routes = append(result.Routes, Route{Rule: router.Rule, Priority: router.Priority, Hosts: hosts})
	}

	return result
}

// hostConstrained parses a rule and reports whether a hostname bounds every
// path through it, at any depth.
func hostConstrained(rule string) bool {
	parser := &ruleParser{rule: rule}

	constrained, ok := parser.parseAlternation()
	if !ok {
		return false
	}

	parser.skipSpace()
	if parser.pos != len(parser.rule) {
		return false
	}
	return constrained
}

// ruleParser walks a rule, each production returning whether a hostname bounds
// its subtree.
type ruleParser struct {
	rule string
	pos  int
}

// parseAlternation handles ||, bounded when every branch is.
func (p *ruleParser) parseAlternation() (bool, bool) {
	constrained, ok := p.parseConjunction()
	if !ok {
		return false, false
	}

	for p.acceptOperator("||") {
		branch, ok := p.parseConjunction()
		if !ok {
			return false, false
		}
		constrained = constrained && branch
	}
	return constrained, true
}

// parseConjunction handles &&, bounded when any branch is.
func (p *ruleParser) parseConjunction() (bool, bool) {
	constrained, ok := p.parseMatcher()
	if !ok {
		return false, false
	}

	for p.acceptOperator("&&") {
		branch, ok := p.parseMatcher()
		if !ok {
			return false, false
		}
		constrained = constrained || branch
	}
	return constrained, true
}

// parseMatcher handles a group or a matcher, negation included.
func (p *ruleParser) parseMatcher() (bool, bool) {
	p.skipSpace()

	negated := false
	for p.pos < len(p.rule) && p.rule[p.pos] == '!' {
		negated = !negated
		p.pos++
		p.skipSpace()
	}

	if p.pos < len(p.rule) && p.rule[p.pos] == '(' {
		p.pos++
		constrained, ok := p.parseAlternation()
		if !ok {
			return false, false
		}
		p.skipSpace()
		if p.pos >= len(p.rule) || p.rule[p.pos] != ')' {
			return false, false
		}
		p.pos++
		return constrained && !negated, true
	}

	name := p.readName()
	if name == "" || !p.readArguments() {
		return false, false
	}
	if negated {
		return false, true
	}
	return name == hostMatcher || name == hostMatcher+regexpMatcherSuffix, true
}

func (p *ruleParser) acceptOperator(operator string) bool {
	p.skipSpace()
	if strings.HasPrefix(p.rule[p.pos:], operator) {
		p.pos += len(operator)
		return true
	}
	return false
}

func (p *ruleParser) readName() string {
	start := p.pos
	for p.pos < len(p.rule) && isNameByte(p.rule[p.pos]) {
		p.pos++
	}
	return p.rule[start:p.pos]
}

// readArguments consumes an argument list, parentheses inside backticks included.
func (p *ruleParser) readArguments() bool {
	if p.pos >= len(p.rule) || p.rule[p.pos] != '(' {
		return false
	}

	closing := closingParenthesis(p.rule, p.pos+1)
	if closing < 0 {
		return false
	}
	p.pos = closing + 1
	return true
}

func (p *ruleParser) skipSpace() {
	for p.pos < len(p.rule) && (p.rule[p.pos] == ' ' || p.rule[p.pos] == '\t') {
		p.pos++
	}
}

// backtickedPattern finds the arguments of a matcher. Traefik quotes them with
// backticks, and a single matcher can carry several.
var backtickedPattern = regexp.MustCompile("`([^`]*)`")

// ExtractHosts returns the hostnames a rule claims, in the order they appear,
// a regular expression one as its pattern text.
func ExtractHosts(rule string) []Host {
	var hosts []Host
	for _, matcher := range hostMatchers(rule) {
		for _, argument := range backtickedPattern.FindAllStringSubmatch(matcher.arguments, -1) {
			value := strings.TrimSpace(argument[1])
			if value != "" {
				hosts = append(hosts, Host{Value: value, IsRegexp: matcher.isRegexp})
			}
		}
	}
	return hosts
}

// hostMatcher is one Host or HostRegexp matcher found in a rule.
type hostMatcherRef struct {
	arguments string
	isRegexp  bool
}

// hostMatchers returns every Host and HostRegexp matcher, scanning each one to
// the parenthesis that closes its arguments.
func hostMatchers(rule string) []hostMatcherRef {
	var matchers []hostMatcherRef

	for i := 0; i+len(hostMatcher) <= len(rule); {
		index := strings.Index(rule[i:], hostMatcher)
		if index < 0 {
			break
		}
		index += i
		i = index + len(hostMatcher)

		// A matcher name ending in Host, such as a middleware's, is not one.
		if index > 0 && isNameByte(rule[index-1]) {
			continue
		}

		// A negated matcher claims nothing: it serves everything except that name.
		if isNegated(rule, index) {
			continue
		}

		rest := rule[i:]
		isRegexp := false
		if trimmed, found := strings.CutPrefix(rest, regexpMatcherSuffix); found {
			rest, isRegexp = trimmed, true
		}
		if !strings.HasPrefix(rest, "(") {
			continue
		}

		open := len(rule) - len(rest) + 1
		closing := closingParenthesis(rule, open)
		if closing < 0 {
			break
		}
		matchers = append(matchers, hostMatcherRef{arguments: rule[open:closing], isRegexp: isRegexp})
		i = closing + 1
	}

	return matchers
}

const (
	hostMatcher         = "Host"
	regexpMatcherSuffix = "Regexp"
)

// closingParenthesis returns the index of the parenthesis closing an argument
// list, ignoring the ones inside backticked arguments.
func closingParenthesis(rule string, from int) int {
	quoted := false
	for i := from; i < len(rule); i++ {
		switch rule[i] {
		case '`':
			quoted = !quoted
		case ')':
			if !quoted {
				return i
			}
		}
	}
	return -1
}

// isNegated reports whether the matcher at index is preceded by !.
func isNegated(rule string, index int) bool {
	for i := index - 1; i >= 0; i-- {
		switch rule[i] {
		case ' ', '\t':
			continue
		case '!':
			return true
		default:
			return false
		}
	}
	return false
}

func isNameByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

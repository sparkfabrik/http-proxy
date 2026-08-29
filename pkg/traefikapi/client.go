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

// ProxyDeclarationName is the middleware every Spark HTTP Proxy publishes to
// say what it is. Port 30000 identifies a Traefik, not this proxy, and a tailnet
// may carry an unrelated Traefik with its dashboard exposed, whose routes must
// never be adopted. The declaration is what separates the two.
//
// A middleware is the right shape for it because it is inert: Traefik applies
// one only where a router references it, so a declaration no router references
// changes nothing on the machine that makes it.
const ProxyDeclarationName = "spark-http-proxy"

// PeerRouterPrefix names the routers generated for tailnet peers. A machine
// offers only the routes it serves itself, so routers carrying this prefix are
// skipped when reading a routing table: without it two machines that both
// forward a hostname would forward it to each other indefinitely.
//
// The same prefix names the generated files and the Traefik services, so the
// loop guard, the file ownership and the entrypoint's cleanup all move together.
const PeerRouterPrefix = "tailscale-peer-"

// ErrUnreachable reports a machine that did not answer on the API port at all,
// as opposed to one that answered with something that is not a routing table.
// Most devices on a tailnet run no proxy, so the two outcomes are worth telling
// apart when reporting why a machine contributed nothing.
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

// Route is a router reduced to what a forwarding router copies from it: the
// rule and the ordering, plus the hostnames it claims.
type Route struct {
	Rule     string
	Priority int
	Hosts    []string
}

// Client reads the routing table of one proxy.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// New returns a client reading the API at baseURL with the given HTTP client.
func New(httpClient *http.Client, baseURL string) *Client {
	return &Client{httpClient: httpClient, baseURL: baseURL}
}

// NewWithTimeout returns a client with its own HTTP client. Most devices on a
// tailnet do not run the proxy, so failing fast is the common path.
func NewWithTimeout(baseURL string, timeout time.Duration) *Client {
	return New(&http.Client{Timeout: timeout}, baseURL)
}

// Declares reports whether the machine says it is this proxy.
//
// It is asked before the routing table, so a machine that is not this proxy
// costs one request rather than two.
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

// Routers returns the HTTP routers the proxy reports, sorted by name so a
// caller sees the same order on every poll.
func (c *Client) Routers(ctx context.Context) ([]Router, error) {
	var routers []Router
	if err := c.get(ctx, RoutersPath, &routers); err != nil {
		return nil, err
	}

	slices.SortFunc(routers, func(a, b Router) int { return cmp.Compare(a.Name, b.Name) })
	return routers, nil
}

// get decodes one API endpoint into out. A machine that does not answer at all
// is reported apart from one that answers with something else, because most
// devices on a tailnet run no proxy and that is not a fault.
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
	// Rejected holds rules that named a hostname but were not safely
	// constrained, so a caller can say what it refused rather than dropping it
	// silently. A rule we cannot be confident about is exactly the one not to
	// copy.
	Rejected []string
}

// Routes keeps the routers that name a hostname a machine serves itself.
//
// Routers are dropped when they are disabled, when they belong to the proxy's
// own dashboard and API, when they carry the peer prefix, when they are the
// HTTPS half of a pair the proxy emits once per entrypoint, and when their
// rule matches no hostname at all.
//
// A rule that survives all that is still refused unless every one of its
// top-level alternatives is constrained by a hostname. The rule is copied into
// a local router verbatim, so an unconstrained alternative such as
// Host(`peer.loc`) || PathPrefix(`/`) would match every request this machine
// receives, and local precedence could not save it: the hostname extraction
// sees only `peer.loc`, so the route that shadows every local container would
// never be recognised as a collision.
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

// hostConstrained reports whether every top-level alternative of a rule is
// narrowed by a hostname it is not negating. A rule that cannot be parsed
// confidently, with unbalanced parentheses or backticks, is not constrained.
func hostConstrained(rule string) bool {
	branches, ok := topLevelAlternatives(rule)
	if !ok {
		return false
	}

	for _, branch := range branches {
		if len(ExtractHosts(branch)) == 0 {
			return false
		}
	}
	return len(branches) > 0
}

// topLevelAlternatives splits a rule on the || operators that are not inside
// parentheses or backticks. It reports false when the rule does not parse.
func topLevelAlternatives(rule string) ([]string, bool) {
	var branches []string
	depth, start, quoted := 0, 0, false

	for i := 0; i < len(rule); i++ {
		switch rule[i] {
		case '`':
			quoted = !quoted
		case '(':
			if !quoted {
				depth++
			}
		case ')':
			if !quoted {
				depth--
				if depth < 0 {
					return nil, false
				}
			}
		case '|':
			if quoted || depth != 0 || i+1 >= len(rule) || rule[i+1] != '|' {
				continue
			}
			branches = append(branches, rule[start:i])
			i++
			start = i + 1
		}
	}

	if quoted || depth != 0 {
		return nil, false
	}
	return append(branches, rule[start:]), true
}

// backtickedPattern finds the arguments of a matcher. Traefik quotes them with
// backticks, and a single matcher can carry several.
var backtickedPattern = regexp.MustCompile("`([^`]*)`")

// ExtractHosts returns the hostnames a rule claims, in the order they appear.
// A regular expression hostname is returned as its pattern text: comparing
// patterns catches two machines using the same wildcard, which is the
// collision worth reporting.
func ExtractHosts(rule string) []string {
	var hosts []string
	for _, arguments := range hostMatcherArguments(rule) {
		for _, argument := range backtickedPattern.FindAllStringSubmatch(arguments, -1) {
			host := strings.TrimSpace(argument[1])
			if host != "" {
				hosts = append(hosts, host)
			}
		}
	}
	return hosts
}

// hostMatcherArguments returns the argument text of every Host and HostRegexp
// matcher in a rule.
//
// The arguments are scanned rather than matched with an expression, because a
// regular expression hostname may itself contain a parenthesis: the rule
// HostRegexp(`^(a|b)\.loc$`) is produced by any container whose VIRTUAL_HOST is
// a `~`-prefixed pattern, and stopping at the first `)` would truncate it.
func hostMatcherArguments(rule string) []string {
	var arguments []string

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

		// A negated matcher serves everything except that hostname, so it is
		// not a claim on it. The rule is still copied whole, so routing is
		// unaffected; only ownership would be, by recording a machine as
		// serving the one hostname it does not.
		if isNegated(rule, index) {
			continue
		}

		rest := rule[i:]
		if trimmed, isRegexp := strings.CutPrefix(rest, regexpMatcherSuffix); isRegexp {
			rest = trimmed
		}
		if !strings.HasPrefix(rest, "(") {
			continue
		}

		open := len(rule) - len(rest) + 1
		closing := closingParenthesis(rule, open)
		if closing < 0 {
			break
		}
		arguments = append(arguments, rule[open:closing])
		i = closing + 1
	}

	return arguments
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

// isNegated reports whether the matcher at index is preceded by the negation
// operator, ignoring the whitespace Traefik allows between the two.
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

## 1. Read the declaration

- [x] 1.1 `VIRTUAL_PATH` is read from a container alongside `VIRTUAL_HOST` and `VIRTUAL_PORT`
- [x] 1.2 A container without `VIRTUAL_PATH` produces byte-identical configuration to what it produces today
- [x] 1.3 A trailing separator is normalised away, so `/api` and `/api/` are one declaration
- [x] 1.4 `VIRTUAL_PATH=/` is treated as though the variable were absent
- [x] 1.5 A value that is not a single plain path beginning with a separator is rejected, reported, and leaves the container's hostname routes intact
- [x] 1.6 A value containing a separator between several paths is rejected rather than accepted as one unmatchable path
- [x] 1.7 A value containing characters that would be read as routing syntax is rejected, covered by a test that fails if the check is removed

## 2. Build the route

- [x] 2.1 A declared path produces a route matching that path and everything beneath it, on every hostname the container declares
- [x] 2.2 The route does not match a sibling path that merely begins with the same characters
- [x] 2.3 The backend receives the request path unchanged
- [x] 2.4 The path route and its secure twin are both produced, as the hostname routes already are
- [x] 2.5 The container's own port is used, whatever port another container on the hostname declares
- [x] 2.6 A wildcard or `~`-pattern hostname keeps its meaning when a path is declared with it

## 3. Make precedence deterministic without disturbing what exists

- [x] 3.1 The router type carries a precedence field that serialises, with a non-zero floor, so it cannot be dropped and silently restore inherited ordering
- [x] 3.2 Only the routes a declared path produces carry an explicit precedence; hostname routes are emitted exactly as before
- [x] 3.3 A path route outranks the route for the hostname it sits on
- [x] 3.4 A path route outranks a wildcard route that also matches the hostname
- [x] 3.5 A longer path outranks a shorter one on the same hostname
- [x] 3.6 A path's HTTP and HTTPS routes carry the same precedence
- [x] 3.7 A test asserts the emitted configuration, not the struct, so the field cannot silently stop serialising
- [x] 3.8 A test asserts that a container without a path emits no precedence at all

## 4. Report what cannot work

- [x] 4.1 Every report this change introduces is emitted at a level visible at the default setting
- [x] 4.2 `VIRTUAL_PATH` with no `VIRTUAL_HOST` is reported, naming the container
- [x] 4.3 `VIRTUAL_PATH` on a container carrying any `traefik.` label is reported, since the container is skipped whole
- [x] 4.4 A rejected path says what was wrong with it
- [x] 4.5 Two containers claiming the same path on one hostname are reported, naming both
- [x] 4.6 The collision is detected whether both containers were present at startup or the second arrived later, which needs state carried across events rather than a single scan

## 5. Keep certificates about hostnames

- [x] 5.1 A path is served over HTTPS by the certificate already covering its hostname, with nothing further created
- [x] 5.2 Generating a certificate for an argument containing a path is refused, with a message saying a certificate covers a hostname
- [x] 5.3 Removing one is refused the same way
- [x] 5.4 The refusal is checked, since the filename helper currently maps a separator to an underscore and lets such a request half-succeed

## 6. Documentation a reader will actually find

- [x] 6.1 `VIRTUAL_PATH` is listed wherever the existing container variables are described, with what it does
- [x] 6.2 The supported-patterns section shows it, marked as a separate variable rather than another hostname form
- [x] 6.3 A runnable example shows two containers sharing one hostname, one at the root and one under a path
- [x] 6.4 The compatibility notes record it as borrowed from `nginx-proxy` rather than from the dinghy proxy the table describes, and record `VIRTUAL_DEST` as unsupported
- [x] 6.5 The documentation states that nothing is stripped, that a certificate covers a hostname, that the port belongs to the container, that a `traefik.` label disables both variables, and what the paths return once the mounted container stops
- [x] 6.6 A line says that changing the variable needs the container recreated, since environment variables cannot change in place
- [x] 6.7 The contributor guide gains the variable, and two of its claims are corrected: that the repository has no unit tests, and that the make target runs them
- [x] 6.8 The shipped example that is already dead, because a middleware label suppresses its `VIRTUAL_HOST`, is fixed rather than left teaching the trap
- [x] 6.9 Every example added or touched is checked by running it
- [x] 6.10 A changelog entry, as the contributor guide requires

## 7. The agent skill for this proxy

Lands in the companion skills repository the contributor guide points at, and
therefore as its own pull request. Left open here deliberately: the archive
records the proxy change, and this group is tracked to completion in that
repository once the published image carries the feature.

- [ ] 7.1 The skill's container-exposure reference covers the variable, with a worked compose example of two containers on one hostname
- [ ] 7.2 It appears in the skill's summary of accepted forms, marked as a separate variable
- [ ] 7.3 The skill's description names the signals that should select it: the variable, one origin, a path that is not routing locally
- [ ] 7.4 The skill's troubleshooting covers a path reaching the wrong container, the stopped-container fall-through, and the `traefik.` label case
- [ ] 7.5 The skill states what the proxy does not do: nothing is stripped, a certificate covers a hostname, the port belongs to the container
- [ ] 7.6 Raised only once the proxy change is released, so the skill never describes a version nobody can run

## 8. Cover the parsing and rule building directly

- [x] 8.1 Every accepted and rejected form has a case: absent, root-only, trailing separator, no leading separator, a list, and routing syntax
- [x] 8.2 The generated rule is asserted whole, not by substring, so a change to the matcher cannot pass unnoticed
- [x] 8.3 The emitted router is asserted whole, so precedence and its absence are both pinned
- [x] 8.4 The precedence ordering is asserted against a hostname rule and against a wildcard rule
- [x] 8.5 A container with several hostnames and one path produces a mounted route on each

## 9. Prove it end to end

- [x] 9.1 Two containers on one hostname, one at the root and one under a path: each request reaches the right one, proved by distinct response content rather than status codes
- [x] 9.2 The same over HTTPS, with the same result
- [x] 9.3 A request for a sibling path sharing the prefix reaches the root container
- [x] 9.4 A wildcard container covering the same domain does not take the mounted path
- [x] 9.5 The mounted container stops: its paths fall through to the root container, which is the documented behaviour
- [x] 9.6 The root container stops: the mounted path still answers and the root does not
- [x] 9.7 A path on a hostname nothing else serves answers under the path and nowhere else
- [x] 9.8 A stack declaring no paths behaves exactly as before
- [x] 9.9 Test containers use an image the suite's readiness probe can actually reach, since it executes a command inside the container and the obvious image for distinguishing backends has no shell
- [x] 9.10 The suite's bookkeeping is updated in every place it is kept by hand: the container names, their creation and waits, the per-suite counters, the printed results block, and cleanup

## 10. Ship

- [x] 10.1 Fresh-eye read of the whole diff, and a pass removing anything it does not need
- [x] 10.2 Pull request opened, with the automated checks green before it is merged
- [x] 10.3 Archive this change

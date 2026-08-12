## Purpose

Let a container be routed on a path of a hostname another container already serves, through `VIRTUAL_PATH`, so applications that must share one origin can do so locally with the same environment-variable interface used to expose a whole hostname.

## ADDED Requirements

### Requirement: VIRTUAL_PATH mounts a container under a path of its hostname

A container setting `VIRTUAL_PATH` alongside `VIRTUAL_HOST` SHALL receive requests for that path on that hostname. Requests outside the path SHALL continue to reach whichever container serves the hostname itself.

#### Scenario: Two containers share one hostname

- **WHEN** one container sets `VIRTUAL_HOST=app.loc` and another sets `VIRTUAL_HOST=app.loc` with `VIRTUAL_PATH=/api`
- **THEN** a request for `/api` reaches the second container
- **AND** a request for `/` reaches the first

#### Scenario: The backend sees the path unchanged

- **WHEN** a request for `/api/users` reaches a container mounted at `/api`
- **THEN** the container receives `/api/users`, with nothing removed

#### Scenario: Both schemes reach the same container

- **WHEN** a request for a mounted path arrives over HTTP, and the same request arrives over HTTPS
- **THEN** both reach the mounted container

#### Scenario: The container keeps its own port

- **WHEN** a container mounted under a path sets `VIRTUAL_PORT`
- **THEN** that port is used for it, whatever port any other container on the hostname sets

#### Scenario: VIRTUAL_PATH applies to every hostname the container declares

- **WHEN** a container sets `VIRTUAL_HOST=a.loc,b.loc` with `VIRTUAL_PATH=/api`
- **THEN** it is mounted at `/api` on both hostnames

### Requirement: A path matches on whole segments

`VIRTUAL_PATH` SHALL match its own path and anything beneath it, and SHALL NOT match a sibling path that merely begins with the same characters.

#### Scenario: The path itself and its children

- **WHEN** a container is mounted at `/api`
- **THEN** a request for `/api` reaches it
- **AND** so does a request for `/api/users`

#### Scenario: A sibling sharing the prefix

- **WHEN** a container is mounted at `/api` and a request names `/api-docs`
- **THEN** it does not reach the mounted container

#### Scenario: A trailing separator makes no difference

- **WHEN** a container sets `VIRTUAL_PATH=/api` and another sets `VIRTUAL_PATH=/api/`
- **THEN** the two declarations route identically

### Requirement: A mounted path wins against the routes this proxy generates

A mounted path SHALL take precedence over every other route this proxy generates that would otherwise match the same request. Precedence SHALL be stated on the mounted path's routes rather than left to be inferred.

Routes a user writes directly as Traefik labels are outside this requirement, because a user can set any precedence there deliberately.

#### Scenario: Against the hostname's own route

- **WHEN** `app.loc` is served by one container and `/api` on it by another
- **THEN** requests under `/api` reach the mounted container

#### Scenario: Against a wildcard covering the same hostname

- **WHEN** another container declares a wildcard hostname that also matches `app.loc`
- **THEN** requests under `/api` still reach the mounted container

#### Scenario: A longer path against a shorter one

- **WHEN** one container is mounted at `/api` and another at `/api/internal` on the same hostname
- **THEN** a request for `/api/internal/x` reaches the second

#### Scenario: Both of a mounted path's routes rank together

- **WHEN** a container is mounted under a path
- **THEN** its HTTP and HTTPS routes carry the same precedence, so the two schemes cannot order differently

### Requirement: Existing declarations are untouched

Adding `VIRTUAL_PATH` SHALL NOT change the routing of any container that does not set it.

#### Scenario: A container declaring only a hostname

- **WHEN** a container sets `VIRTUAL_HOST` and no `VIRTUAL_PATH`
- **THEN** its generated routes are unchanged, including how they rank against every other route on the machine

#### Scenario: A hostname that is a wildcard or a pattern

- **WHEN** a container declares a wildcard or a `~`-prefixed pattern hostname
- **THEN** it keeps being interpreted as such, whether or not `VIRTUAL_PATH` is set

#### Scenario: A container using a path is still managed

- **WHEN** a container sets `VIRTUAL_PATH`
- **THEN** it is managed, and its network is joined, on the same terms as any other exposed container

### Requirement: A declaration that cannot work is reported

A declaration that cannot produce a working route SHALL be reported where a user running the proxy normally will see it, and SHALL NOT silently produce a route that never matches or a rule the declaration did not intend.

#### Scenario: VIRTUAL_PATH without VIRTUAL_HOST

- **WHEN** a container sets `VIRTUAL_PATH` and no `VIRTUAL_HOST`
- **THEN** it is reported, naming the container, because the path has no hostname to sit on and the container is not exposed at all

#### Scenario: VIRTUAL_PATH alongside a Traefik label

- **WHEN** a container sets `VIRTUAL_PATH` and also carries any `traefik.` label
- **THEN** it is reported, because such a container is skipped entirely and neither its hostname nor its path is routed

#### Scenario: A malformed path

- **WHEN** `VIRTUAL_PATH` is not a single plain path beginning with a separator
- **THEN** it is rejected and reported, naming what was wrong

#### Scenario: A path carrying routing syntax

- **WHEN** `VIRTUAL_PATH` contains characters that would otherwise be read as routing syntax
- **THEN** it is rejected, and no route is produced from it

#### Scenario: A list where one path is expected

- **WHEN** `VIRTUAL_PATH` contains a separator between several paths, as `VIRTUAL_HOST` accepts for hostnames
- **THEN** it is rejected and reported, rather than accepted as one path that can never match

#### Scenario: Two containers claiming one path

- **WHEN** two containers claim the same path on the same hostname, whether they were both present at startup or the second arrived later
- **THEN** it is reported, naming both, because which one answers would otherwise be arbitrary

### Requirement: A path addressing the whole hostname is a hostname declaration

`VIRTUAL_PATH` naming the root SHALL be treated as though it were absent, rather than producing a second route for the whole hostname.

#### Scenario: The root path

- **WHEN** a container sets `VIRTUAL_PATH=/`
- **THEN** it is routed as though only `VIRTUAL_HOST` were set

### Requirement: A path needs no certificate of its own

Certificate handling SHALL remain per hostname. A request to create or remove a certificate for a path SHALL be refused with an explanation.

#### Scenario: A certificate covers the paths on its hostname

- **WHEN** a hostname has a certificate and a container is mounted under a path of it
- **THEN** the mounted path is served over HTTPS, and no further certificate is created

#### Scenario: Asking for a certificate for a path

- **WHEN** someone asks to generate or remove a certificate for an argument containing a path
- **THEN** the request is refused, and the message says a certificate covers a hostname

### Requirement: The hostname's own container is not required

A path SHALL be routable whether or not another container currently serves the hostname it sits on.

#### Scenario: No container serves the hostname

- **WHEN** a container is mounted at `/api` on a hostname nothing else serves
- **THEN** requests under `/api` reach it, and requests for `/` are not found

#### Scenario: The mounted container stops

- **WHEN** the container serving `/api` stops while the hostname's own container keeps running
- **THEN** requests under `/api` reach the hostname's container instead, as they would if the path had never been declared

### Requirement: The shipped diagnostic exercises a path

The self-diagnosis command SHALL verify that a mounted path is answered by the container mounted there, distinguishing it from the container serving the hostname.

#### Scenario: The diagnostic checks a path

- **WHEN** a user runs the self-diagnosis command
- **THEN** it starts a container on a hostname and another mounted under a path of it
- **AND** it asserts that a request under the path is answered by the mounted container, identified by its response content rather than only its status

#### Scenario: A status code alone is not enough

- **WHEN** the mounted container's route is missing and the request falls through to the hostname's container
- **THEN** the diagnosis fails, because the response did not come from the container it asked for

#### Scenario: The diagnostic cleans up after itself

- **WHEN** the diagnosis finishes, whether it passed or failed
- **THEN** every container it started is removed

### Requirement: The capability is documented where the existing variables are

`VIRTUAL_PATH` SHALL appear wherever `VIRTUAL_HOST` and `VIRTUAL_PORT` are already described, with a worked example, and what is deliberately unsupported SHALL be stated.

#### Scenario: A reader looks up how to expose a container

- **WHEN** someone reads the documentation of the environment variables a container can set
- **THEN** `VIRTUAL_PATH` is listed alongside them, with what it does and how it combines with `VIRTUAL_HOST`

#### Scenario: A reader wants a working example

- **WHEN** someone runs the shipped examples
- **THEN** one shows two containers sharing a hostname, one at the root and one under a path, and it works as written

#### Scenario: A reader comes from the projects this layer is compatible with

- **WHEN** someone reads the compatibility notes
- **THEN** `VIRTUAL_PATH` is recorded as borrowed from `nginx-proxy` rather than from `codekitchen/dinghy-http-proxy`, which never had it
- **AND** `VIRTUAL_DEST`, the `nginx-proxy` companion that rewrites the path, is recorded as unsupported

#### Scenario: A reader needs the edge cases

- **WHEN** someone reads about `VIRTUAL_PATH`
- **THEN** it states that nothing is stripped, that a certificate covers a hostname, that `VIRTUAL_PORT` belongs to the container, that a `traefik.` label on the same container disables both variables, and what the paths return once the mounted container stops

### Requirement: The agent skill for this proxy teaches the new variable

The companion skill agents load when asked to expose a container SHALL cover `VIRTUAL_PATH`, so an agent asked to put two applications on one hostname reaches for it rather than inventing routing labels.

#### Scenario: An agent is asked to expose a container under a path

- **WHEN** an agent working from the skill is asked to serve an application under a path of a hostname another container already uses
- **THEN** the skill gives it `VIRTUAL_PATH`, with a worked compose example

#### Scenario: The skill's summary of accepted forms

- **WHEN** an agent reads the skill's table of what `VIRTUAL_HOST` accepts
- **THEN** `VIRTUAL_PATH` appears alongside it, marked as a separate variable rather than another hostname form

#### Scenario: The skill's triggers

- **WHEN** a user mentions `VIRTUAL_PATH`, or asks for one origin, or asks why a path is not routing locally
- **THEN** the skill is selected, because its description names those signals

#### Scenario: The skill's troubleshooting

- **WHEN** an agent is asked why a path reaches the wrong container
- **THEN** the skill covers it, including the stopped-container fall-through and the `traefik.` label case

# tailscale-peer-routing Specification

## Purpose

Let a hostname served by a container on one machine be reached, under the same name, from every other machine belonging to the same user on the same Tailscale tailnet, so a developer with several machines keeps one hostname per project instead of one per machine.

## Requirements

### Requirement: A hostname served by another machine is reachable locally

When a hostname is not served by any local container and a peer machine serves it, a request for that hostname SHALL be answered by the container on that peer machine. The response SHALL be the one the peer's container produces, unchanged.

#### Scenario: A remote-only hostname is answered

- **WHEN** one machine runs a container for `app.loc` and another machine runs no container for it
- **THEN** a request for `app.loc` on the second machine is answered by the container on the first

#### Scenario: The hostname is the same on both machines

- **WHEN** a container is exposed under a hostname on the machine that runs it
- **THEN** that same hostname reaches it from every other machine, with no machine name added to it and no per-machine configuration in the project

#### Scenario: A path-mounted container is reached through its path

- **WHEN** a container on a peer machine is mounted under a path of a hostname
- **THEN** a request for that path reaches it, and a request for the hostname's root reaches whichever container serves the root on that peer

#### Scenario: Encryption terminates on the machine being asked

- **WHEN** a request for a remotely served hostname arrives over HTTPS
- **THEN** it is answered using the certificates installed on the machine receiving the request
- **AND** no certificate authority has to be shared between the machines

### Requirement: A locally served hostname never leaves the machine

A hostname served by a local container SHALL be answered locally, whether or not a peer machine also serves it. A collision between a local hostname and a remote one SHALL be reported.

#### Scenario: The local container wins

- **WHEN** both the local machine and a peer machine serve `app.loc`
- **THEN** the request is answered by the local container
- **AND** the collision is reported

#### Scenario: Local routing is unchanged

- **WHEN** every hostname a machine is asked for is served by one of its own containers
- **THEN** the machine routes exactly as it does with the behaviour disabled, and no request is sent to another machine

### Requirement: Peer machines are discovered without configuration

The set of peer machines SHALL be derived at runtime from the tailnet the machine is already on. It SHALL include only machines belonging to the same user, and only machines currently online. No list of peers is written down or maintained by hand.

The same-user restriction is a fixed property of discovery. It SHALL be applied on every cycle, with no setting that widens, weakens or bypasses it, and no mode of operation in which it does not run.

#### Scenario: A new machine needs no configuration

- **WHEN** a machine joins the tailnet and runs the proxy
- **THEN** the hostnames it serves become reachable from the other machines without any of them being reconfigured

#### Scenario: A machine that is not a proxy is ignored

- **WHEN** the tailnet contains devices that do not run the proxy, such as a phone, a router or a television
- **THEN** they contribute no routes and do not prevent other machines from being discovered

#### Scenario: Another user's machine is excluded

- **WHEN** the tailnet contains a machine belonging to a different user
- **THEN** its routes are not used

#### Scenario: The restriction cannot be widened

- **WHEN** any available setting is changed, in any combination
- **THEN** a machine belonging to a different user is still excluded

#### Scenario: A missing daemon does not become permission

- **WHEN** the machine cannot obtain the tailnet's account of itself
- **THEN** no peer is probed and no hostname is forwarded
- **AND** the failure is reported, rather than being treated as an empty tailnet

#### Scenario: A machine going offline withdraws its hostnames

- **WHEN** a peer machine goes offline, or stops serving a hostname
- **THEN** that hostname stops being forwarded to it
- **AND** it is answered locally again if a local container serves it

### Requirement: There is no unverified mode

Every machine the proxy forwards to SHALL have been vouched for, on the cycle it is used, by the tailnet's own account of who owns it. There SHALL be no setting, mode or failure path by which an address is forwarded to without that check having run.

How that account is obtained MAY differ between platforms. What it is checked for SHALL NOT.

#### Scenario: An address alone is never enough

- **WHEN** an address is known to the machine by any means
- **THEN** it is not forwarded to unless the tailnet vouched for the machine at that address on the current cycle

#### Scenario: No platform is exempt

- **WHEN** a platform cannot obtain the account the way another platform does
- **THEN** it obtains it another way and applies the same check
- **AND** it does not forward with the check skipped

### Requirement: Only a machine running this proxy is used

This proxy SHALL declare its own identity in a way a peer can read. A machine SHALL be adopted as a source of routes only when that declaration is present on the cycle its routes are used.

Answering on the expected port SHALL NOT be sufficient. A machine that answers with a routing table but does not declare this proxy SHALL contribute nothing, and SHALL be reported as answering without being this proxy, distinctly from one that did not answer at all.

The declaration SHALL be made whether or not the declaring machine forwards to peers itself, since a machine can be a source of routes for others without using any.

#### Scenario: An unrelated proxy is not adopted

- **WHEN** a machine on the tailnet answers a routing table on the expected port but is not running this proxy
- **THEN** none of its routes are used
- **AND** it is reported as answering without being this proxy, not as unreachable

#### Scenario: A machine that serves no peers still declares itself

- **WHEN** a machine runs this proxy with peer routing disabled
- **THEN** other machines can still read its routes and forward to it

#### Scenario: An older version is not adopted

- **WHEN** a machine runs a version of this proxy that predates the declaration
- **THEN** it contributes nothing rather than being adopted on the strength of the port alone

### Requirement: A forwarded hostname is never forwarded onward

A machine SHALL offer to its peers only the hostnames served by its own containers. A hostname a machine reaches by forwarding SHALL NOT be offered to another machine.

#### Scenario: Two machines cannot bounce a request

- **WHEN** two machines both have the behaviour enabled and neither serves a given hostname
- **THEN** the request is not passed back and forth between them

### Requirement: One hostname has one owner

When two peer machines serve the same hostname, the same one SHALL be chosen on every poll and by every machine, and the choice SHALL NOT depend on which peer answered first. The collision SHALL be reported.

#### Scenario: Two peers claim one hostname

- **WHEN** two peer machines both serve `app.loc` and the local machine serves neither
- **THEN** requests are consistently answered by the same one of them
- **AND** the collision is reported

### Requirement: The behaviour is opt-in and turned on the way monitoring is

Peer routing SHALL be disabled unless explicitly enabled. With it disabled, a machine SHALL behave exactly as it does without this capability, and SHALL send no request to another machine.

It SHALL be started and stopped through the command line in the same shape as the optional monitoring stack: a lifecycle command that starts the proxy with peer routing on, and one that stops peer routing alone without disturbing the rest of the proxy. Both SHALL appear in the usage text and in shell completion alongside their monitoring counterparts.

Stopping peer routing SHALL leave no forwarded hostname reachable, whether or not the proxy itself is restarted afterwards.

#### Scenario: An upgrade changes nothing

- **WHEN** a machine is upgraded to a version containing this capability and nothing is enabled
- **THEN** its routing is unchanged and it makes no attempt to discover peers

#### Scenario: Disabling restores previous behaviour

- **WHEN** the behaviour is disabled on a machine that had it enabled, and the proxy is restarted
- **THEN** no forwarded hostnames remain and the machine routes only its own containers

#### Scenario: Stopping peer routing alone

- **WHEN** peer routing is stopped without stopping the proxy
- **THEN** no forwarded hostname is reachable any more
- **AND** the machine keeps serving its own containers throughout

#### Scenario: It is discoverable from the command line

- **WHEN** a user reads the usage text
- **THEN** starting the proxy with peer routing, and stopping peer routing, are listed as commands, as the monitoring stack's are

### Requirement: What was discovered can be inspected from the command line

A command SHALL report the peer machines the proxy considered on its most recent cycle, the hostnames each one contributes, and the reason any considered machine contributed nothing. It SHALL report the same information in a machine-readable form on request. It SHALL report what the proxy acted on, rather than performing its own discovery.

#### Scenario: Peers and their hostnames are listed

- **WHEN** peer routing is enabled and at least one peer is contributing routes
- **THEN** the command names each peer, its address, and the hostnames being forwarded to it

#### Scenario: A peer that gave nothing is still explained

- **WHEN** a machine was considered but contributed no hostnames
- **THEN** it is listed with the reason, distinguishing one that could not be reached from one that answered but runs no proxy, and from one that was excluded

#### Scenario: Collisions are visible without reading logs

- **WHEN** a hostname is served both locally and by a peer, or by two peers
- **THEN** the command names the hostname and which machine serves it

#### Scenario: The report matches what the proxy is doing

- **WHEN** the command is run
- **THEN** it reflects the proxy's most recent cycle, and it does not query the tailnet itself

#### Scenario: Disabled is reported as disabled

- **WHEN** peer routing is not enabled
- **THEN** the command says so and says how to enable it, rather than reporting an empty result

#### Scenario: A stopped proxy still reports its last state

- **WHEN** the proxy is not running
- **THEN** the command reports the last known state and makes clear that it is not current

### Requirement: Peer routes do not disturb local ones

The configuration generated for peers SHALL be owned separately from the configuration generated for local containers. Neither SHALL remove or alter the other's, and the certificates and built-in configuration the proxy already carries SHALL be left untouched.

#### Scenario: Local container configuration survives

- **WHEN** peer routes are written, refreshed and removed over the lifetime of the proxy
- **THEN** the configuration for local containers, the generated certificate configuration, and the built-in middlewares are unaffected

#### Scenario: Stale peer configuration is removed

- **WHEN** a peer that was contributing routes is no longer reachable
- **THEN** the configuration written for it is removed, rather than being left behind pointing at an address that no longer answers

## Purpose

Covers how a certificate generated or removed through the CLI reaches Traefik,
how quickly the change takes effect, and what happens when the proxy is not
running. It exists because applying a certificate previously required restarting
the proxy, which drops every connection on the machine.

## ADDED Requirements

### Requirement: Generating a certificate does not interrupt traffic

Generating a certificate SHALL NOT restart the proxy and SHALL NOT drop
connections to any hostname the proxy already serves.

#### Scenario: A request in flight survives certificate generation

- **WHEN** a certificate is generated while another hostname is being served
- **THEN** the request in flight completes normally
- **AND** the proxy is not restarted

#### Scenario: A newly generated certificate is served

- **WHEN** a certificate is generated for a hostname the proxy serves
- **AND** the proxy is running
- **THEN** that hostname is served with the new certificate within 10 seconds

#### Scenario: Regenerating an existing certificate replaces the one served

- **WHEN** a certificate that the proxy already serves is generated again
- **THEN** the hostname is served with the new certificate within 10 seconds
- **AND** the certificate previously served is no longer presented

### Requirement: Removing a certificate does not interrupt traffic

Removing a certificate SHALL NOT restart the proxy, and the proxy SHALL stop
referring to the removed certificate rather than pointing at a file that no
longer exists.

#### Scenario: A removed certificate stops being served

- **WHEN** a certificate is removed while the proxy is running
- **THEN** its hostname is no longer served with that certificate within 10 seconds
- **AND** the proxy is not restarted

#### Scenario: Removing the last certificate leaves nothing dangling

- **WHEN** the only certificate installed is removed
- **THEN** the proxy refers to no certificate at all
- **AND** hostnames still served fall back to the proxy's default certificate

### Requirement: Certificate commands succeed when the proxy is not running

Generating or removing a certificate SHALL succeed when the proxy is not
running. The command SHALL NOT fail, SHALL NOT report an error, and SHALL NOT
start the proxy, because the proxy applies the certificate when it next starts.

#### Scenario: Generating with the proxy stopped

- **WHEN** a certificate is generated and the proxy is not running
- **THEN** the certificate files are written
- **AND** the command exits successfully with no error reported
- **AND** the proxy is not started

#### Scenario: The certificate applies at the next start

- **WHEN** a certificate was generated while the proxy was stopped
- **AND** the proxy is then started
- **THEN** its hostname is served with that certificate

### Requirement: Certificate commands describe what they did

Certificate commands SHALL NOT announce an action they do not perform. In
particular they SHALL NOT report restarting the proxy.

#### Scenario: Generation reports applying, not restarting

- **WHEN** a certificate is generated while the proxy is running
- **THEN** the output says the certificate is being applied
- **AND** the output does not say the proxy is being restarted

#### Scenario: Generation with the proxy stopped says when it will apply

- **WHEN** a certificate is generated and the proxy is not running
- **THEN** the output says the certificate applies when the proxy starts
- **AND** the output does not read as a failure

### Requirement: An older proxy image degrades quietly

A CLI carrying this behaviour SHALL work against a proxy image that predates it.
It SHALL detect that the running proxy cannot apply a certificate on request,
and SHALL NOT issue a command that the older proxy would misinterpret.

#### Scenario: The running proxy cannot apply a certificate on request

- **WHEN** a certificate is generated against a proxy image that predates this change
- **THEN** the command exits successfully
- **AND** no unrecognised command is sent to the proxy
- **AND** the output does not claim the certificate is already live

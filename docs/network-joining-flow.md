# Network Joining Flow Documentation

This document explains how the `join-networks` service automatically manages Docker network connections for the HTTP proxy container.

## Overview

The `join-networks` service ensures that the HTTP proxy container is automatically connected to all Docker networks that contain manageable containers (containers with `VIRTUAL_HOST` environment variables). This enables automatic routing without manual network configuration.

### Key Concepts

- **HTTP Proxy Container**: The main Traefik container that routes HTTP traffic
- **Manageable Containers**: Any container with `VIRTUAL_HOST` environment variable or Traefik labels
- **Join-Networks Service**: A background service that monitors Docker events and manages network connections
- **Automatic Network Discovery**: The process of scanning Docker networks to find containers that need routing

### How It Works

1. **At Startup**: The service scans all Docker bridge networks and connects the HTTP proxy to networks containing manageable containers
2. **During Runtime**: When containers start/stop, the service automatically joins new networks or leaves empty ones
3. **Security**: Only explicitly configured containers (with `VIRTUAL_HOST` or Traefik labels) are considered for routing
4. **Fail-Fast**: If any network operation fails, the service exits and relies on container restart for recovery

## Architecture Flow

```mermaid
graph TD
    A[Service Startup] --> B[Initial Network Scan]
    B --> C[Get Container Info]
    C --> D[Find Bridge Networks]
    D --> E[Check Networks for Manageable Containers]
    E --> F{Has Manageable Containers?}
    F -->|Yes| G[Add to Join List]
    F -->|No| H[Skip Network]
    G --> I[Execute Network Operations]
    H --> I
    I --> J[Service Running - Listen for Events]

    J --> K{Docker Event}
    K -->|Container Start| L[Handle Container Start]
    K -->|Container Stop| M[Handle Container Stop]
    K -->|Other| N[Ignore Event]

    L --> O[Re-scan Networks]
    O --> P[Join New Networks]
    P --> J

    M --> Q[Check Current Networks]
    Q --> R{Network Empty?}
    R -->|Yes| S[Leave Empty Network]
    R -->|No| T[Keep Connection]
    S --> J
    T --> J
    N --> J

    style A fill:#e1f5fe
    style J fill:#f3e5f5
    style I fill:#e8f5e8
    style S fill:#fff3e0
```

## HTTP Proxy and Container Interactions

This sequence diagram shows how the HTTP proxy container interacts with other containers and networks:

```mermaid
sequenceDiagram
    participant HP as HTTP Proxy Container
    participant JS as Join-Networks Service
    participant DA as Docker API
    participant N1 as Network: app-network
    participant N2 as Network: api-network
    participant C1 as App Container<br/>(VIRTUAL_HOST=app.local)
    participant C2 as API Container<br/>(VIRTUAL_HOST=api.local)
    participant C3 as Database Container<br/>(no VIRTUAL_HOST)

    Note over HP,C3: Initial State: Containers and Networks Exist

    HP->>+JS: Service Startup
    JS->>+DA: Get HTTP Proxy Container Info
    DA-->>-JS: Current Networks: [bridge]

    JS->>+DA: List All Bridge Networks
    DA-->>-JS: [bridge, app-network, api-network]

    Note over JS,C3: Scan each network for manageable containers

    JS->>+DA: Get Containers in app-network
    DA-->>-JS: [C1 - has VIRTUAL_HOST]

    JS->>+DA: Get Containers in api-network
    DA-->>-JS: [C2 - has VIRTUAL_HOST, C3 - no VIRTUAL_HOST]

    Note over JS: Decision: Join app-network & api-network<br/>Skip bridge (default), Ignore C3 (not manageable)

    JS->>+DA: Connect HTTP Proxy to app-network
    DA-->>-JS: ✅ Connected

    JS->>+DA: Connect HTTP Proxy to api-network
    DA-->>-JS: ✅ Connected

    Note over HP,C3: HTTP Proxy now routes traffic to C1 and C2

    rect rgb(230, 245, 255)
        Note over HP,C3: New Container Scenario
        C1->>+DA: Container Starts (new-app with VIRTUAL_HOST=new.local)
        DA->>JS: Event: Container Start
        JS->>+DA: Re-scan Networks
        DA-->>-JS: Found new-app-network with manageable container
        JS->>+DA: Connect HTTP Proxy to new-app-network
        DA-->>-JS: ✅ Connected
        Note over HP: HTTP Proxy now routes to new.local
    end

    rect rgb(255, 245, 230)
        Note over HP,C3: Container Stop Scenario
        C2->>+DA: Container Stops
        DA->>JS: Event: Container Stop
        JS->>+DA: Check api-network for manageable containers
        DA-->>-JS: Only C3 remains (not manageable)
        JS->>+DA: Disconnect HTTP Proxy from api-network
        DA-->>-JS: ✅ Disconnected
        Note over HP: HTTP Proxy no longer routes to api-network
    end

    Note over HP,C3: Result: HTTP Proxy automatically manages<br/>network connections based on manageable containers
```

## Detailed Process Flow

### 1. Initial Network Scan

```mermaid
sequenceDiagram
    participant S as Service
    participant D as Docker API
    participant N as Networks
    participant C as Containers

    S->>D: Get HTTP Proxy Container Info
    D-->>S: Container Details & Current Networks

    S->>D: List All Bridge Networks
    D-->>N: Network List

    loop For Each Network
        S->>D: List Containers in Network
        D-->>C: Container List
        S->>S: Check for Manageable Containers
        alt Has Manageable Containers
            S->>S: Add to Join List
        else No Manageable Containers
            S->>S: Skip Network
        end
    end

    S->>S: Calculate Network Operations
    S->>D: Execute Network Operations
    D-->>S: Success or Process Exit on Failure
```

### 2. Event-Driven Network Management

```mermaid
stateDiagram-v2
    [*] --> Listening: Service Started

    Listening --> ContainerStart: Docker Event: Container Start
    Listening --> ContainerStop: Docker Event: Container Stop

    ContainerStart --> ScanNetworks: Re-scan for new networks
    ScanNetworks --> JoinNetworks: Found new networks with containers
    JoinNetworks --> Listening: Networks joined successfully

    ContainerStop --> CheckNetworks: Check current network connections
    CheckNetworks --> LeaveEmpty: Found empty networks
    CheckNetworks --> Listening: All networks have containers
    LeaveEmpty --> Listening: Empty networks left

    state ScanNetworks {
        [*] --> FindBridge
        FindBridge --> CheckContainers
        CheckContainers --> [*]
    }

    state CheckNetworks {
        [*] --> GetCurrentNetworks
        GetCurrentNetworks --> CheckEachNetwork
        CheckEachNetwork --> [*]
    }
```

### 3. Simplified Network Operations

```mermaid
graph TD
    A[Network Operation Request] --> B[Execute Operation]
    B --> C{Success?}
    C -->|Yes| D[Operation Complete]
    C -->|No| E[Log Error & Exit Process]

    E --> F[Container Restart]
    F --> G[Fresh Service Start]

    style A fill:#e3f2fd
    style D fill:#e8f5e8
    style E fill:#ffebee
    style F fill:#fff3e0
    style G fill:#e1f5fe
```

## Key Components

### NetworkJoiner Service

- **Purpose**: Manages automatic network joining/leaving for the HTTP proxy
- **Interface**: Implements `service.EventHandler`
- **Configuration**: Uses `NetworkJoinerConfig` with HTTP proxy container name

### Network Discovery Process

1. **Find Bridge Networks**: Lists all Docker bridge networks
2. **Filter Networks**: Excludes default bridge and non-bridge networks
3. **Check for Manageable Containers**: Looks for containers with `VIRTUAL_HOST` env vars or Traefik labels
4. **Calculate Operations**: Determines which networks to join/leave

### Failure Handling Strategy

- **Fail-Fast Approach**: Any network operation failure causes immediate process exit
- **Container Restart**: Relies on Docker/Kubernetes to restart the service automatically
- **Retry Logic**: Built into Docker API calls for transient failures
- **Clean State**: Each restart starts with a fresh scan of the current state

### Event Handling

- **Container Start**: Triggers network re-scan to join new networks
- **Container Stop**: Checks for empty networks that can be safely left
- **Filtering**: Only processes events for manageable containers

## Configuration

The service is configured via command-line flags:

- `--container-name`: Name of the HTTP proxy container (default: "http-proxy")
- `--log-level`: Logging verbosity level (default: "info")

### Internal Configuration Constants

- **Max Retries**: 3 attempts for Docker API operations
- **Retry Delay**: 2-second delay between retry attempts
- **Bridge Driver**: Only processes "bridge" type networks
- **Default Bridge Protection**: Never disconnects from the default Docker bridge network

## Error Handling

The service uses a simplified, fail-fast error handling approach:

- **Network Operation Failures**: Logged and process exits immediately
- **Docker API Errors**: Built-in retry logic with exponential backoff
- **Container Info Errors**: Process exits if critical information cannot be obtained
- **Service Recovery**: Container orchestration handles automatic restart and recovery

## Benefits

1. **Zero Configuration**: Automatically detects and connects to relevant networks
2. **Dynamic Management**: Responds to container lifecycle events in real-time
3. **Safety First**: Multiple safety checks prevent connectivity loss
4. **Efficient**: Only joins networks with manageable containers
5. **Resilient**: Handles failures with fail-fast approach and automatic restart recovery

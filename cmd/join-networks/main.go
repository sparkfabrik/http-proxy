package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/sparkfabrik/http-proxy/pkg/logger"
	"github.com/sparkfabrik/http-proxy/pkg/service"
	"github.com/sparkfabrik/http-proxy/pkg/utils"
)

const (
	bridgeDriverName    = "bridge"
	defaultBridgeOption = "com.docker.network.bridge.default_bridge"
	defaultBridgeName   = "bridge"
)

// NetworkJoiner manages automatic Docker network connections for the HTTP proxy container.
// It monitors Docker events and maintains optimal network connectivity by joining networks
// that contain manageable containers and leaving networks that become empty.
type NetworkJoiner struct {
	dockerClient           *client.Client
	logger                 *logger.Logger
	httpProxyContainerName string
}

// NetworkJoinerConfig holds configuration parameters for the NetworkJoiner service.
// HTTPProxyContainerName specifies which container to manage network connections for.
type NetworkJoinerConfig struct {
	HTTPProxyContainerName string
	LogLevel               string
}

// Validate checks if the configuration is valid
func (c *NetworkJoinerConfig) Validate() error {
	if strings.TrimSpace(c.HTTPProxyContainerName) == "" {
		return fmt.Errorf("container-name cannot be empty")
	}

	return utils.ValidateLogLevel(c.LogLevel)
}

// NewNetworkJoiner creates a new NetworkJoiner with configuration
func NewNetworkJoiner(cfg *NetworkJoinerConfig) *NetworkJoiner {
	return &NetworkJoiner{
		httpProxyContainerName: cfg.HTTPProxyContainerName,
	}
}

// GetName returns the service name for the EventHandler interface
func (nj *NetworkJoiner) GetName() string {
	return "join-networks"
}

// SetDependencies sets the Docker client and logger from the service framework
func (nj *NetworkJoiner) SetDependencies(dockerClient *client.Client, logger *logger.Logger) {
	nj.dockerClient = dockerClient
	nj.logger = logger
}

// HandleInitialScan scans all existing Docker bridge networks and connects the HTTP proxy
// to any networks that contain manageable containers (containers with VIRTUAL_HOST or traefik labels).
// This runs once at service startup to establish initial network connectivity.
func (nj *NetworkJoiner) HandleInitialScan(ctx context.Context) error {
	nj.logger.Debug("Performing initial network scan and join")
	return nj.performInitialNetworkJoin(ctx, nj.httpProxyContainerName)
}

// HandleEvent responds to Docker container lifecycle events to dynamically manage network connections.
// - Container 'start' events: Re-scans networks to join any new networks with manageable containers
// - Container 'die' events: Checks for empty networks (no manageable containers) and leaves them
// - Other events: Ignored to avoid unnecessary processing
func (nj *NetworkJoiner) HandleEvent(ctx context.Context, event events.Message) error {
	action := string(event.Action)
	switch action {
	case "start":
		return nj.handleContainerStart(ctx)
	case "die":
		return nj.handleContainerStop(ctx)
	default:
		nj.logger.Debug("Unhandled container action", "action", action)
		return nil
	}
}

// ContainerInfo consolidates essential container state from Docker API inspection.
// Focuses on network connections to minimize API calls and provide network context.
type ContainerInfo struct {
	ID       string
	Networks map[string]NetworkInfo
}

// NetworkOperation encapsulates a simple network management operation including
// the target container and planned join/leave operations.
type NetworkOperation struct {
	HTTPProxyContainerName string
	ContainerID            string
	ToJoin                 []string
	ToLeave                []string
}

// NetworkSet represents a set of network IDs for cleaner set operations
type NetworkSet map[string]bool

// Contains checks if a network ID is in the set
func (ns NetworkSet) Contains(networkID string) bool {
	return ns[networkID]
}

// Add adds a network ID to the set
func (ns NetworkSet) Add(networkID string) {
	ns[networkID] = true
}

// NetworkInfo contains details about a network connection
type NetworkInfo struct {
	ID      string
	Name    string
	Gateway string
	IP      string
}

// main parses command line arguments and runs the network join service
func main() {
	containerName := flag.String("container-name", "http-proxy", "the name of this docker container")
	logLevel := flag.String("log-level", "info", "log level (debug, info, warn, error)")
	flag.Parse()

	// Create and validate configuration
	cfg := &NetworkJoinerConfig{
		HTTPProxyContainerName: *containerName,
		LogLevel:               *logLevel,
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	// Create the handler
	handler := NewNetworkJoiner(cfg)

	// Run the service using the shared service framework
	ctx := context.Background()
	if err := service.RunWithSignalHandling(ctx, "join-networks", cfg.LogLevel, handler); err != nil {
		fmt.Fprintf(os.Stderr, "Service failed: %v\n", err)
		os.Exit(1)
	}
}

// performInitialNetworkJoin orchestrates the network discovery and connection process.
// It inspects the HTTP proxy container's current state, discovers all bridge networks with
// manageable containers, calculates which networks to join/leave, and executes the operations.
func (nj *NetworkJoiner) performInitialNetworkJoin(ctx context.Context, containerProxy string) error {
	// Get current container state
	containerInfo, err := nj.getContainerInfo(ctx, containerProxy)
	if err != nil {
		return fmt.Errorf("failed to get container info: %w", err)
	}

	currentNetworks := make(NetworkSet)
	for networkID := range containerInfo.Networks {
		currentNetworks.Add(networkID)
	}

	bridgeNetworks, err := nj.getActiveBridgeNetworks(ctx, containerInfo.ID)
	if err != nil {
		return fmt.Errorf("failed to get bridge networks: %w", err)
	}

	defaultBridgeID, err := nj.getDefaultBridgeNetworkID(ctx)
	if err != nil {
		nj.logger.Warn("Could not identify default bridge network", "error", err)
	}

	toJoin := nj.getNetworksToJoin(currentNetworks, bridgeNetworks)
	toLeave := nj.getNetworksToLeave(currentNetworks, bridgeNetworks, defaultBridgeID)

	nj.logger.Info("Network operation plan",
		"current_networks", len(currentNetworks),
		"bridge_networks", len(bridgeNetworks),
		"to_join", len(toJoin),
		"to_leave", len(toLeave))

	// Create operation struct
	operation := &NetworkOperation{
		HTTPProxyContainerName: containerProxy,
		ContainerID:            containerInfo.ID,
		ToJoin:                 toJoin,
		ToLeave:                toLeave,
	}

	return nj.performNetworkOperations(ctx, operation)
}

// handleContainerStart responds to container start events by re-scanning all networks
// to detect newly created networks or networks that now contain manageable containers.
// This ensures the HTTP proxy can immediately route to new services without manual intervention.
func (nj *NetworkJoiner) handleContainerStart(ctx context.Context) error {
	// Re-scan and join any new bridge networks
	nj.logger.Debug("Container started, checking for new networks to join")
	return nj.performInitialNetworkJoin(ctx, nj.httpProxyContainerName)
}

// handleContainerStop responds to container stop events by identifying networks that
// no longer contain any manageable containers and safely disconnecting from them.
// This prevents the HTTP proxy from staying connected to unused networks, optimizing resource usage.
func (nj *NetworkJoiner) handleContainerStop(ctx context.Context) error {
	nj.logger.Debug("Container stopped, checking for empty networks to leave")

	// Get current container info
	containerInfo, err := nj.getContainerInfo(ctx, nj.httpProxyContainerName)
	if err != nil {
		return fmt.Errorf("failed to get container info: %w", err)
	}

	// Check each network the container is connected to
	var networksToLeave []string
	for networkID := range containerInfo.Networks {
		// Skip default bridge network
		defaultBridgeID, err := nj.getDefaultBridgeNetworkID(ctx)
		if err == nil && networkID == defaultBridgeID {
			continue
		}

		// Check if network has any manageable containers
		hasActiveContainers, err := utils.HasManageableContainersInNetwork(ctx, nj.dockerClient, networkID, nj.httpProxyContainerName)
		if err != nil {
			nj.logger.Warn("Failed to check network for manageable containers",
				"network_id", utils.FormatDockerID(networkID), "error", err)
			continue
		}

		if !hasActiveContainers {
			networksToLeave = append(networksToLeave, networkID)
		}
	}

	if len(networksToLeave) > 0 {
		nj.logger.Info("Found empty networks to leave", "count", len(networksToLeave))

		// Leave empty networks
		for _, networkID := range networksToLeave {
			if err := nj.safeLeaveNetwork(ctx, nj.httpProxyContainerName, networkID); err != nil {
				nj.logger.Error("Failed to leave empty network",
					"network_id", utils.FormatDockerID(networkID), "error", err)
			}
		}
	}

	return nil
}

// getContainerInfo performs a comprehensive Docker API inspection of the specified container,
// extracting network connections, port bindings, and connectivity status in a single API call.
// This optimizes performance by avoiding multiple API calls and provides complete container state.
func (nj *NetworkJoiner) getContainerInfo(ctx context.Context, containerName string) (*ContainerInfo, error) {
	containerJSON, err := nj.dockerClient.ContainerInspect(ctx, containerName)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container %s: %w", containerName, err)
	}

	networks := make(map[string]NetworkInfo)

	for networkName, networkData := range containerJSON.NetworkSettings.Networks {
		networks[networkData.NetworkID] = NetworkInfo{
			ID:      networkData.NetworkID,
			Name:    networkName,
			Gateway: networkData.Gateway,
			IP:      networkData.IPAddress,
		}
	}

	return &ContainerInfo{
		ID:       containerJSON.ID,
		Networks: networks,
	}, nil
}

// performNetworkOperations executes the planned network join/leave operations.
// Operations are performed in sequence: leave unwanted networks first, then join new networks.
// If any operation fails, the process exits to allow restart and recovery.
func (nj *NetworkJoiner) performNetworkOperations(ctx context.Context, op *NetworkOperation) error {
	// Execute leave operations first
	if len(op.ToLeave) > 0 {
		if err := nj.executeLeaveOperations(ctx, op); err != nil {
			return err
		}
	}

	// Execute join operations
	if len(op.ToJoin) > 0 {
		return nj.executeJoinOperations(ctx, op)
	}

	return nil
}

// executeJoinOperations connects the HTTP proxy to each specified network.
// If any operation fails, the process will exit and restart.
func (nj *NetworkJoiner) executeJoinOperations(ctx context.Context, op *NetworkOperation) error {
	for _, networkID := range op.ToJoin {
		if err := utils.CheckContext(ctx); err != nil {
			return err
		}

		if err := nj.safeJoinNetwork(ctx, op.HTTPProxyContainerName, networkID); err != nil {
			nj.logger.Error("Failed to join network", "network_id", utils.FormatDockerID(networkID), "error", err)
			return err
		}
	}
	return nil
}

// executeLeaveOperations disconnects the HTTP proxy from specified networks.
// If any operation fails, the process will exit and restart.
func (nj *NetworkJoiner) executeLeaveOperations(ctx context.Context, op *NetworkOperation) error {
	for _, networkID := range op.ToLeave {
		if err := utils.CheckContext(ctx); err != nil {
			return err
		}

		if err := nj.safeLeaveNetwork(ctx, op.HTTPProxyContainerName, networkID); err != nil {
			nj.logger.Error("Failed to leave network", "network_id", utils.FormatDockerID(networkID), "error", err)
			return err
		}
	}
	return nil
}

// safeJoinNetwork connects the HTTP proxy container to a specified network.
func (nj *NetworkJoiner) safeJoinNetwork(ctx context.Context, containerName, networkID string) error {
	netName := nj.getNetworkName(ctx, networkID)
	nj.logger.Info("Joining network", "name", netName, "id", utils.FormatDockerID(networkID))

	err := nj.dockerClient.NetworkConnect(ctx, networkID, containerName, &network.EndpointSettings{})
	if err != nil {
		nj.logger.Error("Failed to join network", "name", netName, "id", utils.FormatDockerID(networkID), "error", err)
		return fmt.Errorf("failed to join network %s: %w", utils.FormatDockerID(networkID), err)
	}

	nj.logger.Debug("Successfully joined network", "name", netName, "id", utils.FormatDockerID(networkID))
	return nil
}

// safeLeaveNetwork disconnects the HTTP proxy container from a specified network.
// The 'force' flag ensures disconnection even if the container is running.
func (nj *NetworkJoiner) safeLeaveNetwork(ctx context.Context, containerName, networkID string) error {
	netName := nj.getNetworkName(ctx, networkID)
	nj.logger.Info("Leaving network", "name", netName, "id", utils.FormatDockerID(networkID))

	err := nj.dockerClient.NetworkDisconnect(ctx, networkID, containerName, true)
	if err != nil {
		nj.logger.Error("Failed to leave network", "name", netName, "id", utils.FormatDockerID(networkID), "error", err)
		return fmt.Errorf("failed to leave network %s: %w", utils.FormatDockerID(networkID), err)
	}

	nj.logger.Debug("Successfully left network", "name", netName, "id", utils.FormatDockerID(networkID))
	return nil
}

// getNetworkName retrieves the human-readable name for a network ID for logging purposes.
// Falls back to a formatted ID if the network name cannot be determined, ensuring
// consistent logging even when networks are in transitional states.
func (nj *NetworkJoiner) getNetworkName(ctx context.Context, networkID string) string {
	if netResource, err := nj.dockerClient.NetworkInspect(ctx, networkID, network.InspectOptions{}); err == nil {
		return netResource.Name
	}
	return "unknown"
}

// getDefaultBridgeNetworkID identifies the Docker default bridge network by name and driver.
// The default bridge is excluded from automatic management because it contains system
// containers and should not be used for custom application routing.
func (nj *NetworkJoiner) getDefaultBridgeNetworkID(ctx context.Context) (string, error) {
	networks, err := nj.dockerClient.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return "", err
	}

	for _, net := range networks {
		if net.Name == defaultBridgeName && net.Driver == bridgeDriverName {
			return net.ID, nil
		}
	}
	return "", fmt.Errorf("default bridge network not found")
}

// getActiveBridgeNetworks discovers all Docker bridge networks that contain manageable containers.
// Scans each bridge network to identify containers with VIRTUAL_HOST environment variables
// or Traefik labels, excluding the HTTP proxy container itself and any non-manageable containers.
// Only considers containers that have dinghy env vars (VIRTUAL_HOST) or traefik labels
func (nj *NetworkJoiner) getActiveBridgeNetworks(ctx context.Context, containerID string) (NetworkSet, error) {
	networks := make(NetworkSet)

	allNetworks, err := nj.dockerClient.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list networks: %w", err)
	}

	for _, netOverview := range allNetworks {
		if netOverview.Driver != bridgeDriverName {
			continue
		}

		net, err := nj.dockerClient.NetworkInspect(ctx, netOverview.ID, network.InspectOptions{})
		if err != nil {
			nj.logger.Warn("Failed to get info for network", "network_id", netOverview.ID, "error", err)
			continue
		}

		isDefaultBridge := net.Options[defaultBridgeOption] == "true" || net.Name == defaultBridgeName

		// Always include default bridge
		if isDefaultBridge {
			networks.Add(net.ID)
			nj.logger.Debug("Including default bridge network",
				"name", net.Name,
				"id", utils.FormatDockerID(net.ID))
			continue
		}

		// For non-default networks, only include if they have manageable containers
		hasManageableContainers, err := utils.HasManageableContainersInNetwork(ctx, nj.dockerClient, net.ID, containerID)
		if err != nil {
			nj.logger.Warn("Failed to check network for manageable containers",
				"network_id", utils.FormatDockerID(net.ID), "error", err)
			continue
		}

		if hasManageableContainers {
			networks.Add(net.ID)
			nj.logger.Info("Including bridge network with manageable containers",
				"name", net.Name,
				"id", utils.FormatDockerID(net.ID))
		} else {
			nj.logger.Debug("Skipping network without manageable containers",
				"name", net.Name,
				"id", utils.FormatDockerID(net.ID))
		}
	}

	return networks, nil
}

// getNetworksToJoin calculates which bridge networks the HTTP proxy should connect to
// by comparing currently connected networks against networks containing manageable containers.
// Returns networks that have manageable containers but are not yet connected to the proxy.
func (nj *NetworkJoiner) getNetworksToJoin(currentNetworks, bridgeNetworks NetworkSet) []string {
	var networkIDs []string
	for networkID := range bridgeNetworks {
		if !currentNetworks.Contains(networkID) {
			networkIDs = append(networkIDs, networkID)
		}
	}
	return networkIDs
}

// getNetworksToLeave identifies networks the HTTP proxy should disconnect from because
// they no longer contain manageable containers. Excludes the default bridge network
// to maintain basic Docker connectivity and only disconnects from networks without manageable containers.
func (nj *NetworkJoiner) getNetworksToLeave(currentNetworks, bridgeNetworks NetworkSet, defaultBridgeID string) []string {
	var networkIDs []string

	for networkID := range currentNetworks {
		if networkID == defaultBridgeID {
			nj.logger.Debug("Protecting default bridge network from disconnection", "network_id", utils.FormatDockerID(networkID))
			continue
		}

		if !bridgeNetworks.Contains(networkID) {
			networkIDs = append(networkIDs, networkID)
		}
	}
	return networkIDs
}

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/sparkfabrik/http-proxy/pkg/logger"
	"github.com/sparkfabrik/http-proxy/pkg/service"
	"github.com/sparkfabrik/http-proxy/pkg/utils"
)

const (
	bridgeDriverName    = "bridge"
	defaultBridgeOption = "com.docker.network.bridge.default_bridge"
	defaultBridgeName   = "bridge"
	maxRetries          = 3
	retryDelay          = 2 * time.Second
	stabilizationDelay  = 1 * time.Second
)

// NetworkJoiner handles joining/leaving Docker networks and implements service.EventHandler
type NetworkJoiner struct {
	dockerClient  *client.Client
	logger        *logger.Logger
	containerName string
}

// NetworkJoinerConfig holds configuration for the NetworkJoiner service
type NetworkJoinerConfig struct {
	ContainerName string
	LogLevel      string
}

// Validate checks if the configuration is valid
func (c *NetworkJoinerConfig) Validate() error {
	if strings.TrimSpace(c.ContainerName) == "" {
		return fmt.Errorf("container-name is required")
	}

	return utils.ValidateLogLevel(c.LogLevel)
}

// NewNetworkJoiner creates a new NetworkJoiner with configuration
func NewNetworkJoiner(cfg *NetworkJoinerConfig) *NetworkJoiner {
	return &NetworkJoiner{
		containerName: cfg.ContainerName,
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

// HandleInitialScan performs the initial network scan and join for the EventHandler interface
func (nj *NetworkJoiner) HandleInitialScan(ctx context.Context) error {
	nj.logger.Info("Performing initial network scan and join")
	return nj.performInitialNetworkJoin(ctx, nj.containerName)
}

// HandleEvent processes Docker events for the EventHandler interface
func (nj *NetworkJoiner) HandleEvent(ctx context.Context, event events.Message) error {
	action := string(event.Action)
	switch action {
	case "start":
		return nj.handleContainerStart(ctx, nj.containerName)
	case "die":
		return nj.handleContainerStop(ctx, nj.containerName)
	default:
		nj.logger.Debug("Unhandled container action", "action", action)
		return nil
	}
}

// ContainerInfo holds comprehensive container information from a single Docker API call
type ContainerInfo struct {
	ID           string
	Networks     map[string]NetworkInfo
	PortBindings nat.PortMap
	HasExternal  bool
}

// NetworkOperation holds parameters for network join/leave operations
type NetworkOperation struct {
	ContainerName string
	ContainerID   string
	ToJoin        []string
	ToLeave       []string
	OriginalState *NetworkState
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

// NetworkState represents the network configuration state of a container
type NetworkState struct {
	Networks     map[string]NetworkInfo
	PortBindings nat.PortMap
	HasExternal  bool
}

// NetworkInfo contains details about a network connection
type NetworkInfo struct {
	ID      string
	Name    string
	Gateway string
	IP      string
}

func (ns *NetworkState) summary() string {
	return fmt.Sprintf("networks=%d, ports=%d, external=%t",
		len(ns.Networks), len(ns.PortBindings), ns.HasExternal)
}

// main parses command line arguments and runs the network join service
func main() {
	containerName := flag.String("container-name", "", "the name of this docker container")
	logLevel := flag.String("log-level", "info", "log level (debug, info, warn, error)")
	flag.Parse()

	// Create and validate configuration
	cfg := &NetworkJoinerConfig{
		ContainerName: *containerName,
		LogLevel:      *logLevel,
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

// performInitialNetworkJoin handles the initial scan and join of existing networks
func (nj *NetworkJoiner) performInitialNetworkJoin(ctx context.Context, containerName string) error {
	// Single comprehensive container inspection
	containerInfo, err := nj.getContainerInfo(ctx, containerName)
	if err != nil {
		return fmt.Errorf("failed to get container info: %w", err)
	}

	preState := &NetworkState{
		Networks:     containerInfo.Networks,
		PortBindings: containerInfo.PortBindings,
		HasExternal:  containerInfo.HasExternal,
	}
	nj.logger.Info("Pre-operation state", "state", preState.summary())

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

	// Create operation struct to reduce parameter passing
	operation := &NetworkOperation{
		ContainerName: containerName,
		ContainerID:   containerInfo.ID,
		ToJoin:        toJoin,
		ToLeave:       toLeave,
		OriginalState: preState,
	}

	if err := nj.performNetworkOperations(ctx, operation); err != nil {
		return fmt.Errorf("network operations failed: %w", err)
	}

	nj.logger.Info("Initial network operations completed successfully")
	return nil
}

// handleContainerStart processes container start events to join new networks
func (nj *NetworkJoiner) handleContainerStart(ctx context.Context, containerName string) error {
	// Re-scan and join any new bridge networks
	nj.logger.Debug("Container started, checking for new networks to join")
	return nj.performInitialNetworkJoin(ctx, containerName)
}

// handleContainerStop processes container stop events to leave empty networks
func (nj *NetworkJoiner) handleContainerStop(ctx context.Context, containerName string) error {
	nj.logger.Debug("Container stopped, checking for empty networks to leave")

	// Get current container info
	containerInfo, err := nj.getContainerInfo(ctx, containerName)
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

		// Check if network has any other active containers
		hasActiveContainers, err := nj.networkHasActiveContainers(ctx, networkID, containerName)
		if err != nil {
			nj.logger.Warn("Failed to check network for active containers",
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
			if err := nj.safeLeaveNetwork(ctx, containerName, networkID); err != nil {
				nj.logger.Error("Failed to leave empty network",
					"network_id", utils.FormatDockerID(networkID), "error", err)
			}
		}
	}

	return nil
}

// networkHasActiveContainers checks if a network has any active containers (excluding the specified container)
func (nj *NetworkJoiner) networkHasActiveContainers(ctx context.Context, networkID, excludeContainer string) (bool, error) {
	// Get all containers
	containers, err := nj.dockerClient.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return false, fmt.Errorf("failed to list containers: %w", err)
	}

	for _, cont := range containers {
		// Skip the container we're excluding and non-running containers
		if cont.State != "running" {
			continue
		}

		// Skip if it's the container we're checking for
		containerName := strings.TrimPrefix(cont.Names[0], "/")
		if containerName == excludeContainer {
			continue
		}

		// Check if this container is connected to the network
		inspect, err := nj.dockerClient.ContainerInspect(ctx, cont.ID)
		if err != nil {
			continue // Skip containers we can't inspect
		}

		for _, networkData := range inspect.NetworkSettings.Networks {
			if networkData.NetworkID == networkID {
				return true, nil // Found an active container on this network
			}
		}
	}

	return false, nil // No active containers found on this network
}

// getContainerInfo performs a single container inspection and returns comprehensive information
func (nj *NetworkJoiner) getContainerInfo(ctx context.Context, containerName string) (*ContainerInfo, error) {
	containerJSON, err := nj.dockerClient.ContainerInspect(ctx, containerName)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container %s: %w", containerName, err)
	}

	networks := make(map[string]NetworkInfo)
	hasExternal := false

	for networkName, networkData := range containerJSON.NetworkSettings.Networks {
		networks[networkData.NetworkID] = NetworkInfo{
			ID:      networkData.NetworkID,
			Name:    networkName,
			Gateway: networkData.Gateway,
			IP:      networkData.IPAddress,
		}
		if networkData.Gateway != "" {
			hasExternal = true
		}
	}

	return &ContainerInfo{
		ID:           containerJSON.ID,
		Networks:     networks,
		PortBindings: containerJSON.NetworkSettings.Ports,
		HasExternal:  hasExternal,
	}, nil
}

// performNetworkOperations executes network join/leave operations with rollback capability
func (nj *NetworkJoiner) performNetworkOperations(ctx context.Context, op *NetworkOperation) error {
	var operationsPerformed []func() error

	defer func() {
		if r := recover(); r != nil {
			nj.logger.Error("PANIC during network operations, attempting rollback", "panic", r)
			nj.rollbackOperations(operationsPerformed)
		}
	}()

	// Execute join operations
	if len(op.ToJoin) > 0 {
		if err := nj.executeJoinOperations(ctx, op, &operationsPerformed); err != nil {
			return err
		}
	}

	// Execute leave operations
	if len(op.ToLeave) > 0 {
		if err := nj.executeLeaveOperations(ctx, op); err != nil {
			return err
		}
	}

	// Validate final state
	return nj.validateFinalState(ctx, op)
}

// executeJoinOperations handles joining networks with rollback tracking
func (nj *NetworkJoiner) executeJoinOperations(ctx context.Context, op *NetworkOperation, operationsPerformed *[]func() error) error {
	for _, networkID := range op.ToJoin {
		if err := utils.CheckContext(ctx); err != nil {
			return err
		}

		if err := nj.safeJoinNetwork(ctx, op.ContainerName, networkID); err != nil {
			nj.logger.Error("Failed to join network, rolling back", "network_id", utils.FormatDockerID(networkID), "error", err)
			nj.rollbackOperations(*operationsPerformed)
			return err
		}

		*operationsPerformed = append(*operationsPerformed, func() error {
			return nj.safeLeaveNetwork(ctx, op.ContainerName, networkID)
		})

		time.Sleep(stabilizationDelay)

		if err := nj.quickConnectivityCheck(ctx, op.ContainerID); err != nil {
			nj.logger.Error("Connectivity lost after joining network, rolling back", "network_id", utils.FormatDockerID(networkID), "error", err)
			nj.rollbackOperations(*operationsPerformed)
			return fmt.Errorf("connectivity lost after joining network: %w", err)
		}
	}
	return nil
}

// executeLeaveOperations handles leaving networks with connectivity protection
func (nj *NetworkJoiner) executeLeaveOperations(ctx context.Context, op *NetworkOperation) error {
	for _, networkID := range op.ToLeave {
		if err := utils.CheckContext(ctx); err != nil {
			return err
		}

		if err := nj.ensureAlternativeConnectivity(ctx, op.ContainerID, networkID); err != nil {
			nj.logger.Warn("Cannot leave network: would lose connectivity", "network_id", utils.FormatDockerID(networkID), "error", err)
			continue
		}

		if err := nj.safeLeaveNetwork(ctx, op.ContainerName, networkID); err != nil {
			nj.logger.Warn("Failed to leave network", "network_id", utils.FormatDockerID(networkID), "error", err)
			continue
		}

		time.Sleep(stabilizationDelay)

		if err := nj.quickConnectivityCheck(ctx, op.ContainerID); err != nil {
			nj.logger.Error("Connectivity lost after leaving network, attempting to rejoin", "network_id", utils.FormatDockerID(networkID), "error", err)
			if rejoinErr := nj.safeJoinNetwork(ctx, op.ContainerName, networkID); rejoinErr != nil {
				nj.logger.Error("Failed to rejoin network", "network_id", utils.FormatDockerID(networkID), "error", rejoinErr)
			}
			return fmt.Errorf("connectivity lost after leaving network: %w", err)
		}
	}
	return nil
}

// validateFinalState ensures the operation didn't break external connectivity
func (nj *NetworkJoiner) validateFinalState(ctx context.Context, op *NetworkOperation) error {
	// Use containerName to get fresh state info
	finalInfo, err := nj.getContainerInfo(ctx, op.ContainerName)
	if err != nil {
		return fmt.Errorf("failed to capture final state: %w", err)
	}

	finalState := &NetworkState{
		Networks:     finalInfo.Networks,
		PortBindings: finalInfo.PortBindings,
		HasExternal:  finalInfo.HasExternal,
	}

	nj.logger.Info("Final state", "state", finalState.summary())

	if !finalState.HasExternal && op.OriginalState.HasExternal {
		return fmt.Errorf("lost external connectivity during operations")
	}

	return nil
}

// safeJoinNetwork connects a container to a network with retry logic
func (nj *NetworkJoiner) safeJoinNetwork(ctx context.Context, containerName, networkID string) error {
	netName := nj.getNetworkName(ctx, networkID)
	nj.logger.Info("Joining network", "name", netName, "id", utils.FormatDockerID(networkID))

	return utils.RetryOperation(func() error {
		return nj.dockerClient.NetworkConnect(ctx, networkID, containerName, &network.EndpointSettings{})
	}, fmt.Sprintf("join network %s", utils.FormatDockerID(networkID)), maxRetries, retryDelay, nj.logger)
}

// safeLeaveNetwork disconnects a container from a network with retry logic
func (nj *NetworkJoiner) safeLeaveNetwork(ctx context.Context, containerName, networkID string) error {
	netName := nj.getNetworkName(ctx, networkID)
	nj.logger.Info("Leaving network", "name", netName, "id", utils.FormatDockerID(networkID))

	return utils.RetryOperation(func() error {
		return nj.dockerClient.NetworkDisconnect(ctx, networkID, containerName, true)
	}, fmt.Sprintf("leave network %s", utils.FormatDockerID(networkID)), maxRetries, retryDelay, nj.logger)
}

// quickConnectivityCheck verifies that the container maintains network connectivity
// For efficiency, it performs a fresh container inspection only when needed
func (nj *NetworkJoiner) quickConnectivityCheck(ctx context.Context, containerID string) error {
	containerJSON, err := nj.dockerClient.ContainerInspect(ctx, containerID)
	if err != nil {
		return fmt.Errorf("failed to inspect container: %w", err)
	}

	networkCount := len(containerJSON.NetworkSettings.Networks)
	if networkCount == 0 {
		return fmt.Errorf("container has no network connections")
	}

	for _, networkData := range containerJSON.NetworkSettings.Networks {
		if networkData.Gateway != "" && networkData.IPAddress != "" {
			return nil
		}
	}

	return fmt.Errorf("no external connectivity found")
}

// ensureAlternativeConnectivity checks if leaving a network would break external connectivity
func (nj *NetworkJoiner) ensureAlternativeConnectivity(ctx context.Context, containerID, networkToLeave string) error {
	containerJSON, err := nj.dockerClient.ContainerInspect(ctx, containerID)
	if err != nil {
		return err
	}

	externalConnections := 0
	for _, networkData := range containerJSON.NetworkSettings.Networks {
		if networkData.NetworkID != networkToLeave && networkData.Gateway != "" {
			externalConnections++
		}
	}

	if externalConnections == 0 {
		return fmt.Errorf("leaving this network would remove last external connection")
	}

	return nil
}

// rollbackOperations executes cleanup operations in reverse order
func (nj *NetworkJoiner) rollbackOperations(operations []func() error) {
	nj.logger.Info("Rolling back operations", "operation_count", len(operations))

	for i := len(operations) - 1; i >= 0; i-- {
		if err := operations[i](); err != nil {
			nj.logger.Error("Rollback operation failed", "operation_index", i, "error", err)
		}
	}
}

// getNetworkName retrieves the human-readable name for a network ID
func (nj *NetworkJoiner) getNetworkName(ctx context.Context, networkID string) string {
	if netResource, err := nj.dockerClient.NetworkInspect(ctx, networkID, network.InspectOptions{}); err == nil {
		return netResource.Name
	}
	return "unknown"
}

// getDefaultBridgeNetworkID finds the ID of the default Docker bridge network
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

// getActiveBridgeNetworks returns bridge networks that should be joined based on activity criteria
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

		_, containsSelf := net.Containers[containerID]
		isDefaultBridge := net.Options[defaultBridgeOption] == "true" || net.Name == defaultBridgeName
		hasMultipleContainers := len(net.Containers) > 1
		hasOtherContainers := len(net.Containers) == 1 && !containsSelf

		if isDefaultBridge || hasMultipleContainers || hasOtherContainers {
			networks.Add(net.ID)
			nj.logger.Info("Including bridge network",
				"name", net.Name,
				"id", utils.FormatDockerID(net.ID),
				"is_default", isDefaultBridge,
				"multiple_containers", hasMultipleContainers,
				"other_containers", hasOtherContainers)
		}
	}

	return networks, nil
}

// getNetworksToJoin returns networks that the container should join
func (nj *NetworkJoiner) getNetworksToJoin(currentNetworks, bridgeNetworks NetworkSet) []string {
	var networkIDs []string
	for networkID := range bridgeNetworks {
		if !currentNetworks.Contains(networkID) {
			networkIDs = append(networkIDs, networkID)
		}
	}
	return networkIDs
}

// getNetworksToLeave returns networks that the container should leave, protecting the default bridge
func (nj *NetworkJoiner) getNetworksToLeave(currentNetworks, bridgeNetworks NetworkSet, defaultBridgeID string) []string {
	var networkIDs []string

	for networkID := range currentNetworks {
		if networkID == defaultBridgeID {
			nj.logger.Info("Protecting default bridge network from disconnection", "network_id", utils.FormatDockerID(networkID))
			continue
		}

		if !bridgeNetworks.Contains(networkID) {
			networkIDs = append(networkIDs, networkID)
		}
	}
	return networkIDs
}

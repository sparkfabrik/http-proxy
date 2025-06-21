package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/sparkfabrik/http-proxy/pkg/logger"
)

const (
	bridgeDriverName    = "bridge"
	defaultBridgeOption = "com.docker.network.bridge.default_bridge"
	defaultBridgeName   = "bridge"
	maxRetries          = 3
	retryDelay          = 2 * time.Second
	stabilizationDelay  = 1 * time.Second
)

// NetworkJoiner handles joining/leaving Docker networks with dependency injection
type NetworkJoiner struct {
	dockerClient *client.Client
	logger       *logger.Logger
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

// formatNetworkID returns a shortened network ID for logging
func (nj *NetworkJoiner) formatNetworkID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// checkContext returns an error if the context is cancelled
func (nj *NetworkJoiner) checkContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		nj.logger.Info("Shutdown signal received, stopping network operations")
		return ctx.Err()
	default:
		return nil
	}
}

// NewNetworkJoiner creates a new NetworkJoiner with injected dependencies
func NewNetworkJoiner(dockerClient *client.Client, logger *logger.Logger) *NetworkJoiner {
	return &NetworkJoiner{
		dockerClient: dockerClient,
		logger:       logger,
	}
}

// main sets up signal handling and runs the network join application
func main() {
	containerName := flag.String("container-name", "", "the name of this docker container")
	dryRun := flag.Bool("dry-run", false, "show what would be done without making changes")
	flag.Parse()

	// Initialize dependencies
	log := logger.NewWithLevel("join-networks", logger.LevelInfo)

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Error("Failed to create Docker client", "error", err)
		os.Exit(1)
	}
	defer dockerClient.Close()

	// Create NetworkJoiner with dependency injection
	joiner := NewNetworkJoiner(dockerClient, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	errChan := make(chan error, 1)
	go func() {
		errChan <- joiner.Run(ctx, *containerName, *dryRun)
	}()

	select {
	case err := <-errChan:
		if err != nil {
			log.Error("Application failed", "error", err)
			os.Exit(1)
		}
		log.Info("Application completed successfully")
	case sig := <-sigChan:
		log.Info("Received shutdown signal", "signal", sig)
		cancel()

		select {
		case err := <-errChan:
			if err != nil && err != context.Canceled {
				log.Error("Error during shutdown", "error", err)
			}
		case <-time.After(10 * time.Second):
			log.Warn("Shutdown timeout exceeded, forcing exit")
		}

		log.Info("Application shut down gracefully")
	}
}

// Run executes the main application logic for joining/leaving networks
func (nj *NetworkJoiner) Run(ctx context.Context, containerName string, dryRun bool) error {
	if strings.TrimSpace(containerName) == "" {
		return fmt.Errorf("container-name is required")
	}

	log := nj.logger

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
	log.Info("Pre-operation state", "state", preState.summary())

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
		log.Warn("Could not identify default bridge network", "error", err)
	}

	toJoin := nj.getNetworksToJoin(currentNetworks, bridgeNetworks)
	toLeave := nj.getNetworksToLeave(currentNetworks, bridgeNetworks, defaultBridgeID)

	log.Info("Network operation plan",
		"current_networks", len(currentNetworks),
		"bridge_networks", len(bridgeNetworks),
		"to_join", len(toJoin),
		"to_leave", len(toLeave))

	if dryRun {
		log.Info("DRY RUN MODE - No changes will be made")
		nj.logPlannedOperations(ctx, toJoin, toLeave)
		return nil
	}

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

	log.Info("Network operations completed successfully")
	return nil
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
		if err := nj.checkContext(ctx); err != nil {
			return err
		}

		if err := nj.safeJoinNetwork(ctx, op.ContainerName, networkID); err != nil {
			nj.logger.Error("Failed to join network, rolling back", "network_id", nj.formatNetworkID(networkID), "error", err)
			nj.rollbackOperations(*operationsPerformed)
			return err
		}

		*operationsPerformed = append(*operationsPerformed, func() error {
			return nj.safeLeaveNetwork(ctx, op.ContainerName, networkID)
		})

		time.Sleep(stabilizationDelay)

		if err := nj.quickConnectivityCheck(ctx, op.ContainerID); err != nil {
			nj.logger.Error("Connectivity lost after joining network, rolling back", "network_id", nj.formatNetworkID(networkID), "error", err)
			nj.rollbackOperations(*operationsPerformed)
			return fmt.Errorf("connectivity lost after joining network: %w", err)
		}
	}
	return nil
}

// executeLeaveOperations handles leaving networks with connectivity protection
func (nj *NetworkJoiner) executeLeaveOperations(ctx context.Context, op *NetworkOperation) error {
	for _, networkID := range op.ToLeave {
		if err := nj.checkContext(ctx); err != nil {
			return err
		}

		if err := nj.ensureAlternativeConnectivity(ctx, op.ContainerID, networkID); err != nil {
			nj.logger.Warn("Cannot leave network: would lose connectivity", "network_id", nj.formatNetworkID(networkID), "error", err)
			continue
		}

		if err := nj.safeLeaveNetwork(ctx, op.ContainerName, networkID); err != nil {
			nj.logger.Warn("Failed to leave network", "network_id", nj.formatNetworkID(networkID), "error", err)
			continue
		}

		time.Sleep(stabilizationDelay)

		if err := nj.quickConnectivityCheck(ctx, op.ContainerID); err != nil {
			nj.logger.Error("Connectivity lost after leaving network, attempting to rejoin", "network_id", nj.formatNetworkID(networkID), "error", err)
			if rejoinErr := nj.safeJoinNetwork(ctx, op.ContainerName, networkID); rejoinErr != nil {
				nj.logger.Error("Failed to rejoin network", "network_id", nj.formatNetworkID(networkID), "error", rejoinErr)
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
	nj.logger.Info("Joining network", "name", netName, "id", nj.formatNetworkID(networkID))

	return nj.retryOperation(func() error {
		return nj.dockerClient.NetworkConnect(ctx, networkID, containerName, &network.EndpointSettings{})
	}, fmt.Sprintf("join network %s", nj.formatNetworkID(networkID)))
}

// safeLeaveNetwork disconnects a container from a network with retry logic
func (nj *NetworkJoiner) safeLeaveNetwork(ctx context.Context, containerName, networkID string) error {
	netName := nj.getNetworkName(ctx, networkID)
	nj.logger.Info("Leaving network", "name", netName, "id", nj.formatNetworkID(networkID))

	return nj.retryOperation(func() error {
		return nj.dockerClient.NetworkDisconnect(ctx, networkID, containerName, true)
	}, fmt.Sprintf("leave network %s", nj.formatNetworkID(networkID)))
}

// retryOperation executes an operation with configurable retry attempts
func (nj *NetworkJoiner) retryOperation(operation func() error, description string) error {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := operation(); err != nil {
			lastErr = err
			if attempt < maxRetries {
				nj.logger.Warn("Operation attempt failed, retrying",
					"attempt", attempt,
					"max_attempts", maxRetries,
					"operation", description,
					"error", err,
					"retry_delay", retryDelay)

				timer := time.NewTimer(retryDelay)
				<-timer.C
				timer.Stop()
				continue
			}
		} else {
			if attempt > 1 {
				nj.logger.Info("Operation succeeded after retry",
					"operation", description,
					"attempt", attempt)
			}
			return nil
		}
	}

	return fmt.Errorf("operation %s failed after %d attempts: %w", description, maxRetries, lastErr)
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

// logPlannedOperations displays what network operations would be performed in dry-run mode
func (nj *NetworkJoiner) logPlannedOperations(ctx context.Context, toJoin, toLeave []string) {
	if len(toJoin) > 0 {
		nj.logger.Info("Would JOIN networks:")
		for _, networkID := range toJoin {
			name := nj.getNetworkName(ctx, networkID)
			nj.logger.Info("  - Network to join", "name", name, "id", nj.formatNetworkID(networkID))
		}
	}

	if len(toLeave) > 0 {
		nj.logger.Info("Would LEAVE networks:")
		for _, networkID := range toLeave {
			name := nj.getNetworkName(ctx, networkID)
			nj.logger.Info("  - Network to leave", "name", name, "id", nj.formatNetworkID(networkID))
		}
	}
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
				"id", nj.formatNetworkID(net.ID),
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
			nj.logger.Info("Protecting default bridge network from disconnection", "network_id", nj.formatNetworkID(networkID))
			continue
		}

		if !bridgeNetworks.Contains(networkID) {
			networkIDs = append(networkIDs, networkID)
		}
	}
	return networkIDs
}

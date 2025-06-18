package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

const (
	bridgeDriverName    = "bridge"
	defaultBridgeOption = "com.docker.network.bridge.default_bridge"
	defaultBridgeName   = "bridge"
	maxRetries          = 3
	retryDelay          = 2 * time.Second
	stabilizationDelay  = 1 * time.Second
)

// main sets up signal handling and runs the network join application
func main() {
	containerName := flag.String("container-name", "", "the name of this docker container")
	dryRun := flag.Bool("dry-run", false, "show what would be done without making changes")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	errChan := make(chan error, 1)
	go func() {
		errChan <- run(ctx, *containerName, *dryRun)
	}()

	select {
	case err := <-errChan:
		if err != nil {
			log.Fatalf("Error: %v", err)
		}
		log.Println("Application completed successfully")
	case sig := <-sigChan:
		log.Printf("Received signal %v, initiating graceful shutdown...", sig)
		cancel()

		select {
		case err := <-errChan:
			if err != nil && err != context.Canceled {
				log.Printf("Error during shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			log.Println("Shutdown timeout exceeded, forcing exit")
		}

		log.Println("Application shut down gracefully")
	}
}

// run executes the main application logic for joining/leaving networks
func run(ctx context.Context, containerName string, dryRun bool) error {
	if strings.TrimSpace(containerName) == "" {
		return fmt.Errorf("container-name is required")
	}

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("failed to create docker client: %w", err)
	}
	defer dockerClient.Close()

	containerID, err := getContainerID(ctx, dockerClient, containerName)
	if err != nil {
		return fmt.Errorf("failed to get container ID: %w", err)
	}

	preState, err := captureContainerNetworkState(ctx, dockerClient, containerID)
	if err != nil {
		return fmt.Errorf("failed to capture pre-operation state: %w", err)
	}
	log.Printf("Pre-operation state: %s", preState.summary())

	currentNetworks, err := getJoinedNetworks(ctx, dockerClient, containerID)
	if err != nil {
		return fmt.Errorf("failed to get current networks: %w", err)
	}

	bridgeNetworks, err := getActiveBridgeNetworks(ctx, dockerClient, containerID)
	if err != nil {
		return fmt.Errorf("failed to get bridge networks: %w", err)
	}

	defaultBridgeID, err := getDefaultBridgeNetworkID(ctx, dockerClient)
	if err != nil {
		log.Printf("Warning: could not identify default bridge network: %v", err)
	}

	toJoin := getNetworksToJoin(currentNetworks, bridgeNetworks)
	toLeave := getNetworksToLeave(currentNetworks, bridgeNetworks, defaultBridgeID)

	log.Printf("Plan: Currently in %d networks, found %d bridge networks, %d to join, %d to leave",
		len(currentNetworks), len(bridgeNetworks), len(toJoin), len(toLeave))

	if dryRun {
		log.Println("DRY RUN MODE - No changes will be made")
		logPlannedOperations(ctx, dockerClient, toJoin, toLeave)
		return nil
	}

	if err := performNetworkOperationsWithRollback(ctx, dockerClient, containerName, containerID, toJoin, toLeave, preState); err != nil {
		return fmt.Errorf("network operations failed: %w", err)
	}

	log.Println("Network operations completed successfully")
	return nil
}

type NetworkState struct {
	Networks     map[string]NetworkInfo
	PortBindings nat.PortMap
	HasExternal  bool
}

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

// captureContainerNetworkState takes a snapshot of container's current network configuration
func captureContainerNetworkState(ctx context.Context, dockerClient *client.Client, containerID string) (*NetworkState, error) {
	containerJSON, err := dockerClient.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, err
	}

	state := &NetworkState{
		Networks:     make(map[string]NetworkInfo),
		PortBindings: containerJSON.NetworkSettings.Ports,
		HasExternal:  false,
	}

	for networkName, networkData := range containerJSON.NetworkSettings.Networks {
		state.Networks[networkData.NetworkID] = NetworkInfo{
			ID:      networkData.NetworkID,
			Name:    networkName,
			Gateway: networkData.Gateway,
			IP:      networkData.IPAddress,
		}
		if networkData.Gateway != "" {
			state.HasExternal = true
		}
	}

	return state, nil
}

// performNetworkOperationsWithRollback executes network operations with automatic rollback on failure
func performNetworkOperationsWithRollback(ctx context.Context, dockerClient *client.Client, containerName, containerID string,
	toJoin, toLeave []string, originalState *NetworkState) error {

	var operationsPerformed []func() error

	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC during network operations, attempting rollback: %v", r)
			rollbackOperations(operationsPerformed)
		}
	}()

	for _, networkID := range toJoin {
		select {
		case <-ctx.Done():
			log.Println("Shutdown signal received, stopping network operations")
			return ctx.Err()
		default:
		}

		if err := safeJoinNetwork(ctx, dockerClient, containerName, networkID); err != nil {
			log.Printf("Failed to join network %s, rolling back...", networkID[:12])
			rollbackOperations(operationsPerformed)
			return err
		}

		operationsPerformed = append(operationsPerformed, func() error {
			return safeLeaveNetwork(ctx, dockerClient, containerName, networkID)
		})

		time.Sleep(stabilizationDelay)

		if err := quickConnectivityCheck(ctx, dockerClient, containerID); err != nil {
			log.Printf("Connectivity lost after joining %s, rolling back...", networkID[:12])
			rollbackOperations(operationsPerformed)
			return fmt.Errorf("connectivity lost after joining network: %w", err)
		}
	}

	for _, networkID := range toLeave {
		select {
		case <-ctx.Done():
			log.Println("Shutdown signal received, stopping network operations")
			return ctx.Err()
		default:
		}

		if err := ensureAlternativeConnectivity(ctx, dockerClient, containerID, networkID); err != nil {
			log.Printf("Cannot leave network %s: would lose connectivity: %v", networkID[:12], err)
			continue
		}

		if err := safeLeaveNetwork(ctx, dockerClient, containerName, networkID); err != nil {
			log.Printf("Warning: failed to leave network %s: %v", networkID[:12], err)
			continue
		}

		time.Sleep(stabilizationDelay)

		if err := quickConnectivityCheck(ctx, dockerClient, containerID); err != nil {
			log.Printf("Connectivity lost after leaving %s, attempting to rejoin...", networkID[:12])
			if rejoinErr := safeJoinNetwork(ctx, dockerClient, containerName, networkID); rejoinErr != nil {
				log.Printf("Failed to rejoin network %s: %v", networkID[:12], rejoinErr)
			}
			return fmt.Errorf("connectivity lost after leaving network: %w", err)
		}
	}

	finalState, err := captureContainerNetworkState(ctx, dockerClient, containerID)
	if err != nil {
		return fmt.Errorf("failed to capture final state: %w", err)
	}

	log.Printf("Final state: %s", finalState.summary())

	if !finalState.HasExternal && originalState.HasExternal {
		return fmt.Errorf("lost external connectivity during operations")
	}

	return nil
}

// safeJoinNetwork connects a container to a network with retry logic
func safeJoinNetwork(ctx context.Context, dockerClient *client.Client, containerName, networkID string) error {
	netName := getNetworkName(ctx, dockerClient, networkID)
	log.Printf("Joining network %s (%s)", netName, networkID[:12])

	return retryOperation(func() error {
		return dockerClient.NetworkConnect(ctx, networkID, containerName, &network.EndpointSettings{})
	}, fmt.Sprintf("join network %s", networkID[:12]))
}

// safeLeaveNetwork disconnects a container from a network with retry logic
func safeLeaveNetwork(ctx context.Context, dockerClient *client.Client, containerName, networkID string) error {
	netName := getNetworkName(ctx, dockerClient, networkID)
	log.Printf("Leaving network %s (%s)", netName, networkID[:12])

	return retryOperation(func() error {
		return dockerClient.NetworkDisconnect(ctx, networkID, containerName, true)
	}, fmt.Sprintf("leave network %s", networkID[:12]))
}

// retryOperation executes an operation with configurable retry attempts
func retryOperation(operation func() error, description string) error {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := operation(); err != nil {
			lastErr = err
			if attempt < maxRetries {
				log.Printf("Attempt %d/%d failed for %s: %v, retrying in %v...",
					attempt, maxRetries, description, err, retryDelay)

				timer := time.NewTimer(retryDelay)
				<-timer.C
				timer.Stop()
				continue
			}
		} else {
			if attempt > 1 {
				log.Printf("Operation %s succeeded on attempt %d", description, attempt)
			}
			return nil
		}
	}

	return fmt.Errorf("operation %s failed after %d attempts: %w", description, maxRetries, lastErr)
}

// quickConnectivityCheck verifies that the container maintains network connectivity
func quickConnectivityCheck(ctx context.Context, dockerClient *client.Client, containerID string) error {
	containerJSON, err := dockerClient.ContainerInspect(ctx, containerID)
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
func ensureAlternativeConnectivity(ctx context.Context, dockerClient *client.Client, containerID, networkToLeave string) error {
	containerJSON, err := dockerClient.ContainerInspect(ctx, containerID)
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
func rollbackOperations(operations []func() error) {
	log.Printf("Rolling back %d operations...", len(operations))

	for i := len(operations) - 1; i >= 0; i-- {
		if err := operations[i](); err != nil {
			log.Printf("Rollback operation %d failed: %v", i, err)
		}
	}
}

// getNetworkName retrieves the human-readable name for a network ID
func getNetworkName(ctx context.Context, dockerClient *client.Client, networkID string) string {
	if netResource, err := dockerClient.NetworkInspect(ctx, networkID, network.InspectOptions{}); err == nil {
		return netResource.Name
	}
	return "unknown"
}

// logPlannedOperations displays what network operations would be performed in dry-run mode
func logPlannedOperations(ctx context.Context, dockerClient *client.Client, toJoin, toLeave []string) {
	if len(toJoin) > 0 {
		log.Println("Would JOIN networks:")
		for _, networkID := range toJoin {
			name := getNetworkName(ctx, dockerClient, networkID)
			log.Printf("  - %s (%s)", name, networkID[:12])
		}
	}

	if len(toLeave) > 0 {
		log.Println("Would LEAVE networks:")
		for _, networkID := range toLeave {
			name := getNetworkName(ctx, dockerClient, networkID)
			log.Printf("  - %s (%s)", name, networkID[:12])
		}
	}
}

// getContainerID retrieves the full container ID from a container name
func getContainerID(ctx context.Context, dockerClient *client.Client, containerName string) (string, error) {
	containerJSON, err := dockerClient.ContainerInspect(ctx, containerName)
	if err != nil {
		return "", fmt.Errorf("failed to inspect container %s: %w", containerName, err)
	}
	return containerJSON.ID, nil
}

// getDefaultBridgeNetworkID finds the ID of the default Docker bridge network
func getDefaultBridgeNetworkID(ctx context.Context, dockerClient *client.Client) (string, error) {
	networks, err := dockerClient.NetworkList(ctx, network.ListOptions{})
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

// getJoinedNetworks returns the networks that the container is currently connected to
func getJoinedNetworks(ctx context.Context, dockerClient *client.Client, containerID string) (map[string]bool, error) {
	networks := make(map[string]bool)

	containerJSON, err := dockerClient.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container %s: %w", containerID, err)
	}

	for _, net := range containerJSON.NetworkSettings.Networks {
		networks[net.NetworkID] = true
	}

	return networks, nil
}

// getActiveBridgeNetworks returns bridge networks that should be joined based on activity criteria
func getActiveBridgeNetworks(ctx context.Context, dockerClient *client.Client, containerID string) (map[string]bool, error) {
	networks := make(map[string]bool)

	allNetworks, err := dockerClient.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list networks: %w", err)
	}

	for _, netOverview := range allNetworks {
		if netOverview.Driver != bridgeDriverName {
			continue
		}

		net, err := dockerClient.NetworkInspect(ctx, netOverview.ID, network.InspectOptions{})
		if err != nil {
			log.Printf("Warning: failed to get info for network %s: %v", netOverview.ID, err)
			continue
		}

		_, containsSelf := net.Containers[containerID]
		isDefaultBridge := net.Options[defaultBridgeOption] == "true" || net.Name == defaultBridgeName
		hasMultipleContainers := len(net.Containers) > 1
		hasOtherContainers := len(net.Containers) == 1 && !containsSelf

		if isDefaultBridge || hasMultipleContainers || hasOtherContainers {
			networks[net.ID] = true
			log.Printf("Including bridge network %s (%s) - Default: %t, MultipleContainers: %t, OtherContainers: %t",
				net.Name, net.ID[:12], isDefaultBridge, hasMultipleContainers, hasOtherContainers)
		}
	}

	return networks, nil
}

// getNetworksToJoin returns networks that the container should join
func getNetworksToJoin(currentNetworks, bridgeNetworks map[string]bool) []string {
	var networkIDs []string
	for networkID := range bridgeNetworks {
		if !currentNetworks[networkID] {
			networkIDs = append(networkIDs, networkID)
		}
	}
	return networkIDs
}

// getNetworksToLeave returns networks that the container should leave, protecting the default bridge
func getNetworksToLeave(currentNetworks, bridgeNetworks map[string]bool, defaultBridgeID string) []string {
	var networkIDs []string

	for networkID := range currentNetworks {
		if networkID == defaultBridgeID {
			log.Printf("Protecting default bridge network %s from disconnection", networkID[:12])
			continue
		}

		if !bridgeNetworks[networkID] {
			networkIDs = append(networkIDs, networkID)
		}
	}
	return networkIDs
}

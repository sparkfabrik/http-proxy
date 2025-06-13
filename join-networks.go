package main

import (
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	docker "github.com/fsouza/go-dockerclient"
)

const (
	defaultDockerEndpoint = "unix:///tmp/docker.sock"
	bridgeDriverName      = "bridge"
	defaultBridgeOption   = "com.docker.network.bridge.default_bridge"
	defaultBridgeName     = "bridge"
	maxRetries            = 3
	retryDelay            = 2 * time.Second
	stabilizationDelay    = 1 * time.Second
)

func main() {
	containerName := flag.String("container-name", "", "the name of this docker container")
	dryRun := flag.Bool("dry-run", false, "show what would be done without making changes")
	flag.Parse()

	if err := run(*containerName, *dryRun); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func run(containerName string, dryRun bool) error {
	if strings.TrimSpace(containerName) == "" {
		return fmt.Errorf("container-name is required")
	}

	client, err := docker.NewClient(defaultDockerEndpoint)
	if err != nil {
		return fmt.Errorf("failed to create docker client: %w", err)
	}

	containerID, err := getContainerID(client, containerName)
	if err != nil {
		return fmt.Errorf("failed to get container ID: %w", err)
	}

	// Pre-operation state capture
	preState, err := captureContainerNetworkState(client, containerID)
	if err != nil {
		return fmt.Errorf("failed to capture pre-operation state: %w", err)
	}
	log.Printf("Pre-operation state: %s", preState.summary())

	currentNetworks, err := getJoinedNetworks(client, containerID)
	if err != nil {
		return fmt.Errorf("failed to get current networks: %w", err)
	}

	bridgeNetworks, err := getActiveBridgeNetworks(client, containerID)
	if err != nil {
		return fmt.Errorf("failed to get bridge networks: %w", err)
	}

	defaultBridgeID, err := getDefaultBridgeNetworkID(client)
	if err != nil {
		log.Printf("Warning: could not identify default bridge network: %v", err)
	}

	toJoin := getNetworksToJoin(currentNetworks, bridgeNetworks)
	toLeave := getNetworksToLeave(currentNetworks, bridgeNetworks, defaultBridgeID)

	log.Printf("Plan: Currently in %d networks, found %d bridge networks, %d to join, %d to leave",
		len(currentNetworks), len(bridgeNetworks), len(toJoin), len(toLeave))

	if dryRun {
		log.Println("DRY RUN MODE - No changes will be made")
		logPlannedOperations(client, toJoin, toLeave)
		return nil
	}

	// Critical: perform operations atomically with rollback capability
	if err := performNetworkOperationsWithRollback(client, containerName, containerID, toJoin, toLeave, preState); err != nil {
		return fmt.Errorf("network operations failed: %w", err)
	}

	log.Println("Network operations completed successfully")
	return nil
}

type NetworkState struct {
	Networks     map[string]NetworkInfo
	PortBindings map[docker.Port][]docker.PortBinding
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

func captureContainerNetworkState(client *docker.Client, containerID string) (*NetworkState, error) {
	container, err := client.InspectContainerWithOptions(docker.InspectContainerOptions{ID: containerID})
	if err != nil {
		return nil, err
	}

	state := &NetworkState{
		Networks:     make(map[string]NetworkInfo),
		PortBindings: container.NetworkSettings.Ports,
		HasExternal:  false,
	}

	for networkName, network := range container.NetworkSettings.Networks {
		state.Networks[network.NetworkID] = NetworkInfo{
			ID:      network.NetworkID,
			Name:    networkName,
			Gateway: network.Gateway,
			IP:      network.IPAddress,
		}
		if network.Gateway != "" {
			state.HasExternal = true
		}
	}

	return state, nil
}

func performNetworkOperationsWithRollback(client *docker.Client, containerName, containerID string,
	toJoin, toLeave []string, originalState *NetworkState) error {

	var operationsPerformed []func() error

	// Cleanup function for rollback
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC during network operations, attempting rollback: %v", r)
			rollbackOperations(operationsPerformed)
		}
	}()

	// Step 1: Join new networks first (safer order)
	for _, networkID := range toJoin {
		if err := safeJoinNetwork(client, containerName, networkID); err != nil {
			log.Printf("Failed to join network %s, rolling back...", networkID[:12])
			rollbackOperations(operationsPerformed)
			return err
		}

		// Add rollback operation
		operationsPerformed = append(operationsPerformed, func() error {
			return safeLeaveNetwork(client, containerName, networkID)
		})

		// Wait for network state to stabilize
		time.Sleep(stabilizationDelay)

		// Verify connectivity after each join
		if err := quickConnectivityCheck(client, containerID); err != nil {
			log.Printf("Connectivity lost after joining %s, rolling back...", networkID[:12])
			rollbackOperations(operationsPerformed)
			return fmt.Errorf("connectivity lost after joining network: %w", err)
		}
	}

	// Step 2: Leave old networks (with connectivity preservation)
	for _, networkID := range toLeave {
		// Before leaving, ensure we still have external connectivity through other networks
		if err := ensureAlternativeConnectivity(client, containerID, networkID); err != nil {
			log.Printf("Cannot leave network %s: would lose connectivity: %v", networkID[:12], err)
			continue // Skip this network to preserve connectivity
		}

		if err := safeLeaveNetwork(client, containerName, networkID); err != nil {
			log.Printf("Warning: failed to leave network %s: %v", networkID[:12], err)
			// Don't rollback for leave failures, just continue
			continue
		}

		// Wait for network state to stabilize
		time.Sleep(stabilizationDelay)

		// Verify connectivity after each leave
		if err := quickConnectivityCheck(client, containerID); err != nil {
			log.Printf("Connectivity lost after leaving %s, attempting to rejoin...", networkID[:12])
			// Try to rejoin the network we just left
			if rejoinErr := safeJoinNetwork(client, containerName, networkID); rejoinErr != nil {
				log.Printf("Failed to rejoin network %s: %v", networkID[:12], rejoinErr)
			}
			return fmt.Errorf("connectivity lost after leaving network: %w", err)
		}
	}

	// Final verification
	finalState, err := captureContainerNetworkState(client, containerID)
	if err != nil {
		return fmt.Errorf("failed to capture final state: %w", err)
	}

	log.Printf("Final state: %s", finalState.summary())

	// Ensure we didn't lose critical connectivity
	if !finalState.HasExternal && originalState.HasExternal {
		return fmt.Errorf("lost external connectivity during operations")
	}

	return nil
}

func safeJoinNetwork(client *docker.Client, containerName, networkID string) error {
	netName := getNetworkName(client, networkID)
	log.Printf("Joining network %s (%s)", netName, networkID[:12])

	return retryOperation(func() error {
		return client.ConnectNetwork(networkID, docker.NetworkConnectionOptions{
			Container: containerName,
		})
	}, fmt.Sprintf("join network %s", networkID[:12]))
}

func safeLeaveNetwork(client *docker.Client, containerName, networkID string) error {
	netName := getNetworkName(client, networkID)
	log.Printf("Leaving network %s (%s)", netName, networkID[:12])

	return retryOperation(func() error {
		return client.DisconnectNetwork(networkID, docker.NetworkConnectionOptions{
			Container: containerName,
			Force:     true,
		})
	}, fmt.Sprintf("leave network %s", networkID[:12]))
}

func retryOperation(operation func() error, description string) error {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := operation(); err != nil {
			lastErr = err
			if attempt < maxRetries {
				log.Printf("Attempt %d/%d failed for %s: %v, retrying in %v...",
					attempt, maxRetries, description, err, retryDelay)
				time.Sleep(retryDelay)
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

func quickConnectivityCheck(client *docker.Client, containerID string) error {
	container, err := client.InspectContainerWithOptions(docker.InspectContainerOptions{ID: containerID})
	if err != nil {
		return fmt.Errorf("failed to inspect container: %w", err)
	}

	networkCount := len(container.NetworkSettings.Networks)
	if networkCount == 0 {
		return fmt.Errorf("container has no network connections")
	}

	// Check for at least one network with external connectivity
	for _, network := range container.NetworkSettings.Networks {
		if network.Gateway != "" && network.IPAddress != "" {
			return nil // Found external connectivity
		}
	}

	return fmt.Errorf("no external connectivity found")
}

func ensureAlternativeConnectivity(client *docker.Client, containerID, networkToLeave string) error {
	container, err := client.InspectContainerWithOptions(docker.InspectContainerOptions{ID: containerID})
	if err != nil {
		return err
	}

	// Count networks with external connectivity, excluding the one we want to leave
	externalConnections := 0
	for _, network := range container.NetworkSettings.Networks {
		if network.NetworkID != networkToLeave && network.Gateway != "" {
			externalConnections++
		}
	}

	if externalConnections == 0 {
		return fmt.Errorf("leaving this network would remove last external connection")
	}

	return nil
}

func rollbackOperations(operations []func() error) {
	log.Printf("Rolling back %d operations...", len(operations))

	// Execute rollback operations in reverse order
	for i := len(operations) - 1; i >= 0; i-- {
		if err := operations[i](); err != nil {
			log.Printf("Rollback operation %d failed: %v", i, err)
		}
	}
}

func getNetworkName(client *docker.Client, networkID string) string {
	if net, err := client.NetworkInfo(networkID); err == nil {
		return net.Name
	}
	return "unknown"
}

func logPlannedOperations(client *docker.Client, toJoin, toLeave []string) {
	if len(toJoin) > 0 {
		log.Println("Would JOIN networks:")
		for _, networkID := range toJoin {
			name := getNetworkName(client, networkID)
			log.Printf("  - %s (%s)", name, networkID[:12])
		}
	}

	if len(toLeave) > 0 {
		log.Println("Would LEAVE networks:")
		for _, networkID := range toLeave {
			name := getNetworkName(client, networkID)
			log.Printf("  - %s (%s)", name, networkID[:12])
		}
	}
}

func getContainerID(client *docker.Client, containerName string) (string, error) {
	container, err := client.InspectContainerWithOptions(docker.InspectContainerOptions{ID: containerName})
	if err != nil {
		return "", fmt.Errorf("failed to inspect container %s: %w", containerName, err)
	}
	return container.ID, nil
}

func getDefaultBridgeNetworkID(client *docker.Client) (string, error) {
	networks, err := client.ListNetworks()
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

// getJoinedNetworks returns a map of network IDs that the container is currently joined to
func getJoinedNetworks(client *docker.Client, containerID string) (map[string]bool, error) {
	networks := make(map[string]bool)

	container, err := client.InspectContainerWithOptions(docker.InspectContainerOptions{ID: containerID})
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container %s: %w", containerID, err)
	}

	for _, net := range container.NetworkSettings.Networks {
		networks[net.NetworkID] = true
	}

	return networks, nil
}

// getActiveBridgeNetworks returns bridge networks that should be joined based on the criteria:
// - Default bridge networks
// - Networks with more than one container
// - Networks with one container that is not this container
func getActiveBridgeNetworks(client *docker.Client, containerID string) (map[string]bool, error) {
	networks := make(map[string]bool)

	allNetworks, err := client.ListNetworks()
	if err != nil {
		return nil, fmt.Errorf("failed to list networks: %w", err)
	}

	for _, netOverview := range allNetworks {
		if netOverview.Driver != bridgeDriverName {
			continue
		}

		net, err := client.NetworkInfo(netOverview.ID)
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

// getNetworksToJoin returns network IDs that should be joined (in bridge networks but not in current networks)
func getNetworksToJoin(currentNetworks, bridgeNetworks map[string]bool) []string {
	var networkIDs []string
	for networkID := range bridgeNetworks {
		if !currentNetworks[networkID] {
			networkIDs = append(networkIDs, networkID)
		}
	}
	return networkIDs
}

// getNetworksToLeave returns network IDs that should be left (in current networks but not in bridge networks)
// Protects the default bridge network from being disconnected
func getNetworksToLeave(currentNetworks, bridgeNetworks map[string]bool, defaultBridgeID string) []string {
	var networkIDs []string

	for networkID := range currentNetworks {
		// Never leave default bridge network - critical for host connectivity
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

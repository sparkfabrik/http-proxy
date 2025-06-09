package main

import (
	"flag"
	"fmt"
	"log"
	"strings"

	docker "github.com/fsouza/go-dockerclient"
)

const (
	defaultDockerEndpoint = "unix:///tmp/docker.sock"
	bridgeDriverName      = "bridge"
	defaultBridgeOption   = "com.docker.network.bridge.default_bridge"
)

func main() {
	containerName := flag.String("container-name", "", "the name of this docker container")
	flag.Parse()

	if err := run(*containerName); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func run(containerName string) error {
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

	currentNetworks, err := getJoinedNetworks(client, containerID)
	if err != nil {
		return fmt.Errorf("failed to get current networks: %w", err)
	}

	bridgeNetworks, err := getActiveBridgeNetworks(client, containerID)
	if err != nil {
		return fmt.Errorf("failed to get bridge networks: %w", err)
	}

	toJoin := getNetworksToJoin(currentNetworks, bridgeNetworks)
	toLeave := getNetworksToLeave(currentNetworks, bridgeNetworks)

	log.Printf("Currently in %d networks, found %d bridge networks, %d to join, %d to leave",
		len(currentNetworks), len(bridgeNetworks), len(toJoin), len(toLeave))

	if err := leaveNetworks(client, containerName, toLeave); err != nil {
		return fmt.Errorf("failed to leave networks: %w", err)
	}

	if err := joinNetworks(client, containerName, toJoin); err != nil {
		return fmt.Errorf("failed to join networks: %w", err)
	}

	log.Println("Network operations completed successfully")
	return nil
}

func getContainerID(client *docker.Client, containerName string) (string, error) {
	container, err := client.InspectContainerWithOptions(docker.InspectContainerOptions{ID: containerName})
	if err != nil {
		return "", fmt.Errorf("failed to inspect container %s: %w", containerName, err)
	}
	return container.ID, nil
}

func leaveNetworks(client *docker.Client, containerName string, networkIDs []string) error {
	for _, networkID := range networkIDs {
		log.Printf("Leaving network %s", networkID)
		if err := client.DisconnectNetwork(networkID, docker.NetworkConnectionOptions{
			Container: containerName,
		}); err != nil {
			return fmt.Errorf("failed to disconnect from network %s: %w", networkID, err)
		}
	}
	return nil
}

func joinNetworks(client *docker.Client, containerName string, networkIDs []string) error {
	for _, networkID := range networkIDs {
		log.Printf("Joining network %s", networkID)
		if err := client.ConnectNetwork(networkID, docker.NetworkConnectionOptions{
			Container: containerName,
		}); err != nil {
			return fmt.Errorf("failed to connect to network %s: %w", networkID, err)
		}
	}
	return nil
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
		isDefaultBridge := net.Options[defaultBridgeOption] == "true"
		hasMultipleContainers := len(net.Containers) > 1
		hasOtherContainers := len(net.Containers) == 1 && !containsSelf

		if isDefaultBridge || hasMultipleContainers || hasOtherContainers {
			networks[net.ID] = true
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
func getNetworksToLeave(currentNetworks, bridgeNetworks map[string]bool) []string {
	var networkIDs []string
	for networkID := range currentNetworks {
		if !bridgeNetworks[networkID] {
			networkIDs = append(networkIDs, networkID)
		}
	}
	return networkIDs
}

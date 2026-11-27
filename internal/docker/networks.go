package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
)

// NetworkAddr is a container's address on one Docker network.
type NetworkAddr struct {
	Driver string // "bridge", "macvlan", "ipvlan", "host", ...
	IPv4   string
	IPv6   string // global address only
}

// Routable reports whether the network gives the container an address reachable from the LAN
// without port publishing: macvlan and ipvlan attach it directly to a physical segment.
func (n NetworkAddr) Routable() bool { return n.Driver == "macvlan" || n.Driver == "ipvlan" }

// ContainerNetworks returns the container's addresses keyed by network name.
func ContainerNetworks(ctx context.Context, c *dockerclient.Client, containerID string) (map[string]NetworkAddr, error) {
	info, err := c.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("inspect container: %w", err)
	}
	out := map[string]NetworkAddr{}
	if info.NetworkSettings == nil {
		return out, nil
	}
	for name, ep := range info.NetworkSettings.Networks {
		if ep == nil {
			continue
		}
		addr := NetworkAddr{IPv4: ep.IPAddress, IPv6: ep.GlobalIPv6Address}
		nw, err := c.NetworkInspect(ctx, ep.NetworkID, network.InspectOptions{})
		if err != nil {
			return nil, fmt.Errorf("inspect network %s: %w", name, err)
		}
		addr.Driver = nw.Driver
		out[name] = addr
	}
	return out, nil
}

// Package network manages HCN (Host Compute Network) resources for BARGE containers.
// Every container gets an endpoint on the barge-nat NAT network so it can reach
// the internet and receive inbound connections via host-port mappings.
package network

import (
	"encoding/json"
	"fmt"
	stdnet "net"
	"strconv"
	"strings"

	"github.com/Microsoft/hcsshim/hcn"
)

const BargeNetworkName = "barge-nat"

// PortMapping is a host→container port forwarding rule.
type PortMapping struct {
	HostPort      uint16
	ContainerPort uint16
	Proto         string // "tcp" or "udp"
}

// ParsePortMapping parses "hostPort:containerPort[/proto]".
func ParsePortMapping(spec string) (PortMapping, error) {
	proto := "tcp"
	if idx := strings.LastIndex(spec, "/"); idx >= 0 {
		proto = strings.ToLower(spec[idx+1:])
		spec = spec[:idx]
	}
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 {
		return PortMapping{}, fmt.Errorf("invalid port mapping %q: expected host:container[/proto]", spec)
	}
	h, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil {
		return PortMapping{}, fmt.Errorf("invalid host port %q: %w", parts[0], err)
	}
	c, err := strconv.ParseUint(parts[1], 10, 16)
	if err != nil {
		return PortMapping{}, fmt.Errorf("invalid container port %q: %w", parts[1], err)
	}
	return PortMapping{HostPort: uint16(h), ContainerPort: uint16(c), Proto: proto}, nil
}

// EnsureNATNetwork creates the barge-nat HCN NAT network if it does not exist.
// Returns the network ID.
func EnsureNATNetwork() (string, error) {
	existing, err := hcn.GetNetworkByName(BargeNetworkName)
	if err == nil {
		return existing.Id, nil
	}

	network := &hcn.HostComputeNetwork{
		Type:          hcn.NAT,
		Name:          BargeNetworkName,
		SchemaVersion: hcn.SchemaVersion{Major: 2, Minor: 0},
	}

	created, err := network.Create()
	if err != nil {
		return "", fmt.Errorf(
			"failed to create barge-nat network: %w\n\n"+
				"  Ensure the Windows Containers feature is enabled:\n"+
				"    Enable-WindowsOptionalFeature -Online -FeatureName Containers",
			err,
		)
	}
	return created.Id, nil
}

// CreateEndpoint creates an HCN endpoint on the given network for a container.
// Port mappings are applied as NAT policies so inbound traffic reaches the container.
// Returns the endpoint ID.
func CreateEndpoint(networkID, containerID string, ports []PortMapping) (string, error) {
	policies := make([]hcn.EndpointPolicy, 0, len(ports))
	for _, pm := range ports {
		raw, err := portMappingJSON(pm)
		if err != nil {
			return "", err
		}
		policies = append(policies, hcn.EndpointPolicy{
			Type:     hcn.PortMapping,
			Settings: raw,
		})
	}

	net, err := hcn.GetNetworkByID(networkID)
	if err != nil {
		return "", fmt.Errorf("cannot load barge-nat network: %w", err)
	}

	ep, err := net.CreateEndpoint(&hcn.HostComputeEndpoint{
		Name:               containerID,
		HostComputeNetwork: networkID,
		Policies:           policies,
		SchemaVersion:      hcn.SchemaVersion{Major: 2, Minor: 0},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create network endpoint for %q: %w", containerID, err)
	}
	return ep.Id, nil
}

// DeleteEndpoint removes an HCN endpoint. Errors are silently swallowed — this
// is always called as best-effort cleanup during stop/rm.
func DeleteEndpoint(endpointID string) {
	ep, err := hcn.GetEndpointByID(endpointID)
	if err != nil {
		return
	}
	_ = ep.Delete()
}

// GatewayIP returns the host IP address on the barge-nat network.
// Containers use this address to reach the host (e.g. during builds).
func GatewayIP() (string, error) {
	n, err := hcn.GetNetworkByName(BargeNetworkName)
	if err != nil {
		return "", fmt.Errorf("barge-nat network not found: %w", err)
	}
	// Prefer the explicit default-route next-hop.
	for _, ipam := range n.Ipams {
		for _, subnet := range ipam.Subnets {
			for _, route := range subnet.Routes {
				if route.NextHop != "" && route.DestinationPrefix == "0.0.0.0/0" {
					return route.NextHop, nil
				}
			}
		}
	}
	// Fallback: derive gateway as first host in the subnet (x.y.z.1).
	for _, ipam := range n.Ipams {
		for _, subnet := range ipam.Subnets {
			if subnet.IpAddressPrefix == "" {
				continue
			}
			_, ipNet, err := stdnet.ParseCIDR(subnet.IpAddressPrefix)
			if err != nil {
				continue
			}
			ip := make(stdnet.IP, len(ipNet.IP))
			copy(ip, ipNet.IP)
			ip[len(ip)-1] = 1
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("cannot determine barge-nat gateway IP from network configuration")
}

func portMappingJSON(pm PortMapping) (json.RawMessage, error) {
	proto := uint32(6) // TCP
	if pm.Proto == "udp" {
		proto = 17
	}
	return json.Marshal(hcn.PortMappingPolicySetting{
		Protocol:     proto,
		InternalPort: pm.ContainerPort,
		ExternalPort: pm.HostPort,
	})
}

package option

import (
	"net/netip"

	"github.com/sagernet/sing/common/json/badoption"
)

type CloudflareEndpointOptions struct {
	DialerOptions
	Server            string             `json:"server,omitempty"`
	ServerPort        uint16             `json:"server_port,omitempty"`
	PrivateKey        string             `json:"private_key,omitempty"`
	PublicKey         string             `json:"public_key,omitempty"`
	Certificate       string             `json:"certificate,omitempty"`
	ID                string             `json:"id,omitempty"`
	Token             string             `json:"token,omitempty"`
	Protocol          string             `json:"protocol,omitempty"` // h3 or h2
	UDPTimeout        badoption.Duration `json:"udp_timeout,omitempty"`
	MTU               uint32             `json:"mtu,omitempty"`
	RemoteIP          netip.Addr         `json:"remote_ip,omitempty"`
	RemoteIPv6        netip.Addr         `json:"remote_ipv6,omitempty"`
	InitialPacketSize uint16             `json:"initial_packet_size,omitempty"`
}

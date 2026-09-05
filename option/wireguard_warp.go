package option

import (
	"net/netip"

	"github.com/sagernet/sing/common/json/badoption"
)

type WireGuardWARPEndpointOptions struct {
	DialerOptions
	System     bool                             `json:"system,omitempty"`
	Name       string                           `json:"name,omitempty"`
	MTU        uint32                           `json:"mtu,omitempty"`
	Address    badoption.Listable[netip.Prefix] `json:"address,omitempty"`
	PrivateKey string                           `json:"private_key,omitempty"`
	ListenPort uint16                           `json:"listen_port,omitempty"`
	UDPTimeout badoption.Duration               `json:"udp_timeout,omitempty"`
	Workers    int                              `json:"workers,omitempty"`

	// Peer Configuration
	Server                      string  `json:"server,omitempty"`
	ServerPort                  uint16  `json:"server_port,omitempty"`
	PeerPublicKey               string  `json:"peer_public_key,omitempty"`
	PreSharedKey                string  `json:"pre_shared_key,omitempty"`
	Reserved                    []uint8 `json:"reserved,omitempty"`
	PersistentKeepaliveInterval uint16  `json:"persistent_keepalive_interval,omitempty"`

	// Separate Provisioning Instructions
	Provision         *WARPProvisionOptions `json:"provision,omitempty"`
	BootstrapResolver *DomainResolveOptions `json:"bootstrap_resolver,omitempty"`
}

type WARPProvisionOptions struct {
	License   string `json:"license,omitempty"`
	CachePath string `json:"cache_path,omitempty"`
	Recreate  bool   `json:"recreate,omitempty"`
}

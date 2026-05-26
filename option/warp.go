package option

import (
	"net/netip"

	"github.com/sagernet/sing/common/json/badoption"
)

type WarpOutboundOptions struct {
	DialerOptions       `json:",inline"`
	Server              string                           `json:"server,omitempty"`
	ServerPort          uint16                           `json:"server_port,omitempty"`
	PrivateKey          string                           `json:"private_key,omitempty"`
	PublicKey           string                           `json:"public_key,omitempty"`
	PreSharedKey        string                           `json:"pre_shared_key,omitempty"`
	Address             badoption.Listable[netip.Prefix] `json:"address,omitempty"`
	Reserved            []uint8                          `json:"reserved,omitempty"`
	MTU                 uint32                           `json:"mtu,omitempty"`
	UDPTimeout          badoption.Duration               `json:"udp_timeout,omitempty"`
	PersistentKeepalive uint16                           `json:"persistent_keepalive,omitempty"`
	Workers             int                              `json:"workers,omitempty"`
	InnerDomainResolver *DomainResolveOptions            `json:"inner_domain_resolver,omitempty"`
	License             string                           `json:"license,omitempty"`
}

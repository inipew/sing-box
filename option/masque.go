package option

import (
	"net/netip"

	"github.com/sagernet/sing/common/json/badoption"
)

type MasqueOutboundOptions struct {
	ServerOptions
	System              bool                             `json:"system,omitempty"`
	GSO                 *bool                            `json:"gso,omitempty"`
	Address             badoption.Listable[netip.Prefix] `json:"address"`
	PrivateKey     string                           `json:"private_key"`
	PublicKey      string                           `json:"public_key"`
	AccessToken    string                           `json:"access_token,omitempty"`
	URI                 string                           `json:"uri,omitempty"`
	OutboundTLSOptionsContainer
	QUICOptions
	MTU                 uint32                           `json:"mtu,omitempty"`
	UDPTimeout     badoption.Duration               `json:"udp_timeout,omitempty"`
	InnerDomainResolver *DomainResolveOptions            `json:"inner_domain_resolver,omitempty"`
	DialerOptions
}

//go:build with_warp

package option

import "github.com/sagernet/sing/common/json/badoption"

// WireGuardWARPEndpointOptions defines options for WARP endpoint.
type WireGuardWARPEndpointOptions struct {
	System     bool                        `json:"system,omitempty"`
	Name       string                      `json:"name,omitempty"`
	ListenPort uint16                      `json:"listen_port,omitempty"`
	UDPTimeout badoption.Duration          `json:"udp_timeout,omitempty"`
	Workers    int                         `json:"workers,omitempty"`
	Profile    *WireGuardCloudflareProfile `json:"profile,omitempty"`
	DialerOptions
}

// WireGuardCloudflareProfile holds Cloudflare WARP profile info.
type WireGuardCloudflareProfile struct {
	ID         string `json:"id,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	AuthToken  string `json:"auth_token,omitempty"`
	Recreate   bool   `json:"recreate,omitempty"`
	Detour     string `json:"detour,omitempty"`
}

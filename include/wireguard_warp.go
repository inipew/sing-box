//go:build with_wireguard && with_warp

package include

import (
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/protocol/wireguard"
)

func registerWARPEndpoint(registry *endpoint.Registry) {
	wireguard.RegisterWARPEndpoint(registry)
}

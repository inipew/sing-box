//go:build with_wireguard && !with_warp

package include

import "github.com/sagernet/sing-box/adapter/endpoint"

func registerWARPEndpoint(registry *endpoint.Registry) {}

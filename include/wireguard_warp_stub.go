//go:build with_wireguard && !with_warp

package include

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/endpoint"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

func registerWARPEndpoint(registry *endpoint.Registry) {
	endpoint.Register[option.WireGuardWARPEndpointOptions](registry, C.TypeWarp, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.WireGuardWARPEndpointOptions) (adapter.Endpoint, error) {
		return nil, E.New(`WARP is not included in this build, rebuild with -tags with_warp`)
	})
}

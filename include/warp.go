//go:build with_warp

package include

import (
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/protocol/warp"
)

func registerWarpOutbound(registry *outbound.Registry) {
	warp.RegisterOutbound(registry)
}

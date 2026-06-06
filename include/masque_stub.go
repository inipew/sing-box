//go:build !with_masque

package include

import (
	"context"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

func registerMasqueOutbound(registry *outbound.Registry) {
	outbound.Register[option.MasqueOutboundOptions](registry, C.TypeMasque, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.MasqueOutboundOptions) (adapter.Outbound, error) {
		return nil, E.New(`MASQUE is not included in this build, rebuild with -tags with_masque`)
	})
}

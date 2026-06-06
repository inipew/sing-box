//go:build with_masque

package include

import (
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/protocol/masque"
)

func registerMasqueOutbound(registry *outbound.Registry) {
	masque.RegisterOutbound(registry)
}

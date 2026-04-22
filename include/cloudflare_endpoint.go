//go:build with_cloudflare

package include

import (
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/protocol/cloudflare"
)

func registerCloudflareEndpoint(registry *endpoint.Registry) {
	cloudflare.RegisterEndpoint(registry)
}

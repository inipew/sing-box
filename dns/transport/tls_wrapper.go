package transport

import (
	"context"

	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/dns"
	M "github.com/sagernet/sing/common/metadata"
)

type tlsDialerWrapper struct {
	tls.Dialer
}

func (w tlsDialerWrapper) DialTLSContext(ctx context.Context, destination M.Socksaddr) (dns.TLSConn, error) {
	return w.Dialer.DialTLSContext(ctx, destination)
}

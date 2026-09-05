package group

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/interrupt"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/stretchr/testify/require"
)

func TestSelectorInterruptsAttachedConnection(t *testing.T) {
	selected := &selectorTestOutbound{tag: "selected"}
	replacement := &selectorTestOutbound{tag: "replacement"}
	selector := &Selector{
		outbounds: map[string]adapter.Outbound{
			selected.tag:    selected,
			replacement.tag: replacement,
		},
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: true,
	}
	selector.selected.Store(selected)

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { serverConn.Close() })
	trackedConn := &selectorTrackedConn{Conn: clientConn}
	selector.AttachConnection(trackedConn)

	require.True(t, selector.SelectOutbound(replacement.tag))
	require.True(t, trackedConn.closed.Load())
}

type selectorTrackedConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *selectorTrackedConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

type selectorTestOutbound struct {
	tag string
}

func (o *selectorTestOutbound) Type() string           { return "test" }
func (o *selectorTestOutbound) Tag() string            { return o.tag }
func (o *selectorTestOutbound) Network() []string      { return []string{N.NetworkTCP} }
func (o *selectorTestOutbound) Dependencies() []string { return nil }
func (o *selectorTestOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, nil
}
func (o *selectorTestOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, nil
}
func (o *selectorTestOutbound) NewConnection(context.Context, net.Conn, adapter.InboundContext, N.CloseHandlerFunc) {
}

package route

import (
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/stretchr/testify/require"
)

type noopPacketConn struct{}

func (*noopPacketConn) ReadPacket(*buf.Buffer) (M.Socksaddr, error) {
	return M.Socksaddr{}, net.ErrClosed
}

func (*noopPacketConn) WritePacket(*buf.Buffer, M.Socksaddr) error { return nil }
func (*noopPacketConn) Close() error                               { return nil }
func (*noopPacketConn) LocalAddr() net.Addr                        { return nil }
func (*noopPacketConn) SetDeadline(time.Time) error                { return nil }
func (*noopPacketConn) SetReadDeadline(time.Time) error            { return nil }
func (*noopPacketConn) SetWriteDeadline(time.Time) error           { return nil }

func TestApplySniffDestinationOverrideTracksUDPOriginal(t *testing.T) {
	original := M.ParseSocksaddr("1.1.1.1:443")
	metadata := adapter.InboundContext{
		Network:     N.NetworkUDP,
		Destination: original,
		Domain:      "example.com",
	}

	applySniffDestinationOverride(&metadata)

	require.Equal(t, original, metadata.OriginDestination)
	require.Equal(t, M.ParseSocksaddr("example.com:443"), metadata.Destination)
	require.True(t, metadata.DestOverride)
}

func TestWrapSniffDestinationOverrideUsesNAT(t *testing.T) {
	metadata := adapter.InboundContext{
		OriginDestination: M.ParseSocksaddr("1.1.1.1:443"),
		Destination:       M.ParseSocksaddr("example.com:443"),
		DestOverride:      true,
	}
	var conn N.PacketConn = new(noopPacketConn)

	wrapped := wrapSniffDestinationOverride(conn, metadata)

	require.NotEqual(t, conn, wrapped)
	require.Implements(t, (*bufio.NATPacketConn)(nil), wrapped)
}

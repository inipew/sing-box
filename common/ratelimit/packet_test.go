package ratelimit

import (
	"context"
	"io"
	"testing"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/stretchr/testify/require"
)

type onePacketReader struct {
	N.PacketConn
	reads int
}

func (r *onePacketReader) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	r.reads++
	if r.reads == 1 {
		buffer.Extend(1500)
		return M.ParseSocksaddr("1.1.1.1:53"), nil
	}
	return M.Socksaddr{}, io.EOF
}

func TestLimitedPacketConnDropsUploadAfterWaitTimeout(t *testing.T) {
	limiter := NewUDPLimiter(Config{Upload: 1})
	require.NoError(t, limiter.WaitUpload(context.Background(), limiter.UploadBurst()))
	reader := &onePacketReader{}
	conn := NewLimitedPacketConn(context.Background(), reader, limiter)
	buffer := buf.NewPacket()
	defer buffer.Release()

	_, err := conn.ReadPacket(buffer)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 2, reader.reads)
	require.Zero(t, buffer.Len())
}

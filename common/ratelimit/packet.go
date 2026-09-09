package ratelimit

import (
	"context"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

const udpWaitTimeout = 50 * time.Millisecond

var _ N.PacketConn = (*LimitedPacketConn)(nil)

type LimitedPacketConn struct {
	N.PacketConn
	ctx     context.Context
	limiter *Limiter
}

func NewLimitedPacketConn(ctx context.Context, conn N.PacketConn, limiter *Limiter) N.PacketConn {
	if limiter == nil || (!limiter.HasUpload() && !limiter.HasDownload()) {
		return conn
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &LimitedPacketConn{
		PacketConn: conn,
		ctx:        ctx,
		limiter:    limiter,
	}
}

func (c *LimitedPacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	destination, err := c.PacketConn.ReadPacket(buffer)
	if err != nil {
		return destination, err
	}
	if c.limiter.HasUpload() && buffer.Len() > 0 {
		_ = c.limiter.WaitUploadWithTimeout(c.ctx, buffer.Len(), udpWaitTimeout)
	}
	return destination, nil
}

func (c *LimitedPacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	if !c.limiter.HasDownload() || buffer.Len() == 0 {
		return c.PacketConn.WritePacket(buffer, destination)
	}
	err := c.limiter.WaitDownloadWithTimeout(c.ctx, buffer.Len(), udpWaitTimeout)
	if err != nil {
		// Drop packet on rate limit timeout to prevent unbounded queues
		buffer.Release()
		return nil
	}
	return c.PacketConn.WritePacket(buffer, destination)
}

func (c *LimitedPacketConn) Upstream() any {
	return c.PacketConn
}

func (c *LimitedPacketConn) ReaderReplaceable() bool {
	return !c.limiter.HasUpload()
}

func (c *LimitedPacketConn) WriterReplaceable() bool {
	return !c.limiter.HasDownload()
}

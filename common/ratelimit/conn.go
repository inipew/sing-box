package ratelimit

import (
	"context"
	"net"

	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

var (
	_ net.Conn       = (*LimitedConn)(nil)
	_ N.CachedReader = (*LimitedConn)(nil)
	_ N.WriteCloser  = (*LimitedConn)(nil)
	_ N.ReadCloser   = (*LimitedConn)(nil)
	_ N.ExtendedConn = (*LimitedConn)(nil)
)

type LimitedConn struct {
	N.ExtendedConn
	ctx     context.Context
	limiter *Limiter
}

func NewLimitedConn(ctx context.Context, conn net.Conn, limiter *Limiter) net.Conn {
	if limiter == nil || (!limiter.HasUpload() && !limiter.HasDownload()) {
		return conn
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &LimitedConn{
		ExtendedConn: bufio.NewExtendedConn(conn),
		ctx:          ctx,
		limiter:      limiter,
	}
}

func (c *LimitedConn) Read(b []byte) (int, error) {
	n, err := c.ExtendedConn.Read(b)
	if n > 0 && c.limiter.HasUpload() {
		waitErr := c.limiter.WaitUpload(c.ctx, n)
		if waitErr != nil && err == nil {
			return n, waitErr
		}
	}
	return n, err
}

func (c *LimitedConn) ReadBuffer(buffer *buf.Buffer) error {
	startLen := buffer.Len()
	err := c.ExtendedConn.ReadBuffer(buffer)
	n := buffer.Len() - startLen
	if n > 0 && c.limiter.HasUpload() {
		waitErr := c.limiter.WaitUpload(c.ctx, n)
		if waitErr != nil && err == nil {
			return waitErr
		}
	}
	return err
}

func (c *LimitedConn) Write(b []byte) (int, error) {
	if !c.limiter.HasDownload() || len(b) == 0 {
		return c.ExtendedConn.Write(b)
	}
	burst := c.limiter.DownloadBurst()
	if burst <= 0 {
		burst = minTCPBurst
	}
	var totalWritten int
	for len(b) > 0 {
		chunk := len(b)
		if chunk > burst {
			chunk = burst
		}
		err := c.limiter.WaitDownload(c.ctx, chunk)
		if err != nil {
			return totalWritten, err
		}
		n, err := c.ExtendedConn.Write(b[:chunk])
		totalWritten += n
		if err != nil {
			return totalWritten, err
		}
		b = b[n:]
	}
	return totalWritten, nil
}

func (c *LimitedConn) WriteBuffer(buffer *buf.Buffer) error {
	if !c.limiter.HasDownload() || buffer.IsEmpty() {
		return c.ExtendedConn.WriteBuffer(buffer)
	}
	err := c.limiter.WaitDownload(c.ctx, buffer.Len())
	if err != nil {
		buffer.Release()
		return err
	}
	return c.ExtendedConn.WriteBuffer(buffer)
}

func (c *LimitedConn) Upstream() any {
	return c.ExtendedConn
}

func (c *LimitedConn) ReaderReplaceable() bool {
	return true
}

func (c *LimitedConn) WriterReplaceable() bool {
	return true
}

func (c *LimitedConn) ReadCached() *buf.Buffer {
	if cachedReader, ok := c.ExtendedConn.(N.CachedReader); ok {
		return cachedReader.ReadCached()
	}
	return nil
}

func (c *LimitedConn) CloseWrite() error {
	if writeCloser, ok := c.ExtendedConn.(N.WriteCloser); ok {
		return writeCloser.CloseWrite()
	}
	return nil
}

func (c *LimitedConn) CloseRead() error {
	if readCloser, ok := c.ExtendedConn.(N.ReadCloser); ok {
		return readCloser.CloseRead()
	}
	return nil
}

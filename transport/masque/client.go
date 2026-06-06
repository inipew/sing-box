//go:build with_masque

package masque

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/quic-go/quicvarint"
	E "github.com/sagernet/sing/common/exceptions"
)

// datagramPool reuses []byte buffers for the hot write path, significantly
// reducing GC pressure under heavy (Gigabit) traffic loads.
// The default buffer size matches the default MASQUE MTU (1280) plus overhead.
var datagramPool = sync.Pool{
	New: func() any {
		b := make([]byte, 2048)
		return &b
	},
}

// IPConn represents an established MASQUE Connect-IP tunnel.
type IPConn interface {
	ReadPacket() (b []byte, err error)
	WritePacket(b []byte) (icmp []byte, err error)
	Close() error
}

type ipConn struct {
	str *http3.RequestStream
}

func (c *ipConn) ReadPacket() ([]byte, error) {
	for {
		data, err := c.str.ReceiveDatagram(context.Background())
		if err != nil {
			return nil, err
		}
		contextID, n, err := quicvarint.Parse(data)
		if err != nil || contextID != 0 {
			continue // ignore non-IP or malformed datagrams
		}
		return data[n:], nil
	}
}

func (c *ipConn) WritePacket(b []byte) (icmp []byte, err error) {
	needed := 1 + len(b)

	ptr := datagramPool.Get().(*[]byte)
	var data []byte
	if needed <= cap(*ptr) {
		// Fast path: reuse pooled buffer.
		data = (*ptr)[:needed]
	} else {
		// Slow path: packet exceeds pool capacity (jumbo frame / misconfigured MTU).
		// Allocate a one-off buffer; the pooled slice is returned unchanged.
		data = make([]byte, needed)
	}

	data[0] = 0 // Context ID 0 (1-byte quic varint)
	copy(data[1:], b)
	err = c.str.SendDatagram(data)
	datagramPool.Put(ptr)

	if err != nil {
		var errDTL *quic.DatagramTooLargeError
		if errors.As(err, &errDTL) {
			// Datagram too large — drop silently; the peer's path MTU will
			// handle the retransmission at a smaller size.
			return nil, nil
		}
		return nil, err
	}
	return nil, nil
}

func (c *ipConn) Close() error {
	c.str.CancelRead(quic.StreamErrorCode(http3.ErrCodeNoError))
	return c.str.Close()
}

// DialConnectIP negotiates a MASQUE Connect-IP tunnel over an existing QUIC
// connection. It sends the Extended CONNECT request, verifies server settings,
// sends a full-route ROUTE_ADVERTISEMENT capsule, and returns a ready-to-use
// IPConn for IP datagram exchange.
func DialConnectIP(ctx context.Context, quicConn *quic.Conn, connectURI string, accessToken string) (*http3.Transport, IPConn, error) {
	tr := &http3.Transport{
		EnableDatagrams: true,
	}

	hconn := tr.NewClientConn(quicConn)

	u, err := url.Parse(connectURI)
	if err != nil {
		tr.Close()
		return nil, nil, E.Cause(err, "parse URI")
	}

	select {
	case <-ctx.Done():
		tr.Close()
		return nil, nil, context.Cause(ctx)
	case <-hconn.Context().Done():
		tr.Close()
		return nil, nil, context.Cause(hconn.Context())
	case <-hconn.ReceivedSettings():
	}

	settings := hconn.Settings()
	if !settings.EnableExtendedConnect {
		tr.Close()
		return nil, nil, E.New("server didn't enable Extended CONNECT")
	}
	if !settings.EnableDatagrams {
		tr.Close()
		return nil, nil, E.New("server didn't enable datagrams")
	}

	headers := http.Header{
		http3.CapsuleProtocolHeader: []string{"?1"},
		"User-Agent":                []string{""},
	}
	if accessToken != "" {
		headers.Set("Authorization", "Bearer "+accessToken)
	}

	rstr, err := hconn.OpenRequestStream(ctx)
	if err != nil {
		tr.Close()
		return nil, nil, E.Cause(err, "open request stream")
	}

	if err := rstr.SendRequestHeader(&http.Request{
		Method: http.MethodConnect,
		Proto:  "cf-connect-ip",
		Host:   u.Host,
		Header: headers,
		URL:    u,
	}); err != nil {
		tr.Close()
		return nil, nil, E.Cause(err, "send request")
	}

	rsp, err := rstr.ReadResponse()
	if err != nil {
		tr.Close()
		return nil, nil, E.Cause(err, "read response")
	}
	if rsp.StatusCode < 200 || rsp.StatusCode > 299 {
		tr.Close()
		return nil, nil, E.New("server responded with status ", rsp.StatusCode)
	}

	ipConnInstance := &ipConn{
		str: rstr,
	}

	// Send ROUTE_ADVERTISEMENT capsule advertising default routes for IPv4 and IPv6.
	val := make([]byte, 0, 44)
	// IPv4 default route: 0.0.0.0/0, protocol 0 (all)
	val = append(val, 4)
	val = append(val, 0, 0, 0, 0)
	val = append(val, 255, 255, 255, 255)
	val = append(val, 0)
	// IPv6 default route: ::/0, protocol 0 (all)
	val = append(val, 6)
	val = append(val, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	val = append(val, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255)
	val = append(val, 0)

	capsule := make([]byte, 0, 64)
	capsule = quicvarint.Append(capsule, 3) // ROUTE_ADVERTISEMENT capsule type
	capsule = quicvarint.Append(capsule, uint64(len(val)))
	capsule = append(capsule, val...)
	_, _ = rstr.Write(capsule)

	// Drain incoming capsules asynchronously to keep the request stream open.
	go func() {
		defer rstr.Close()
		r := quicvarint.NewReader(rstr)
		for {
			_, body, err := http3.ParseCapsule(r)
			if err != nil {
				return
			}
			if _, err := io.Copy(io.Discard, body); err != nil {
				return
			}
		}
	}()

	return tr, ipConnInstance, nil
}

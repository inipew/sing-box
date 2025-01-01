package sniff

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
)

type (
	StreamSniffer = func(ctx context.Context, metadata *adapter.InboundContext, reader io.Reader) error
	PacketSniffer = func(ctx context.Context, metadata *adapter.InboundContext, packet []byte) error
)

func Skip(metadata *adapter.InboundContext) bool {
	// skip server first protocols
	switch metadata.Destination.Port {
	case 25, 465, 587:
		// SMTP
		return true
	case 143, 993:
		// IMAP
		return true
	case 110, 995:
		// POP3
		return true
	}
	return false
}

func PeekStream(ctx context.Context, metadata *adapter.InboundContext, conn net.Conn, buffer *buf.Buffer, timeout time.Duration, sniffers ...StreamSniffer) error {
	if timeout == 0 {
		timeout = C.ReadPayloadTimeout
	}
	deadline := time.Now().Add(timeout)
	var errors []error

	for i := 0; ; i++ {
		// Set read deadline
		if err := conn.SetReadDeadline(deadline); err != nil {
			return E.Cause(err, "set read deadline")
		}

		// Read from connection
		_, err := buffer.ReadOnceFrom(conn)
		conn.SetReadDeadline(time.Time{}) // Reset deadline

		if err != nil {
			if i > 0 {
				break
			}
			return E.Cause(err, "read payload")
		}

		// Konkurensi dengan WaitGroup untuk kontrol yang lebih baik
		var wg sync.WaitGroup
		errorsChan := make(chan error, len(sniffers))
		fastClose, cancel := context.WithCancel(ctx)

		for _, sniffer := range sniffers {
			wg.Add(1)
			go func(sn StreamSniffer) {
				defer wg.Done()
				errorsChan <- sn(fastClose, metadata, bytes.NewReader(buffer.Bytes()))
			}(sniffer)
		}

		// Tunggu goroutine selesai di background
		go func() {
			wg.Wait()
			close(errorsChan)
		}()

		// Proses errors
		for err := range errorsChan {
			if err == nil {
				cancel()
				return nil
			}
			errors = append(errors, err)
		}

		cancel()
	}

	return E.Errors(errors...)
}

func PeekPacket(ctx context.Context, metadata *adapter.InboundContext, packet []byte, sniffers ...PacketSniffer) error {
	var (
		wg         sync.WaitGroup
		errors     []error
		errorsChan chan error
	)

	errorsChan = make(chan error, len(sniffers))
	fastClose, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, sniffer := range sniffers {
		wg.Add(1)
		go func(sn PacketSniffer) {
			defer wg.Done()
			errorsChan <- sn(fastClose, metadata, packet)
		}(sniffer)
	}

	// Tunggu goroutine selesai di background
	go func() {
		wg.Wait()
		close(errorsChan)
	}()

	// Proses errors
	for err := range errorsChan {
		if err == nil {
			cancel()
			return nil
		}
		errors = append(errors, err)
	}

	return E.Errors(errors...)
}

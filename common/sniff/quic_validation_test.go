package sniff_test

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/sniff"
	"github.com/stretchr/testify/require"
)

func paddedQUICHeader(firstByte byte, sourceConnectionIDLength byte, packetLength []byte) []byte {
	packet := make([]byte, 1200)
	packet[0] = firstByte
	binary.BigEndian.PutUint32(packet[1:5], 1)
	packet[5] = 1
	packet[6] = 1
	packet[7] = sourceConnectionIDLength
	index := 8 + int(sourceConnectionIDLength)
	packet[index] = 0
	copy(packet[index+1:], packetLength)
	return packet
}

func TestQUICClientHelloRejectsShortHeader(t *testing.T) {
	packet := paddedQUICHeader(0x40, 0, []byte{0})
	err := sniff.QUICClientHello(context.Background(), new(adapter.InboundContext), packet)
	require.ErrorContains(t, err, "not a long header")
}

func TestQUICClientHelloRejectsUndersizedInitial(t *testing.T) {
	packet := []byte{0xc0, 0, 0, 0, 1}
	err := sniff.QUICClientHello(context.Background(), new(adapter.InboundContext), packet)
	require.ErrorContains(t, err, "packet too small")
}

func TestQUICClientHelloRejectsOversizedSourceConnectionID(t *testing.T) {
	packet := paddedQUICHeader(0xc0, 21, []byte{0})
	err := sniff.QUICClientHello(context.Background(), new(adapter.InboundContext), packet)
	require.ErrorContains(t, err, "source connection id")
}

func TestQUICClientHelloRejectsOverflowingPacketLengthWithoutPanic(t *testing.T) {
	packet := paddedQUICHeader(0xc0, 0, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	var err error
	require.NotPanics(t, func() {
		err = sniff.QUICClientHello(context.Background(), new(adapter.InboundContext), packet)
	})
	require.ErrorContains(t, err, "packet length")
}

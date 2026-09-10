//go:build with_wireguard && with_warp

package wireguard

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/stretchr/testify/require"
)

func TestWARPConfigInvariant(t *testing.T) {
	// 1. Valid Key
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)
	require.NotEmpty(t, privKey.String())

	parsedKey, err := ParseKey(privKey.String())
	require.NoError(t, err)
	require.Equal(t, privKey, parsedKey)

	// Invalid keys
	_, err = ParseKey("")
	require.Error(t, err)

	_, err = ParseKey("not-base64!")
	require.Error(t, err)

	_, err = ParseKey("AQID") // too short (3 bytes)
	require.Error(t, err)

	// 2. Reserved validation
	res3, err := ParseReserved([]uint8{1, 2, 3})
	require.NoError(t, err)
	require.Equal(t, Reserved{1, 2, 3}, res3)

	resEmpty, err := ParseReserved(nil)
	require.NoError(t, err)
	require.Equal(t, Reserved{0, 0, 0}, resEmpty)

	_, err = ParseReserved([]uint8{1}) // 1 byte
	require.Error(t, err)

	_, err = ParseReserved([]uint8{1, 2, 3, 4}) // 4 bytes
	require.Error(t, err)
}

func TestWARPKeyDerivation(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	pubKey := privKey.PublicKey()
	require.NotEmpty(t, pubKey.String())
	require.NotEqual(t, DefaultWarpPublicKey, pubKey.String())
}

func TestWARPConfigTranslation(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)
	peerKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	cfg := &WARPConfig{
		PrivateKey:    privKey,
		PeerPublicKey: peerKey.PublicKey(),
		Address: []netip.Prefix{
			netip.MustParsePrefix("172.16.0.2/32"),
			netip.MustParsePrefix("2606:4700:110:8::/128"),
		},
		Reserved: Reserved{1, 2, 3},
	}

	wgOpts := cfg.WireGuardEndpointOptions()
	require.Equal(t, uint32(DefaultWarpMTU), wgOpts.MTU)
	require.Equal(t, privKey.String(), wgOpts.PrivateKey)
	require.Len(t, wgOpts.Peers, 1)

	peer := wgOpts.Peers[0]
	require.Equal(t, DefaultWarpEndpointHost, peer.Address)
	require.Equal(t, uint16(DefaultWarpEndpointPort), peer.Port)
	require.Equal(t, peerKey.PublicKey().String(), peer.PublicKey)
	require.Equal(t, []uint8{1, 2, 3}, peer.Reserved)
	require.Equal(t, uint16(25), peer.PersistentKeepaliveInterval)
	require.Len(t, peer.AllowedIPs, 2)
}

func TestWARPConfigTranslationUsesBootstrapResolver(t *testing.T) {
	privateKey, err := GeneratePrivateKey()
	require.NoError(t, err)
	resolver := &option.DomainResolveOptions{Server: "bootstrap"}
	cfg := &WARPConfig{
		PrivateKey:        privateKey,
		Address:           []netip.Prefix{netip.MustParsePrefix("172.16.0.2/32")},
		BootstrapResolver: resolver,
	}
	options := cfg.WireGuardEndpointOptions()
	require.Same(t, resolver, options.DomainResolver)
}

func TestWARPStaticProvider(t *testing.T) {
	privKey, err := GeneratePrivateKey()
	require.NoError(t, err)

	opts := option.WireGuardWARPEndpointOptions{
		PrivateKey: privKey.String(),
		Address: badoption.Listable[netip.Prefix]{
			netip.MustParsePrefix("172.16.0.2/32"),
		},
		Reserved: []uint8{1, 2, 3},
	}

	provider := NewStaticProfileProvider(opts)
	cfg, err := provider.Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, privKey, cfg.PrivateKey)
	require.Equal(t, Reserved{1, 2, 3}, cfg.Reserved)
	require.Len(t, cfg.Address, 1)
}

func TestWARPStaticConfigurationMustBeComplete(t *testing.T) {
	_, err := NewWARPEndpoint(context.Background(), nil, log.NewNOPFactory().Logger(), "warp", option.WireGuardWARPEndpointOptions{
		Address: badoption.Listable[netip.Prefix]{netip.MustParsePrefix("172.16.0.2/32")},
	})
	require.ErrorContains(t, err, "private_key")

	privateKey, err := GeneratePrivateKey()
	require.NoError(t, err)
	_, err = NewWARPEndpoint(context.Background(), nil, log.NewNOPFactory().Logger(), "warp", option.WireGuardWARPEndpointOptions{
		PrivateKey: privateKey.String(),
	})
	require.ErrorContains(t, err, "address")
}

type mockProfileProvider struct {
	cfg *WARPConfig
	err error
}

func (m *mockProfileProvider) Load(ctx context.Context) (*WARPConfig, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.cfg, nil
}

func TestWARPProvisionFailureClean(t *testing.T) {
	mockErr := errors.New("simulated cloudflare api timeout")
	ep := &WARPEndpoint{
		ctx:      context.Background(),
		logger:   log.NewNOPFactory().Logger(),
		provider: &mockProfileProvider{err: mockErr},
	}
	err := ep.Start(adapter.StartStateStart)
	require.Error(t, err)
	require.Contains(t, err.Error(), "simulated cloudflare api timeout")
	require.False(t, ep.started)
	require.False(t, ep.ready)
	require.False(t, ep.closed)

	// Dial before ready should return error, not panic
	_, dialErr := ep.DialContext(context.Background(), "tcp", M.ParseSocksaddr("1.1.1.1:80"))
	require.Error(t, dialErr)
	require.Contains(t, dialErr.Error(), "not ready")
}

func TestWARPDialCloseRace(t *testing.T) {
	ep := &WARPEndpoint{}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for j := 0; j < 100; j++ {
			_, _ = ep.DialContext(context.Background(), "tcp", M.ParseSocksaddr("1.1.1.1:80"))
			_, _, _ = ep.ListenPacketWithDestination(context.Background(), M.ParseSocksaddr("1.1.1.1:53"))
			_, _ = ep.ListenPacket(context.Background(), M.ParseSocksaddr("1.1.1.1:53"))
		}
	}()

	go func() {
		defer wg.Done()
		_ = ep.Close()
	}()

	wg.Wait()
	require.True(t, ep.closed)
	require.NoError(t, ep.Close())
}

func TestWARPPostStartRequiresStart(t *testing.T) {
	ep := &WARPEndpoint{
		ctx:      context.Background(),
		logger:   log.NewNOPFactory().Logger(),
		provider: &mockProfileProvider{err: errors.New("provider err")},
	}
	err := ep.Start(adapter.StartStatePostStart)
	require.Error(t, err)
	require.Contains(t, err.Error(), "has not completed start stage")
}

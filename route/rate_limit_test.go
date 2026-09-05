package route

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	"github.com/sagernet/sing/common/byteformats"
	"github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

func TestRateLimitConfig_Validation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	logFactory := log.NewNOPFactory()

	var up1MB, down5MB byteformats.NetworkBytesCompat
	_ = json.Unmarshal([]byte(`"1 MB"`), &up1MB)
	_ = json.Unmarshal([]byte(`"5 MB"`), &down5MB)

	// 1. Duplicate shared rate_limiter tag -> fail
	opts1 := option.RouteOptions{
		RateLimiters: []option.RateLimiterOptions{
			{Tag: "pool1", Download: &down5MB},
			{Tag: "pool1", Upload: &up1MB},
		},
	}
	router1 := NewRouter(ctx, logFactory, opts1, option.DNSOptions{}, nil)
	err1 := router1.Initialize(nil, nil)
	require.ErrorContains(t, err1, "duplicate rate limiter tag")

	// 2. Empty rate_limiter tag -> fail
	opts2 := option.RouteOptions{
		RateLimiters: []option.RateLimiterOptions{
			{Tag: "", Download: &down5MB},
		},
	}
	router2 := NewRouter(ctx, logFactory, opts2, option.DNSOptions{}, nil)
	err2 := router2.Initialize(nil, nil)
	require.ErrorContains(t, err2, "empty rate limiter tag")

	// 3. No upload and no download limit -> fail
	opts3 := option.RouteOptions{
		RateLimiters: []option.RateLimiterOptions{
			{Tag: "empty-pool"},
		},
	}
	router3 := NewRouter(ctx, logFactory, opts3, option.DNSOptions{}, nil)
	err3 := router3.Initialize(nil, nil)
	require.ErrorContains(t, err3, "neither upload nor download limit")

	// 4. Unknown tag reference in rule -> fail
	opts4 := option.RouteOptions{
		Rules: []option.Rule{
			{
				Type: "default",
				DefaultOptions: option.DefaultRule{
					RawDefaultRule: option.RawDefaultRule{
						Inbound: []string{"in-1"},
					},
					RuleAction: option.RuleAction{
						Action: "route",
						RouteOptions: option.RouteActionOptions{
							Outbound: "direct",
							RawRouteOptionsActionOptions: option.RawRouteOptionsActionOptions{
								RateLimit: &option.RateLimitActionOptions{
									Tag: "non-existent-pool",
								},
							},
						},
					},
				},
			},
		},
	}
	router4 := NewRouter(ctx, logFactory, opts4, option.DNSOptions{}, nil)
	err4 := router4.Initialize(opts4.Rules, nil)
	require.ErrorContains(t, err4, "rate limiter not found in rule[0]: non-existent-pool")

	// 5. Valid shared limiter reference -> succeed
	opts5 := option.RouteOptions{
		RateLimiters: []option.RateLimiterOptions{
			{Tag: "streaming", Download: &down5MB},
		},
		Rules: []option.Rule{
			{
				Type: "default",
				DefaultOptions: option.DefaultRule{
					RawDefaultRule: option.RawDefaultRule{
						Inbound: []string{"in-1"},
					},
					RuleAction: option.RuleAction{
						Action: "route",
						RouteOptions: option.RouteActionOptions{
							Outbound: "direct",
							RawRouteOptionsActionOptions: option.RawRouteOptionsActionOptions{
								RateLimit: &option.RateLimitActionOptions{
									Tag: "streaming",
								},
							},
						},
					},
				},
			},
		},
	}
	router5 := NewRouter(ctx, logFactory, opts5, option.DNSOptions{}, nil)
	err5 := router5.Initialize(opts5.Rules, nil)
	require.NoError(t, err5)

	// 6. Valid inline limiter -> succeed
	opts6 := option.RouteOptions{
		Rules: []option.Rule{
			{
				Type: "default",
				DefaultOptions: option.DefaultRule{
					RawDefaultRule: option.RawDefaultRule{
						Inbound: []string{"in-1"},
					},
					RuleAction: option.RuleAction{
						Action: "route",
						RouteOptions: option.RouteActionOptions{
							Outbound: "direct",
							RawRouteOptionsActionOptions: option.RawRouteOptionsActionOptions{
								RateLimit: &option.RateLimitActionOptions{
									Upload:   &up1MB,
									Download: &down5MB,
								},
							},
						},
					},
				},
			},
		},
	}
	router6 := NewRouter(ctx, logFactory, opts6, option.DNSOptions{}, nil)
	err6 := router6.Initialize(opts6.Rules, nil)
	require.NoError(t, err6)
}

type mockOutbound struct {
	tag           string
	handler       func(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc)
	packetHandler func(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc)
}

func (m *mockOutbound) Type() string           { return "mock" }
func (m *mockOutbound) Tag() string            { return m.tag }
func (m *mockOutbound) Network() []string      { return []string{N.NetworkTCP, N.NetworkUDP} }
func (m *mockOutbound) Dependencies() []string { return nil }
func (m *mockOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return nil, nil
}
func (m *mockOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, nil
}
func (m *mockOutbound) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	if m.handler != nil {
		m.handler(ctx, conn, metadata, onClose)
	}
}
func (m *mockOutbound) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	if m.packetHandler != nil {
		m.packetHandler(ctx, conn, metadata, onClose)
	}
}

type mockOutboundManager struct {
	outbounds map[string]adapter.Outbound
}

func (m *mockOutboundManager) Start(stage adapter.StartStage) error { return nil }
func (m *mockOutboundManager) Close() error                         { return nil }
func (m *mockOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	o, ok := m.outbounds[tag]
	return o, ok
}
func (m *mockOutboundManager) Outbounds() []adapter.Outbound {
	return nil
}
func (m *mockOutboundManager) Default() adapter.Outbound {
	return m.outbounds["direct"]
}
func (m *mockOutboundManager) Remove(tag string) error { return nil }
func (m *mockOutboundManager) Create(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, outboundType string, options any) error {
	return nil
}

func TestRateLimit_RouteConnection_Throttled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	writeDone := make(chan time.Duration, 1)

	mockOB := &mockOutbound{
		tag: "direct",
		handler: func(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
			go func() {
				start := time.Now()
				// Write 60KB through the limited conn (rate limit is 50KB/s download)
				payload := make([]byte, 60*1024)
				_, _ = conn.Write(payload)
				writeDone <- time.Since(start)
				if onClose != nil {
					onClose(nil)
				}
			}()
		},
	}

	obMgr := &mockOutboundManager{
		outbounds: map[string]adapter.Outbound{
			"direct": mockOB,
		},
	}

	appCtx := service.ContextWith[adapter.OutboundManager](ctx, obMgr)
	logFactory := log.NewNOPFactory()

	var down50KB byteformats.NetworkBytesCompat
	_ = json.Unmarshal([]byte(`"50 KB"`), &down50KB)

	opts := option.RouteOptions{
		Rules: []option.Rule{
			{
				Type: "default",
				DefaultOptions: option.DefaultRule{
					RawDefaultRule: option.RawDefaultRule{
						Inbound: []string{"in-1"},
					},
					RuleAction: option.RuleAction{
						Action: "route",
						RouteOptions: option.RouteActionOptions{
							Outbound: "direct",
							RawRouteOptionsActionOptions: option.RawRouteOptionsActionOptions{
								RateLimit: &option.RateLimitActionOptions{
									Download: &down50KB,
								},
							},
						},
					},
				},
			},
		},
	}

	router := NewRouter(appCtx, logFactory, opts, option.DNSOptions{}, nil)
	err := router.Initialize(opts.Rules, nil)
	require.NoError(t, err)

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	// Drain clientConn in background
	go func() {
		buf := make([]byte, 4096)
		for {
			_, err := clientConn.Read(buf)
			if err != nil {
				return
			}
		}
	}()

	metadata := adapter.InboundContext{
		Inbound:     "in-1",
		Destination: M.ParseSocksaddr("1.1.1.1:80"),
	}

	err = router.RouteConnection(appCtx, serverConn, metadata)
	require.NoError(t, err)

	select {
	case elapsed := <-writeDone:
		// 60KB with 50KB/s limit (and burst cap 32KB) will take > 400ms
		require.GreaterOrEqual(t, elapsed, 400*time.Millisecond)
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for write to complete")
	}
}

func TestRateLimit_RoutePacketConnection_Throttled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	writeDone := make(chan time.Duration, 1)

	mockOB := &mockOutbound{
		tag: "direct",
		packetHandler: func(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
			go func() {
				start := time.Now()
				dest := M.ParseSocksaddr("1.1.1.1:53")
				// Write 3 packets of 16KB each (total 48KB, at 50KB/s download rate)
				for i := 0; i < 3; i++ {
					b := buf.NewSize(16 * 1024)
					b.Extend(16 * 1024)
					_ = conn.WritePacket(b, dest)
				}
				writeDone <- time.Since(start)
				if onClose != nil {
					onClose(nil)
				}
			}()
		},
	}

	obMgr := &mockOutboundManager{
		outbounds: map[string]adapter.Outbound{
			"direct": mockOB,
		},
	}

	appCtx := service.ContextWith[adapter.OutboundManager](ctx, obMgr)
	logFactory := log.NewNOPFactory()

	var down50KB byteformats.NetworkBytesCompat
	_ = json.Unmarshal([]byte(`"50 KB"`), &down50KB)

	opts := option.RouteOptions{
		Rules: []option.Rule{
			{
				Type: "default",
				DefaultOptions: option.DefaultRule{
					RawDefaultRule: option.RawDefaultRule{
						Inbound: []string{"in-1"},
					},
					RuleAction: option.RuleAction{
						Action: "route",
						RouteOptions: option.RouteActionOptions{
							Outbound: "direct",
							RawRouteOptionsActionOptions: option.RawRouteOptionsActionOptions{
								RateLimit: &option.RateLimitActionOptions{
									Download: &down50KB,
								},
							},
						},
					},
				},
			},
		},
	}

	router := NewRouter(appCtx, logFactory, opts, option.DNSOptions{}, nil)
	err := router.Initialize(opts.Rules, nil)
	require.NoError(t, err)

	udpListener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer udpListener.Close()

	packetConn := bufio.NewPacketConn(udpListener)

	metadata := adapter.InboundContext{
		Inbound:     "in-1",
		Destination: M.ParseSocksaddr("1.1.1.1:53"),
	}

	err = router.RoutePacketConnection(appCtx, packetConn, metadata)
	require.NoError(t, err)

	select {
	case elapsed := <-writeDone:
		require.GreaterOrEqual(t, elapsed, 100*time.Millisecond)
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for UDP write to complete")
	}
}

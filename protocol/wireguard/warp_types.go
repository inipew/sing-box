//go:build with_wireguard && with_warp

package wireguard

import (
	"net/netip"
	"time"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json/badoption"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const (
	DefaultWarpEndpointHost = "engage.cloudflareclient.com"
	DefaultWarpEndpointPort = 2408
	DefaultWarpPublicKey    = "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="
	DefaultWarpMTU          = 1280
)

type Key = wgtypes.Key
type Reserved [3]byte

func ParseKey(s string) (Key, error) {
	if s == "" {
		return Key{}, E.New("key cannot be empty")
	}
	return wgtypes.ParseKey(s)
}

func GeneratePrivateKey() (Key, error) {
	return wgtypes.GeneratePrivateKey()
}

func ParseReserved(r []uint8) (Reserved, error) {
	var res Reserved
	if len(r) == 0 {
		return res, nil
	}
	if len(r) != 3 {
		return res, E.New("invalid reserved value length: expected 3 bytes, got ", len(r))
	}
	copy(res[:], r)
	return res, nil
}

// WARPConfig is the verified, trusted domain model for creating a WireGuard Endpoint.
type WARPConfig struct {
	PrivateKey                  Key
	PeerPublicKey               Key
	PreSharedKey                *Key
	Address                     []netip.Prefix
	Reserved                    Reserved
	Server                      string
	ServerPort                  uint16
	MTU                         uint32
	Workers                     int
	UDPTimeout                  time.Duration
	PersistentKeepaliveInterval uint16

	System            bool
	Name              string
	ListenPort        uint16
	DialerOptions     option.DialerOptions
	BootstrapResolver *option.DomainResolveOptions
}

func (c *WARPConfig) WireGuardEndpointOptions() option.WireGuardEndpointOptions {
	mtu := c.MTU
	if mtu == 0 {
		mtu = DefaultWarpMTU
	}

	serverHost := c.Server
	if serverHost == "" {
		serverHost = DefaultWarpEndpointHost
	}

	serverPort := c.ServerPort
	if serverPort == 0 {
		serverPort = DefaultWarpEndpointPort
	}

	peerPublicKey := c.PeerPublicKey.String()
	if peerPublicKey == "" || peerPublicKey == "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" {
		peerPublicKey = DefaultWarpPublicKey
	}

	var pskStr string
	if c.PreSharedKey != nil {
		pskStr = c.PreSharedKey.String()
	}

	keepalive := c.PersistentKeepaliveInterval
	if keepalive == 0 {
		keepalive = 25
	}

	return option.WireGuardEndpointOptions{
		System:     c.System,
		Name:       c.Name,
		MTU:        mtu,
		Address:    badoption.Listable[netip.Prefix](c.Address),
		PrivateKey: c.PrivateKey.String(),
		ListenPort: c.ListenPort,
		Peers: []option.WireGuardPeer{
			{
				Address:                     serverHost,
				Port:                        serverPort,
				PublicKey:                   peerPublicKey,
				PreSharedKey:                pskStr,
				AllowedIPs:                  badoption.Listable[netip.Prefix]{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")},
				PersistentKeepaliveInterval: keepalive,
				Reserved:                    c.Reserved[:],
			},
		},
		UDPTimeout:    badoption.Duration(c.UDPTimeout),
		Workers:       c.Workers,
		DialerOptions: c.DialerOptions,
	}
}

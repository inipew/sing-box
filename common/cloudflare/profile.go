//go:build with_warp

package cloudflare

import "time"

const CurrentProfileVersion = 1

type StoredProfile struct {
	Version     int            `json:"version"`
	Credentials Credentials    `json:"credentials"`
	Tunnel      TunnelSettings `json:"tunnel"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type Credentials struct {
	ID         string `json:"id"`
	Token      string `json:"token"`
	License    string `json:"license,omitempty"`
	PrivateKey string `json:"private_key"`
}

type TunnelSettings struct {
	Address       []string `json:"address"`
	PeerPublicKey string   `json:"peer_public_key"`
	PeerEndpoint  string   `json:"peer_endpoint"`
	PeerPorts     []int    `json:"peer_ports,omitempty"`
}

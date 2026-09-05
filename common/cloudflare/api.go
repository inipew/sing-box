//go:build with_warp

package cloudflare

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
)

type Client struct {
	httpClient *http.Client
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{httpClient: httpClient}
}

func (c *Client) Register(ctx context.Context, privateKeyBase64, publicKeyBase64, license string) (*StoredProfile, error) {
	payload := map[string]any{
		"key":          publicKeyBase64,
		"install_id":   generateRandomHex(22),
		"fcm_token":    "",
		"referrer":     "",
		"warp_enabled": true,
		"tos":          time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		"type":         "Android",
		"locale":       "en_US",
	}

	reqBytes, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.cloudflareclient.com/v0a1922/reg", bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "okhttp/3.12.1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, E.Cause(err, "send register request to Cloudflare")
	}
	defer common.Close(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, E.New("failed to register warp, status: ", resp.Status, " body: ", string(bodyBytes))
	}

	var regResp struct {
		ID     string `json:"id"`
		Token  string `json:"token"`
		Config struct {
			Interface struct {
				Addresses struct {
					V4 string `json:"v4"`
					V6 string `json:"v6"`
				} `json:"addresses"`
			} `json:"interface"`
			Peers []struct {
				PublicKey string `json:"public_key"`
				Endpoint  struct {
					Host  string `json:"host"`
					Ports []int  `json:"ports"`
				} `json:"endpoint"`
			} `json:"peers"`
		} `json:"config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&regResp); err != nil {
		return nil, E.Cause(err, "decode register response")
	}

	var addresses []string
	if regResp.Config.Interface.Addresses.V4 != "" {
		addresses = append(addresses, regResp.Config.Interface.Addresses.V4)
	}
	if regResp.Config.Interface.Addresses.V6 != "" {
		addresses = append(addresses, regResp.Config.Interface.Addresses.V6)
	}
	if len(addresses) == 0 {
		return nil, E.New("no address assigned by Cloudflare WARP")
	}

	var peerPublicKey string
	var peerEndpoint string
	var peerPorts []int
	if len(regResp.Config.Peers) > 0 {
		peerPublicKey = regResp.Config.Peers[0].PublicKey
		peerEndpoint = regResp.Config.Peers[0].Endpoint.Host
		peerPorts = regResp.Config.Peers[0].Endpoint.Ports
	}

	profile := &StoredProfile{
		Version: CurrentProfileVersion,
		Credentials: Credentials{
			ID:         regResp.ID,
			Token:      regResp.Token,
			License:    license,
			PrivateKey: privateKeyBase64,
		},
		Tunnel: TunnelSettings{
			Address:       addresses,
			PeerPublicKey: peerPublicKey,
			PeerEndpoint:  peerEndpoint,
			PeerPorts:     peerPorts,
		},
		UpdatedAt: time.Now(),
	}

	if license != "" {
		if err := c.UpdateLicense(ctx, regResp.ID, regResp.Token, license); err != nil {
			// Non-fatal, return profile with warning in caller
		}
	}

	return profile, nil
}

func (c *Client) UpdateLicense(ctx context.Context, deviceID, token, license string) error {
	reqBytes, _ := json.Marshal(map[string]any{"license": license})
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPut,
		"https://api.cloudflareclient.com/v0a1922/reg/"+deviceID+"/account",
		bytes.NewReader(reqBytes),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "okhttp/3.12.1")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return E.Cause(err, "send update license request to Cloudflare")
	}
	defer common.Close(resp.Body)

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return E.New("failed to update license, status: ", resp.Status, " body: ", string(bodyBytes))
	}
	return nil
}

func generateRandomHex(length int) string {
	b := make([]byte, length)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

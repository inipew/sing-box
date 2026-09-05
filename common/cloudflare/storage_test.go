//go:build with_warp

package cloudflare

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAtomicSaveAndLoadProfile(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "warp.json")

	profile := &StoredProfile{
		Version: CurrentProfileVersion,
		Credentials: Credentials{
			ID:         "dev-123",
			Token:      "tok-456",
			License:    "lic-789",
			PrivateKey: "aW52YWxpZC1rZXktZm9yLXRlc3RpbmctcHVycG9zZXM=",
		},
		Tunnel: TunnelSettings{
			Address:       []string{"172.16.0.2/32", "2606:4700:110:8::/128"},
			PeerPublicKey: "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
			PeerEndpoint:  "engage.cloudflareclient.com",
			PeerPorts:     []int{2408, 500, 1701},
		},
		UpdatedAt: time.Now().Truncate(time.Second),
	}

	err := AtomicSaveProfile(filePath, profile)
	require.NoError(t, err)

	// Ensure .tmp file is removed after atomic rename
	_, err = os.Stat(filePath + ".tmp")
	require.True(t, os.IsNotExist(err))

	loaded, err := LoadProfile(filePath)
	require.NoError(t, err)
	require.Equal(t, profile.Version, loaded.Version)
	require.Equal(t, profile.Credentials.ID, loaded.Credentials.ID)
	require.Equal(t, profile.Credentials.PrivateKey, loaded.Credentials.PrivateKey)
	require.Equal(t, profile.Tunnel.PeerPublicKey, loaded.Tunnel.PeerPublicKey)
	require.Equal(t, profile.Tunnel.Address, loaded.Tunnel.Address)
	require.Equal(t, profile.Tunnel.PeerPorts, loaded.Tunnel.PeerPorts)
}

func TestLoadProfileCorrupted(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "corrupted.json")

	// Corrupted JSON
	err := os.WriteFile(filePath, []byte("{ malformed json"), 0o600)
	require.NoError(t, err)

	_, err = LoadProfile(filePath)
	require.Error(t, err)

	// Incomplete profile (missing private key)
	err = os.WriteFile(filePath, []byte(`{"version": 1, "credentials": {"id": "1"}, "tunnel": {"address": []}}`), 0o600)
	require.NoError(t, err)

	_, err = LoadProfile(filePath)
	require.Error(t, err)
}

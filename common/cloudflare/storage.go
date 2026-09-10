//go:build with_warp

package cloudflare

import (
	"encoding/json"
	"os"
	"path/filepath"

	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/tailscale/atomicfile"
)

func AtomicSaveProfile(filePath string, profile *StoredProfile) error {
	if profile == nil {
		return E.New("cannot save nil profile")
	}

	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return E.Cause(err, "marshal profile")
	}

	directory := filepath.Dir(filePath)
	if err = os.MkdirAll(directory, 0o700); err != nil {
		return E.Cause(err, "create profile directory")
	}
	if err = atomicfile.WriteFile(filePath, data, 0o600); err != nil {
		return E.Cause(err, "write profile file")
	}
	if directoryHandle, openErr := os.Open(directory); openErr == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}

	return nil
}

func LoadProfile(filePath string) (*StoredProfile, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var profile StoredProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, E.Cause(err, "unmarshal profile")
	}

	if profile.Credentials.PrivateKey == "" || len(profile.Tunnel.Address) == 0 {
		return nil, E.New("invalid or incomplete profile")
	}

	return &profile, nil
}

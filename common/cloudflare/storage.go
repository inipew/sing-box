//go:build with_warp

package cloudflare

import (
	"encoding/json"
	"os"

	E "github.com/sagernet/sing/common/exceptions"
)

func AtomicSaveProfile(filePath string, profile *StoredProfile) error {
	if profile == nil {
		return E.New("cannot save nil profile")
	}

	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return E.Cause(err, "marshal profile")
	}

	tmpPath := filePath + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return E.Cause(err, "create temporary profile file")
	}

	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		return E.Cause(err, "write temporary profile file")
	}

	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		return E.Cause(err, "sync temporary profile file")
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return E.Cause(err, "close temporary profile file")
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return E.Cause(err, "rename profile file")
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

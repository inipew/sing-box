//go:build with_warp

package main

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/sagernet/bbolt"
	"github.com/spf13/cobra"
	E "github.com/sagernet/sing/common/exceptions"
)

var (
	flagWarpExportOutbound string
	flagWarpExportFormat   string
	flagWarpExportCacheID  string
	flagWarpExportList     bool
)

// warpPublicKey is the Cloudflare WARP WireGuard peer public key.
const warpPublicKey = "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="

// warpEndpoint is the default Cloudflare WARP peer endpoint.
const warpEndpoint = "engage.cloudflareclient.com:2408"
const warpEndpointHost = "engage.cloudflareclient.com"
const warpEndpointPort = 2408

type WarpProfile struct {
	PrivateKey string   `json:"private_key"`
	Address    []string `json:"address"`
	DeviceID   string   `json:"device_id"`
	Token      string   `json:"token"`
	License    string   `json:"license,omitempty"`
}

var commandWarpExport = &cobra.Command{
	Use:   "warp-export [cache-path]",
	Short: "Export Cloudflare WARP account credentials",
	Long: `Export Cloudflare WARP account credentials from a cache database or warp.json.

Supported output formats (--format / -f):
  wg        WireGuard .conf format (default)
  json      Raw JSON of the stored profile
  sing      sing-box warp outbound with pre-filled credentials
  endpoint  sing-box WireGuard endpoint configuration`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cachePath := "cache.db"
		if len(args) > 0 {
			cachePath = args[0]
		}
		return runWarpExport(cachePath)
	},
}

func init() {
	commandWarpExport.Flags().StringVarP(&flagWarpExportOutbound, "outbound", "o", "", "Tag of the WARP outbound to export")
	commandWarpExport.Flags().StringVarP(&flagWarpExportFormat, "format", "f", "wg", "Output format: wg, json, sing, endpoint")
	commandWarpExport.Flags().StringVar(&flagWarpExportCacheID, "cache-id", "", "Cache ID used in sing-box experimental.cache_file.cache_id")
	commandWarpExport.Flags().BoolVarP(&flagWarpExportList, "list", "l", false, "List all WARP tags stored in the cache database")
	commandTools.AddCommand(commandWarpExport)
}

func runWarpExport(cachePath string) error {
	var (
		profileData string
		selectedTag string
		bboltErr    error
		jsonErr     error
	)

	// 1. Try to load from cache database.
	db, err := bbolt.Open(cachePath, 0o666, &bbolt.Options{
		ReadOnly: true,
		Timeout:  2 * time.Second,
	})
	if err != nil {
		bboltErr = err
	} else {
		defer db.Close()
		bboltErr = db.View(func(tx *bbolt.Tx) error {
			// Resolve the bucket — support optional cache_id nesting.
			var bucket *bbolt.Bucket
			if flagWarpExportCacheID != "" {
				cacheIDBucket := tx.Bucket(append([]byte{0}, []byte(flagWarpExportCacheID)...))
				if cacheIDBucket != nil {
					bucket = cacheIDBucket.Bucket([]byte("warp"))
				}
			} else {
				bucket = tx.Bucket([]byte("warp"))
			}

			if bucket == nil {
				return E.New("warp bucket not found in cache database")
			}

			// Collect all stored tags.
			var keys []string
			_ = bucket.ForEach(func(k, _ []byte) error {
				keys = append(keys, string(k))
				return nil
			})
			if len(keys) == 0 {
				return E.New("no WARP accounts found in cache database")
			}

			// --list: just print tags and exit.
			if flagWarpExportList {
				fmt.Println("WARP accounts stored in cache database:")
				for _, k := range keys {
					fmt.Println(" ", k)
				}
				return nil
			}

			if flagWarpExportOutbound != "" {
				val := bucket.Get([]byte(flagWarpExportOutbound))
				if len(val) == 0 {
					return E.New("WARP account not found for tag: ", flagWarpExportOutbound,
						". Available: ", strings.Join(keys, ", "))
				}
				profileData = string(val)
				selectedTag = flagWarpExportOutbound
			} else if len(keys) == 1 {
				profileData = string(bucket.Get([]byte(keys[0])))
				selectedTag = keys[0]
			} else {
				return E.New("multiple WARP accounts found. Specify one with --outbound [-o]. Available: ",
					strings.Join(keys, ", "))
			}
			return nil
		})
	}

	// Early exit for --list (already printed above).
	if flagWarpExportList {
		return bboltErr
	}

	// 2. Fall back to warp.json if database yielded nothing.
	if profileData == "" {
		data, readFileErr := os.ReadFile("warp.json")
		if readFileErr == nil {
			profileData = string(data)
			selectedTag = "warp"
		} else {
			jsonErr = readFileErr
		}
	}

	if profileData == "" {
		// Both sources failed — report the most relevant error.
		if bboltErr != nil && jsonErr != nil {
			return E.New("no WARP profile found: cache.db (", bboltErr, "), warp.json (", jsonErr, ")")
		} else if bboltErr != nil {
			return E.Cause(bboltErr, "no WARP profile found in cache database")
		}
		return E.New("no WARP profile found in cache database or warp.json")
	}

	var profile WarpProfile
	if err := json.Unmarshal([]byte(profileData), &profile); err != nil {
		return E.Cause(err, "decode WARP profile JSON")
	}
	if profile.PrivateKey == "" || len(profile.Address) == 0 {
		return E.New("invalid or incomplete WARP profile (missing private_key or address)")
	}

	// Normalize addresses — ensure every entry has a CIDR prefix length.
	normalizedAddresses := normalizeWarpAddresses(profile.Address)

	format := strings.ToLower(flagWarpExportFormat)
	switch format {
	case "wg", "wireguard", "conf":
		fmt.Printf("[Interface]\n")
		fmt.Printf("PrivateKey = %s\n", profile.PrivateKey)
		fmt.Printf("Address = %s\n", strings.Join(normalizedAddresses, ", "))
		fmt.Printf("DNS = 1.1.1.1, 2606:4700:4700::1111\n")
		fmt.Printf("MTU = 1280\n")
		fmt.Printf("\n[Peer]\n")
		fmt.Printf("PublicKey = %s\n", warpPublicKey)
		fmt.Printf("Endpoint = %s\n", warpEndpoint)
		fmt.Printf("AllowedIPs = 0.0.0.0/0, ::/0\n")

	case "json":
		indented, _ := json.MarshalIndent(profile, "", "  ")
		fmt.Println(string(indented))

	case "sing", "sing-box":
		// Export as a warp outbound with pre-filled credentials.
		// Setting private_key + address skips automatic Cloudflare registration.
		outboundConfig := map[string]any{
			"type":        "warp",
			"tag":         selectedTag,
			"private_key": profile.PrivateKey,
			"address":     normalizedAddresses,
		}
		indented, _ := json.MarshalIndent(outboundConfig, "", "  ")
		fmt.Println(string(indented))

	case "endpoint", "wg-endpoint":
		// Export as a sing-box WireGuard endpoint configuration.
		endpointConfig := map[string]any{
			"type":        "wireguard",
			"tag":         selectedTag + "-endpoint",
			"address":     normalizedAddresses,
			"private_key": profile.PrivateKey,
			"peers": []map[string]any{
				{
					"address":     warpEndpointHost,
					"port":        warpEndpointPort,
					"public_key":  warpPublicKey,
					"allowed_ips": []string{"0.0.0.0/0", "::/0"},
				},
			},
			"mtu": 1280,
		}
		indented, _ := json.MarshalIndent(endpointConfig, "", "  ")
		fmt.Println(string(indented))

	default:
		return E.New("unsupported format: \"", flagWarpExportFormat, "\". Use: wg, json, sing, endpoint")
	}

	return nil
}

// normalizeWarpAddresses ensures every address has a CIDR prefix length.
// Cloudflare's API returns bare IPs (e.g. "172.16.0.2") which are valid
// internally but need a prefix for WireGuard and sing-box configs.
func normalizeWarpAddresses(addresses []string) []string {
	normalized := make([]string, 0, len(addresses))
	for _, addr := range addresses {
		if strings.Contains(addr, "/") {
			normalized = append(normalized, addr)
			continue
		}
		// Bare IP — add the appropriate host prefix length.
		ip, err := netip.ParseAddr(addr)
		if err != nil {
			normalized = append(normalized, addr) // pass through unknown values
			continue
		}
		if ip.Is4() {
			normalized = append(normalized, netip.PrefixFrom(ip, 32).String())
		} else {
			normalized = append(normalized, netip.PrefixFrom(ip, 128).String())
		}
	}
	return normalized
}

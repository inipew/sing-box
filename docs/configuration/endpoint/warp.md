### Structure

```json
{
  "type": "warp",
  "tag": "warp-ep",

  "system": false,
  "name": "",
  "server": "engage.cloudflareclient.com",
  "server_port": 2408,
  "private_key": "",
  "peer_public_key": "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
  "pre_shared_key": "",
  "address": [
    "172.16.0.2/32",
    "2606:4700:110:8f83:3cd3:bc68:e3b7:58bb/128"
  ],
  "reserved": [0, 0, 0],
  "mtu": 1280,
  "udp_timeout": "5m",
  "persistent_keepalive_interval": 0,
  "workers": 4,
  "provision": {
    "license": "",
    "cache_path": "",
    "recreate": false
  }
}
```

### Fields

#### system

Optional. Enable WireGuard system interface.

#### name

Optional. WireGuard interface name.

#### server

The WARP server address. Cloudflare WARP default `engage.cloudflareclient.com` is used if empty.

#### server_port

The WARP server port. Cloudflare WARP default `2408` is used if empty.

#### private_key

The base64-encoded WireGuard private key. Automatically registered with Cloudflare if empty.

#### peer_public_key

The base64-encoded Peer public key. Cloudflare WARP default `bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=` is used if empty.

#### pre_shared_key

The base64-encoded WireGuard pre-shared key. Optional.

#### address

A list of IP (v4 or v6) address prefixes to be assigned to the interface.

#### reserved

WireGuard reserved field bytes (must be exactly 3 bytes).

#### mtu

The maximum transmission unit. `1280` will be used if empty.

#### udp_timeout

The UDP connection timeout.

#### persistent_keepalive_interval

WireGuard persistent keepalive interval in seconds. Default `25` seconds.

#### workers

The number of WireGuard workers.

#### bootstrap_resolver

Domain resolve options for resolving the WARP server endpoint. See [Domain Resolve Options](/configuration/shared/domain_resolve/) for details.

#### provision

Optional Cloudflare account auto-provisioning settings:

- `license`: Cloudflare WARP account license key (e.g. for WARP+ upgrade).
- `cache_path`: Optional custom file path to store/load profile JSON (defaults to `warp.json`).
- `recreate`: If `true`, ignores cached profile and registers a new account.

---

### Tools

#### warp-export

The `warp-export` tool allows you to export Cloudflare WARP credentials/profile stored in a cache database (`cache.db`) or `warp.json`.

```shell
sing-box tools warp-export [cache-path] [flags]
```

##### Arguments

- `[cache-path]`: Path to the cache database. Defaults to `cache.db`.

##### Flags

- `-o`, `--outbound <tag>`: Specify the tag of the WARP endpoint to export (required if multiple accounts are stored in the database).
- `-f`, `--format <format>`: Specify the output format.
  - `wg`: WireGuard `.conf` format (default).
  - `json`: Raw JSON profile format.
  - `sing`: `sing-box` `warp` endpoint structure with pre-filled credentials.
  - `endpoint`: `sing-box` `wireguard` endpoint structure.
- `--cache-id <id>`: Specifies the namespaced cache ID used if `experimental.cache_file.cache_id` is defined.
- `-l`, `--list`: Lists all WARP tags stored in the cache database.

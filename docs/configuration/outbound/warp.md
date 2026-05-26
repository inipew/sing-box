### Structure

```json
{
  "type": "warp",
  "tag": "warp-out",

  "server": "engage.cloudflareclient.com",
  "server_port": 2408,
  "private_key": "",
  "public_key": "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
  "pre_shared_key": "",
  "address": [
    "172.16.0.2/32",
    "2606:4700:110:8f83:3cd3:bc68:e3b7:58bb/128"
  ],
  "reserved": [0, 0, 0],
  "mtu": 1280,
  "udp_timeout": "5m",
  "persistent_keepalive": 0,
  "workers": 4,
  "license": ""
}
```

### Fields

#### server

The WARP server address. Cloudflare WARP default `engage.cloudflareclient.com` is used if empty.

#### server_port

The WARP server port. Cloudflare WARP default `2408` is used if empty.

#### private_key

The base64-encoded WireGuard private key. Automatically registered with Cloudflare if empty.

#### public_key

The base64-encoded Peer public key. Cloudflare WARP default is used if empty.

#### pre_shared_key

The base64-encoded WireGuard pre-shared key. Optional.

#### address

A list of IP (v4 or v6) address prefixes to be assigned to the interface.

#### reserved

WireGuard reserved field bytes.

#### mtu

The maximum transmission unit. `1280` will be used if empty.

#### udp_timeout

The UDP connection timeout.

#### persistent_keepalive

WireGuard persistent keepalive interval in seconds.

#### workers

The number of WireGuard workers.

#### inner_domain_resolver

Domain resolve options for internal/inner domain resolution. See [Domain Resolve Options](/configuration/shared/domain_resolve/) for details.

#### license

Cloudflare WARP account license key (e.g. for WARP+ upgrade).

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

- `-o`, `--outbound <tag>`: Specify the tag of the WARP outbound to export (required if multiple accounts are stored in the database).
- `-f`, `--format <format>`: Specify the output format.
  - `wg`: WireGuard `.conf` format (default).
  - `json`: Raw JSON profile format.
  - `sing`: `sing-box` `warp` outbound structure with pre-filled credentials.
  - `endpoint`: `sing-box` `wireguard` outbound endpoint structure.
- `--cache-id <id>`: Specifies the namespaced cache ID used if `experimental.cache_file.cache_id` is defined.
- `-l`, `--list`: Lists all WARP tags stored in the cache database.

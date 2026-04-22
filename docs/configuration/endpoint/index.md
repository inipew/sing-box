!!! question "Since sing-box 1.11.0"

# Endpoint

An endpoint is a protocol with inbound and outbound behavior.

### Structure

```json
{
  "endpoints": [
    {
      "type": "",
      "tag": ""
    }
  ]
}
```

### Fields

| `wireguard`  | [WireGuard](./wireguard/)   |
| `tailscale`  | [Tailscale](./tailscale/)   |
| `cloudflare` | [Cloudflare](./cloudflare/) |

#### tag

The tag of the endpoint.

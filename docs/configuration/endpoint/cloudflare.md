---
icon: material/cloud-outline
---

# Cloudflare Endpoint

The Cloudflare endpoint implements the Cloudflare WARP MASQUE (Connect-IP) protocol.

### Structure

```json
{
  "type": "cloudflare",
  "tag": "cloudflare-out",

  "server": "engage.cloudflareclient.com",
  "server_port": 443,
  "private_key": "",
  "public_key": "",
  "certificate": "",
  "id": "10f85726-f693-4188-abe1-c1a3d0eab3de",
  "token": "a2bd13c1-71c5-47b7-9d5f-9c209bbdbeba",
  "remote_ip": "172.16.0.2",
  "remote_ipv6": "2606:4700:110:824e:c877:7af5:44e2:b14a",
  "mtu": 1280,
  "udp_timeout": "5m",

  ... // Dial Fields
}
```

### Fields

#### server

The Cloudflare WARP endpoint address.

`engage.cloudflareclient.com` will be used if empty.

#### server_port

The Cloudflare WARP endpoint port.

`443` will be used if empty.

#### private_key

==Required==

The base64-encoded ECDSA private key of the device.

#### public_key

==Required==

The base64-encoded ECDSA public key of the Cloudflare endpoint.

#### certificate

==Required==

The base64-encoded certificate of the device.

#### id

The device unique identifier (UUID).

#### token

The authentication token for API access (license).

#### remote_ip

The IPv4 address assigned by Cloudflare.

#### remote_ipv6

The IPv6 address assigned by Cloudflare.

#### mtu

The maximum transmission unit (MTU).

`1280` will be used if empty.

#### udp_timeout

The QUIC keep-alive period.

### Dial Fields

See [Dial Fields](/configuration/shared/dial/) for details.

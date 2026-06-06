# MASQUE

MASQUE is an IP Tunnel outbound over HTTP/3 Extended CONNECT.

### Structure

```json
{
  "type": "masque",
  "tag": "masque-out",

  "server": "192.168.1.1",
  "server_port": 443,
  "system": false,
  "gso": false,
  "address": [
    "172.16.0.2/32"
  ],
  "private_key": "",
  "public_key": "",
  "uri": "https://cloudflareaccess.com",
  "sni": "consumer-masque.cloudflareclient.com",
  "skip_cert_verify": false,
  "mtu": 1280,
  "inner_domain_resolver": {}
}
```

### Fields

#### server

Required. The server address.

#### server_port

Required. The server port.

#### system

Optional. Enable the platform/system TUN stack. Requires admin/root privileges.

#### gso

Optional. Enable Generic Segmentation Offload. Only works with `system: true`.

#### address

Required. The IPv4 or IPv6 address prefixes for the local virtual network interface.

#### private_key

Required. Base64-encoded ECDSA private key for MASQUE authentication.

#### public_key

Required. Base64-encoded peer ECDSA public key for the remote MASQUE endpoint.

#### uri

Optional. The HTTP/3 Extended CONNECT URI (default `https://cloudflareaccess.com`).

#### sni

Optional. TLS Server Name Indication (default `consumer-masque.cloudflareclient.com`).

#### skip_cert_verify

Optional. Disables TLS certificate validation (not recommended).

#### mtu

Optional. MTU of the virtual network interface (default `1280`).

#### inner_domain_resolver

Optional. This option uses the same format as [domain_resolver](/configuration/shared/dial/#domain_resolver).

---

!!! note ""
    [Dialer Fields](../shared/dial.md)

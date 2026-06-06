//go:build with_masque

package masque

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"time"

	"github.com/sagernet/quic-go/http3"
	E "github.com/sagernet/sing/common/exceptions"
)

// PrepareTLSConfig creates a TLS configuration using the provided certificate and SNI.
func PrepareTLSConfig(privKey *ecdsa.PrivateKey, peerPubKey *ecdsa.PublicKey, sni string, insecure bool) (*tls.Config, error) {
	verifyCert := func(cert *x509.Certificate) error {
		if _, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok {
			return E.New("remote endpoint does not use ECDSA key")
		}
		if !cert.PublicKey.(*ecdsa.PublicKey).Equal(peerPubKey) {
			return E.New("remote endpoint has a different public key than what we trust")
		}
		return nil
	}

	cert, err := GenerateCert(privKey)
	if err != nil {
		return nil, E.Cause(err, "generate self-signed cert")
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{
			{
				Certificate: cert,
				PrivateKey:  privKey,
			},
		},
		ServerName:         sni,
		NextProtos:         []string{http3.NextProtoH3},
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			var err error
			for _, cert := range cs.PeerCertificates {
				if err = verifyCert(cert); err == nil {
					return nil // Found a matching trusted cert
				}
			}
			if err != nil {
				return E.Cause(err, "verify peer certificates")
			}
			return E.New("no peer certificates presented")
		},
	}
	if insecure {
		tlsConfig.VerifyConnection = nil
	}

	return tlsConfig, nil
}

func GenerateCert(privKey *ecdsa.PrivateKey) ([][]byte, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, E.Cause(err, "generate serial number")
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}

	cert, err := x509.CreateCertificate(rand.Reader, &template, &template, &privKey.PublicKey, privKey)
	if err != nil {
		return nil, E.Cause(err, "create certificate")
	}

	return [][]byte{cert}, nil
}

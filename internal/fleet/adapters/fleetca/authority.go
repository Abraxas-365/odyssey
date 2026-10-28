// Package fleetca is the fleet's certificate authority on PEM files: it
// signs the agents' client certificates and the gateway's own serving
// certificate. Only odyssey-gateway holds the key; the API needs the
// certificate alone (the pin it puts in enrolment tokens). A CA in Vault or
// a cloud KMS is another adapter of fleet.Authority.
package fleetca

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
)

// Authority signs with a CA key held in memory.
type Authority struct {
	cert    *x509.Certificate
	certPEM []byte
	key     crypto.Signer // nil: verify-only (the API)
}

var _ fleet.Authority = (*Authority)(nil)

// Load reads the CA certificate and, when keyFile is not empty, its key.
func Load(certFile, keyFile string) (*Authority, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, errx.Wrap(err, "fleet CA certificate", errx.TypeInternal)
	}
	var keyPEM []byte
	if keyFile != "" {
		if keyPEM, err = os.ReadFile(keyFile); err != nil {
			return nil, errx.Wrap(err, "fleet CA key", errx.TypeInternal)
		}
	}
	return Parse(certPEM, keyPEM)
}

// Parse builds an Authority from PEM (keyPEM may be empty).
func Parse(certPEM, keyPEM []byte) (*Authority, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errx.Internal("fleet CA certificate: no PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errx.Wrap(err, "fleet CA certificate", errx.TypeInternal)
	}
	if !cert.IsCA {
		return nil, errx.Internal("fleet CA certificate is not a CA")
	}
	a := &Authority{cert: cert, certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})}
	if len(keyPEM) == 0 {
		return a, nil
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, errx.Internal("fleet CA key: no PEM block")
	}
	k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		if ek, err2 := x509.ParseECPrivateKey(kb.Bytes); err2 == nil {
			k = ek
		} else {
			return nil, errx.Wrap(err, "fleet CA key", errx.TypeInternal)
		}
	}
	signer, ok := k.(crypto.Signer)
	if !ok {
		return nil, errx.Internal("fleet CA key cannot sign")
	}
	if !publicKeysEqual(signer.Public(), cert.PublicKey) {
		return nil, errx.Internal("fleet CA key does not match its certificate")
	}
	a.key = signer
	return a, nil
}

// Pin is "sha256:<hex>" of a certificate's public key (SubjectPublicKeyInfo).
func Pin(cert *x509.Certificate) string {
	h := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(h[:])
}

func (a *Authority) Trust() (fleet.Trust, error) {
	return fleet.Trust{CAPEM: a.certPEM, Pin: Pin(a.cert)}, nil
}

// Pool is the CA as a certificate pool (to verify agents).
func (a *Authority) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(a.cert)
	return p
}

// Sign issues a client certificate for the CSR's key naming uri, valid
// from a minute before now (clock skew) for ttl.
func (a *Authority) Sign(csr *x509.CertificateRequest, uri string, now time.Time, ttl time.Duration) (fleet.Issued, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return fleet.Issued{}, errx.Wrap(err, "agent identity", errx.TypeInternal)
	}
	tmpl := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "odyssey-agent"},
		URIs:        []*url.URL{u},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	return a.issue(tmpl, csr.PublicKey, now, ttl)
}

// ServerCertificate issues the gateway's serving certificate for hosts
// (names or IPs), with a new key.
func (a *Authority) ServerCertificate(hosts []string, now time.Time, ttl time.Duration) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "odyssey-gateway"},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	issued, err := a.issue(tmpl, key.Public(), now, ttl)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, _ := pem.Decode(issued.PEM)
	parsed, err := x509.ParseCertificate(leaf.Bytes)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{leaf.Bytes, a.cert.Raw}, PrivateKey: key, Leaf: parsed}, nil
}

func (a *Authority) issue(tmpl *x509.Certificate, pub any, now time.Time, ttl time.Duration) (fleet.Issued, error) {
	if a.key == nil {
		return fleet.Issued{}, errx.Internal("this process holds no fleet CA key (only odyssey-gateway signs)")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return fleet.Issued{}, errx.Wrap(err, "certificate serial", errx.TypeInternal)
	}
	tmpl.SerialNumber = serial
	// tolerate clock skew with the cluster, but by at most a tenth of a short
	// lifetime (the agent renews at two thirds of NotBefore..NotAfter)
	tmpl.NotBefore = now.Add(-min(time.Minute, ttl/10)).UTC()
	tmpl.NotAfter = now.Add(ttl).UTC()
	if tmpl.NotAfter.After(a.cert.NotAfter) {
		tmpl.NotAfter = a.cert.NotAfter
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, pub, a.key)
	if err != nil {
		return fleet.Issued{}, errx.Wrap(err, "sign certificate", errx.TypeInternal)
	}
	return fleet.Issued{
		PEM:       pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		CAPEM:     a.certPEM,
		Serial:    fleet.Serial(serial),
		ExpiresAt: tmpl.NotAfter.Truncate(time.Second),
	}, nil
}

// Generate creates a new CA (certificate and PKCS#8 key, PEM) valid for ttl.
func Generate(name string, now time.Time, ttl time.Duration) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name, Organization: []string{"Odyssey"}},
		NotBefore:             now.Add(-time.Minute).UTC(),
		NotAfter:              now.Add(ttl).UTC(),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, nil, err
	}
	kd, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), nil
}

func publicKeysEqual(a, b any) bool {
	type equaler interface{ Equal(crypto.PublicKey) bool }
	e, ok := a.(equaler)
	return ok && e.Equal(b)
}

// Disabled is the Authority of a deployment where the fleet is not
// configured: clusters can be listed, not registered.
type Disabled struct{}

var _ fleet.Authority = Disabled{}

func (Disabled) Trust() (fleet.Trust, error) {
	return fleet.Trust{}, errx.Business("Esta instalación de Odyssey todavía no puede conectar clústeres: falta configurar el gateway (FLEET_CA_CERT y FLEET_GATEWAY_URL).").
		WithCode(fleet.CodeFleetDisabled)
}

func (Disabled) Sign(*x509.CertificateRequest, string, time.Time, time.Duration) (fleet.Issued, error) {
	return fleet.Issued{}, errx.Internal("the fleet CA is not configured")
}

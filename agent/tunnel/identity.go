package tunnel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Identity is what the agent holds after enrolling: its key (generated in
// the cluster, it never leaves), its certificate and where the gateway is.
type Identity struct {
	Organization string
	Cluster      string
	KeyPEM       []byte
	CertPEM      []byte
	// CAPEM is the fleet CA the gateway answered with (its pin was checked).
	CAPEM []byte
	// Gateway is host:port; CAPin is "sha256:<hex>" of the CA's public key.
	Gateway string
	CAPin   string
}

// Enrollment is what an operator hands the agent once.
type Enrollment struct {
	Token   string
	Gateway string
	CAPin   string
}

func (e Enrollment) complete() bool { return e.Token != "" && e.Gateway != "" && e.CAPin != "" }

// keyPair parses the identity's certificate and key.
func (id Identity) keyPair() (tls.Certificate, error) {
	c, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("agent identity: %w", err)
	}
	return c, nil
}

// renewAt is when the certificate has lived 2/3 of its life.
func renewAt(c *x509.Certificate) time.Time {
	return c.NotBefore.Add(c.NotAfter.Sub(c.NotBefore) * 2 / 3)
}

// newKey generates a key and a certificate request for it. The subject is
// ignored by the gateway: the certificate names the cluster of the token.
func newKey() (keyPEM, csrDER []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	csrDER, err = x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "odyssey-agent"}}, key)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), csrDER, nil
}

// Pin is "sha256:<hex>" of a certificate's public key (SubjectPublicKeyInfo).
func Pin(c *x509.Certificate) string {
	h := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(h[:])
}

// checkCA confirms the CA PEM the gateway sent is the pinned one.
func checkCA(caPEM []byte, pin string) error {
	b, _ := pem.Decode(caPEM)
	if b == nil {
		return errors.New("the gateway sent no CA certificate")
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return fmt.Errorf("the gateway's CA certificate: %w", err)
	}
	if !strings.EqualFold(Pin(c), pin) {
		return errors.New("the gateway's CA does not match the pinned one")
	}
	return nil
}

// tlsConfig trusts only a gateway whose chain ends at the pinned CA and
// whose certificate names the dialed host; the system's CAs play no part.
// cert supplies the client certificate (nil: none, for Enroll).
func tlsConfig(gateway, pin string, cert func() *tls.Certificate) (*tls.Config, error) {
	host, _, err := net.SplitHostPort(gateway)
	if err != nil {
		return nil, fmt.Errorf("gateway %q: %w", gateway, err)
	}
	if !strings.HasPrefix(strings.ToLower(pin), "sha256:") {
		return nil, fmt.Errorf("CA pin must be sha256:<hex>")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: host,
		// Verification is VerifyPeerCertificate's, against the pin alone.
		InsecureSkipVerify: true, //nolint:gosec // replaced by pinned verification below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			return verifyPinned(raw, host, pin)
		},
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			if cert == nil {
				return &tls.Certificate{}, nil
			}
			if c := cert(); c != nil {
				return c, nil
			}
			return &tls.Certificate{}, nil
		},
	}, nil
}

func verifyPinned(raw [][]byte, host, pin string) error {
	if len(raw) == 0 {
		return errors.New("the gateway presented no certificate")
	}
	certs := make([]*x509.Certificate, 0, len(raw))
	for _, r := range raw {
		c, err := x509.ParseCertificate(r)
		if err != nil {
			return fmt.Errorf("gateway certificate: %w", err)
		}
		certs = append(certs, c)
	}
	roots := x509.NewCertPool()
	found := false
	for _, c := range certs[1:] {
		if c.IsCA && strings.EqualFold(Pin(c), pin) {
			roots.AddCert(c)
			found = true
		}
	}
	if !found {
		return errors.New("the gateway's certificate is not from the pinned fleet CA")
	}
	_, err := certs[0].Verify(x509.VerifyOptions{DNSName: host, Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	if err != nil {
		return fmt.Errorf("gateway certificate: %w", err)
	}
	return nil
}

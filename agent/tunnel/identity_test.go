package tunnel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newCA(t *testing.T) testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return testCA{c, key}
}

func (ca testCA) server(t *testing.T, host string) []byte {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "gw"}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, key.Public(), ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestVerifyPinned(t *testing.T) {
	ca, other := newCA(t), newCA(t)
	leaf := ca.server(t, "gateway.test")
	pin := Pin(ca.cert)
	if err := verifyPinned([][]byte{leaf, ca.cert.Raw}, "gateway.test", pin); err != nil {
		t.Fatalf("valid chain: %v", err)
	}
	for name, c := range map[string]struct {
		raw  [][]byte
		host string
		pin  string
	}{
		"other CA's pin":         {[][]byte{leaf, ca.cert.Raw}, "gateway.test", Pin(other.cert)},
		"leaf of another CA":     {[][]byte{other.server(t, "gateway.test"), ca.cert.Raw}, "gateway.test", pin},
		"wrong host":             {[][]byte{leaf, ca.cert.Raw}, "evil.test", pin},
		"no chain":               {[][]byte{leaf}, "gateway.test", pin},
		"nothing":                {nil, "gateway.test", pin},
		"leaf posing as the CA":  {[][]byte{leaf, leaf}, "gateway.test", pin},
		"other CA sent with pin": {[][]byte{other.server(t, "gateway.test"), other.cert.Raw}, "gateway.test", pin},
	} {
		if err := verifyPinned(c.raw, c.host, c.pin); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCheckCA(t *testing.T) {
	ca, other := newCA(t), newCA(t)
	p := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw})
	if err := checkCA(p, strings.ToUpper(Pin(ca.cert)[:7])+Pin(ca.cert)[7:]); err != nil {
		t.Errorf("pinned CA: %v", err)
	}
	if err := checkCA(p, Pin(other.cert)); err == nil {
		t.Error("another CA was accepted")
	}
	if err := checkCA([]byte("garbage"), Pin(ca.cert)); err == nil {
		t.Error("garbage was accepted")
	}
}

func TestTLSConfigNeedsAPinAndAHost(t *testing.T) {
	if _, err := tlsConfig("gateway.test", "sha256:00", nil); err == nil {
		t.Error("gateway without port accepted")
	}
	if _, err := tlsConfig("gateway.test:8443", "md5:00", nil); err == nil {
		t.Error("non-sha256 pin accepted")
	}
}

func TestRenewAtTwoThirds(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := &x509.Certificate{NotBefore: start, NotAfter: start.Add(30 * 24 * time.Hour)}
	if got := renewAt(c); !got.Equal(start.Add(20 * 24 * time.Hour)) {
		t.Errorf("renewAt %v", got)
	}
}

func TestJitterStaysWithinTwentyPercent(t *testing.T) {
	for range 1000 {
		if d := jitter(10 * time.Second); d < 8*time.Second || d > 12*time.Second {
			t.Fatalf("jitter %v", d)
		}
	}
}

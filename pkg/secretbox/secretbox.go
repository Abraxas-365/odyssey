// Package secretbox seals a managed secret's value to one cluster (phase 2a).
// A person types the value in the console; the browser seals it with the
// cluster's public key (HPKE, RFC 9180) and only that cluster's agent can
// open it. The control plane carries the sealed bytes without being able to
// read them, and keeps them only until the agent acknowledges them.
//
// Both planes and the console (frontend/src/lib/secretbox.ts, with
// @hpke/core) implement exactly this: same suite, same info.
package secretbox

import (
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

// Suite is the only HPKE ciphersuite: DHKEM(X25519, HKDF-SHA256),
// HKDF-SHA256 and AES-256-GCM (AES-GCM because WebCrypto has it).
const Suite = "DHKEM-X25519-HKDF-SHA256/HKDF-SHA256/AES-256-GCM"

// MaxValue bounds a secret's value (a Kubernetes Secret holds 1 MiB in all;
// a single variable has no business near that).
const MaxValue = 64 << 10

// MaxSealed bounds the sealed bytes: the encapsulated key (32), the value
// and the AEAD tag (16).
const MaxSealed = 32 + MaxValue + 16

// MinSealed is a sealed empty value: the encapsulated key and the tag.
const MinSealed = 32 + 16

var kem = hpke.DHKEM(ecdh.X25519())

// Key is a cluster's key pair.
type Key struct{ priv hpke.PrivateKey }

// GenerateKey makes a new cluster key.
func GenerateKey() (Key, error) {
	k, err := kem.GenerateKey()
	if err != nil {
		return Key{}, err
	}
	return Key{priv: k}, nil
}

// ParseKey reads a key saved with Key.Bytes.
func ParseKey(b []byte) (Key, error) {
	k, err := kem.NewPrivateKey(b)
	if err != nil {
		return Key{}, errors.New("secretbox: not a key")
	}
	return Key{priv: k}, nil
}

// Bytes serializes the private key (it stays in the cluster).
func (k Key) Bytes() ([]byte, error) { return k.priv.Bytes() }

// Public is the public key, serialized (what the agent announces).
func (k Key) Public() []byte { return k.priv.PublicKey().Bytes() }

// ID names the public key.
func (k Key) ID() string { return KeyID(k.Public()) }

// KeyID names a public key: base64url(sha256(public)), first 16 chars.
func KeyID(public []byte) string {
	sum := sha256.Sum256(public)
	return base64.RawURLEncoding.EncodeToString(sum[:])[:16]
}

// Target is what a sealed value is for. Info binds it: a value sealed for
// one secret, version, app, environment or cluster opens for no other (so
// not even the control plane can redirect it to another app's Secret).
type Target struct {
	Cluster     string
	Environment string
	App         string
	Name        string
	Version     int64
}

// Info is the HPKE info string of t:
// "odicea/secret/v1" NUL cluster NUL environment NUL app NUL name NUL version.
func (t Target) Info() []byte {
	return []byte(strings.Join([]string{"odicea/secret/v1", t.Cluster, t.Environment, t.App, t.Name, strconv.FormatInt(t.Version, 10)}, "\x00"))
}

// Seal encrypts value for t to the public key (the console does the same in
// the browser; this one serves tests and tools).
func Seal(public []byte, t Target, value []byte) ([]byte, error) {
	if len(value) > MaxValue {
		return nil, errors.New("secretbox: value too long")
	}
	pk, err := kem.NewPublicKey(public)
	if err != nil {
		return nil, errors.New("secretbox: not a public key")
	}
	return hpke.Seal(pk, hpke.HKDFSHA256(), hpke.AES256GCM(), t.Info(), value)
}

// ErrOpen is any failure to open: wrong key, wrong target, tampered bytes.
var ErrOpen = errors.New("secretbox: cannot open (sealed for another key or secret, or altered)")

// Open decrypts what Seal made for t.
func (k Key) Open(t Target, sealed []byte) ([]byte, error) {
	if len(sealed) > MaxSealed {
		return nil, ErrOpen
	}
	out, err := hpke.Open(k.priv, hpke.HKDFSHA256(), hpke.AES256GCM(), t.Info(), sealed)
	if err != nil {
		return nil, ErrOpen
	}
	return out, nil
}

// ValidPublic reports whether b is a public key of the suite.
func ValidPublic(b []byte) bool {
	_, err := kem.NewPublicKey(b)
	return err == nil
}

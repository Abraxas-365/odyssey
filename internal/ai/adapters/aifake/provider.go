// Package aifake is an ai.Provider in memory, for tests and local
// development without a gateway. Its keys are random and work nowhere.
package aifake

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
	"sync"

	"github.com/Abraxas-365/odyssey/internal/ai"
	"github.com/Abraxas-365/odyssey/internal/errx"
)

type key struct {
	id, name, secret string
	revoked          bool
}

type Provider struct {
	mu      sync.Mutex
	keys    map[string]*key
	seq     int
	down    error
	failing map[string]error
}

func New() *Provider { return &Provider{keys: map[string]*key{}, failing: map[string]error{}} }

var _ ai.Provider = (*Provider)(nil)

// Down makes every call fail with err (nil: back up).
func (p *Provider) Down(err error) { p.mu.Lock(); p.down = err; p.mu.Unlock() }

// FailOnce makes the next call of op ("create", "revoke" or "list") fail with
// err; "created" makes the next create succeed at the gateway and still fail
// for the caller (a crash before the key was saved).
func (p *Provider) FailOnce(op string, err error) { p.mu.Lock(); p.failing[op] = err; p.mu.Unlock() }

func (p *Provider) check(op string) error {
	if p.down != nil {
		return p.down
	}
	if err, ok := p.failing[op]; ok {
		delete(p.failing, op)
		return err
	}
	return nil
}

func (p *Provider) CreateKey(_ context.Context, name string) (ai.ProviderKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check("create"); err != nil {
		return ai.ProviderKey{}, err
	}
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	p.seq++
	k := &key{id: "sa-" + hex.EncodeToString(raw[:6]), name: name, secret: "ik_svc_fake_" + hex.EncodeToString(raw)}
	p.keys[k.id] = k
	if err, ok := p.failing["created"]; ok { // the gateway made the key, the caller never heard
		delete(p.failing, "created")
		return ai.ProviderKey{}, err
	}
	return ai.ProviderKey{ID: k.id, Secret: k.secret}, nil
}

func (p *Provider) RevokeKey(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check("revoke"); err != nil {
		return err
	}
	if k, ok := p.keys[id]; ok {
		k.revoked = true
	}
	return nil
}

func (p *Provider) ListKeys(_ context.Context, prefix string) ([]ai.KeyInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check("list"); err != nil {
		return nil, err
	}
	out := []ai.KeyInfo{}
	for _, k := range p.keys {
		if !k.revoked && strings.HasPrefix(k.name, prefix) {
			out = append(out, ai.KeyInfo{ID: k.id, Name: k.name})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Live returns the names of the keys not revoked.
func (p *Provider) Live() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []string{}
	for _, k := range p.keys {
		if !k.revoked {
			out = append(out, k.name)
		}
	}
	sort.Strings(out)
	return out
}

// Secret is the secret of the key called name (live or not); "" when unknown.
func (p *Provider) Secret(name string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range p.keys {
		if k.name == name {
			return k.secret
		}
	}
	return ""
}

// Valid reports whether secret is a live key (what the gateway would accept).
func (p *Provider) Valid(secret string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range p.keys {
		if k.secret == secret && !k.revoked {
			return true
		}
	}
	return false
}

// Unavailable is the error a gateway that is unreachable gives.
func Unavailable() error { return errx.External("the fake AI gateway is down") }

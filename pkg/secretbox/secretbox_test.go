package secretbox_test

import (
	"bytes"
	"testing"

	"github.com/Abraxas-365/odyssey/pkg/secretbox"
)

func TestSealOpensOnlyForItsTarget(t *testing.T) {
	k, err := secretbox.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	target := secretbox.Target{Cluster: "c1", Environment: "e1", App: "portal", Name: "API_KEY", Version: 3}
	sealed, err := secretbox.Seal(k.Public(), target, []byte("s3cr3t"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := k.Open(target, sealed)
	if err != nil || string(got) != "s3cr3t" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	for name, other := range map[string]secretbox.Target{
		"cluster":     {Cluster: "c2", Environment: "e1", App: "portal", Name: "API_KEY", Version: 3},
		"environment": {Cluster: "c1", Environment: "e2", App: "portal", Name: "API_KEY", Version: 3},
		"app":         {Cluster: "c1", Environment: "e1", App: "other", Name: "API_KEY", Version: 3},
		"name":        {Cluster: "c1", Environment: "e1", App: "portal", Name: "OTHER", Version: 3},
		"version":     {Cluster: "c1", Environment: "e1", App: "portal", Name: "API_KEY", Version: 2},
		// NUL separators: "a\x00b" + "c" must not equal "a" + "b\x00c"
		"shifted": {Cluster: "c1", Environment: "e1\x00portal", App: "API_KEY", Name: "", Version: 3},
	} {
		if _, err := k.Open(other, sealed); err != secretbox.ErrOpen {
			t.Errorf("%s: opened for another target (err %v)", name, err)
		}
	}
	other, _ := secretbox.GenerateKey()
	if _, err := other.Open(target, sealed); err != secretbox.ErrOpen {
		t.Errorf("another key opened it: %v", err)
	}
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1
	if _, err := k.Open(target, tampered); err != secretbox.ErrOpen {
		t.Errorf("tampered bytes opened: %v", err)
	}
}

func TestKeyRoundTripAndID(t *testing.T) {
	k, _ := secretbox.GenerateKey()
	raw, err := k.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	back, err := secretbox.ParseKey(raw)
	if err != nil || !bytes.Equal(back.Public(), k.Public()) || back.ID() != k.ID() {
		t.Fatalf("round trip lost the key: %v", err)
	}
	if len(k.ID()) != 16 || !secretbox.ValidPublic(k.Public()) || secretbox.ValidPublic([]byte("short")) {
		t.Fatalf("id %q / ValidPublic wrong", k.ID())
	}
	if _, err := secretbox.ParseKey([]byte("nope")); err == nil {
		t.Fatal("ParseKey accepted garbage")
	}
}

func TestLimits(t *testing.T) {
	k, _ := secretbox.GenerateKey()
	target := secretbox.Target{Cluster: "c", Environment: "e", Name: "N", Version: 1}
	if _, err := secretbox.Seal(k.Public(), target, make([]byte, secretbox.MaxValue+1)); err == nil {
		t.Fatal("sealed a value over MaxValue")
	}
	big, err := secretbox.Seal(k.Public(), target, make([]byte, secretbox.MaxValue))
	if err != nil || len(big) != secretbox.MaxSealed {
		t.Fatalf("max value sealed to %d bytes (want %d): %v", len(big), secretbox.MaxSealed, err)
	}
	if _, err := k.Open(target, append(big, 0)); err != secretbox.ErrOpen {
		t.Fatal("opened more than MaxSealed")
	}
}

// TestVector pins the wire format against a fixed key and sealed value made
// by the console's implementation (frontend/src/lib/secretbox.test.ts makes
// the same check from the other side), so the two cannot drift apart.
func TestVector(t *testing.T) {
	k, err := secretbox.ParseKey(vectorKey)
	if err != nil {
		t.Fatal(err)
	}
	got, err := k.Open(vectorTarget, vectorSealed)
	if err != nil || string(got) != vectorValue {
		t.Fatalf("vector did not open: %q %v", got, err)
	}
}

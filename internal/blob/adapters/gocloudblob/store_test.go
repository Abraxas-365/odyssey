package gocloudblob_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/blob"
	"github.com/Abraxas-365/odyssey/internal/blob/adapters/gocloudblob"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// TestContract runs the blob.Store contract on every backend the tests can
// reach without a network: memory and disk.
func TestContract(t *testing.T) {
	for name, url := range map[string]string{
		"mem":  "mem://",
		"file": "file://" + t.TempDir() + "?create_dir=1&no_tmp_dir=1",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, err := gocloudblob.Open(ctx, url)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			contract(t, s)
		})
	}
}

func contract(t *testing.T, s blob.Store) {
	ctx := context.Background()
	key := blob.TenantKey(identity.NewOrganizationID(), "sources", identity.NewOrganizationID().String())
	opts := blob.PutOptions{ContentType: "application/gzip"}

	if err := s.Put(ctx, key, strings.NewReader("hola"), opts); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Exists(ctx, key); err != nil || !ok {
		t.Fatalf("exists = %v, %v", ok, err)
	}
	if got := read(t, s, key); got != "hola" {
		t.Fatalf("read %q", got)
	}
	for offset, want := range map[int64]string{0: "hola", 2: "la", 4: "", 9: ""} {
		r, err := s.OpenAt(ctx, key, offset)
		if err != nil {
			t.Fatalf("open at %d: %v", offset, err)
		}
		b, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil || string(b) != want {
			t.Fatalf("open at %d: %q, %v", offset, b, err)
		}
	}
	if _, err := s.OpenAt(ctx, key, -1); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("negative offset: %v", err)
	}
	if _, err := s.OpenAt(ctx, key+"-missing", 2); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("missing at offset: %v", err)
	}
	// Immutable: a second Put is a Conflict and does not change the object.
	if err := s.Put(ctx, key, strings.NewReader("chau"), opts); !errx.IsType(err, errx.TypeConflict) {
		t.Fatalf("overwrite: %v", err)
	}
	if got := read(t, s, key); got != "hola" {
		t.Fatalf("overwritten: %q", got)
	}

	// Concurrent Puts of a fresh key: exactly one wins.
	race := key + "-race"
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.Put(ctx, race, bytes.NewReader([]byte{byte('a' + i)}), opts) == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d concurrent Puts won", wins)
	}

	// A failing reader leaves nothing behind, and its errx verdict comes back as is.
	broken := key + "-broken"
	verdict := errx.Validation("the archive is not a gzip file")
	err := s.Put(ctx, broken, io.MultiReader(strings.NewReader("partial"), errReader{verdict}), opts)
	if !errors.Is(err, verdict) {
		t.Fatalf("broken reader: %v", err)
	}
	if ok, _ := s.Exists(ctx, broken); ok {
		t.Fatal("a failed Put left an object")
	}

	if _, err := s.Open(ctx, key+"-missing"); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("delete twice: %v", err)
	}
	if ok, _ := s.Exists(ctx, key); ok {
		t.Fatal("deleted object still exists")
	}
	for _, bad := range []string{"", "/a", "a/", "a//b", "a/../b", "a\\b"} {
		if err := s.Put(ctx, bad, strings.NewReader("x"), opts); !errx.IsType(err, errx.TypeValidation) {
			t.Errorf("key %q: %v", bad, err)
		}
	}
	if err := s.Put(ctx, key+"-ct", strings.NewReader("x"), blob.PutOptions{}); !errx.IsType(err, errx.TypeValidation) {
		t.Errorf("no content type: %v", err)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func read(t *testing.T, s blob.Store, key string) string {
	t.Helper()
	r, err := s.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

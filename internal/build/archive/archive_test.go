package archive_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"testing"

	"github.com/Abraxas-365/odyssey/internal/build/archive"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/pkg/srcarchive"
)

func tgz(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func read(data []byte, l archive.Limits) (archive.Summary, error) {
	v := archive.NewVerifier(bytes.NewReader(data), l)
	defer v.Close()
	_, err := io.Copy(io.Discard, v)
	return v.Summary(), err
}

// The shared rules (pkg/srcarchive) are tested there; this checks how the
// API words them.
func TestPlatformLimitsAreTheBuilders(t *testing.T) {
	if archive.Default() != srcarchive.Platform() {
		t.Fatalf("the upload's limits %+v differ from the builder's %+v", archive.Default(), srcarchive.Platform())
	}
}

func TestRefusalsAreAPIErrors(t *testing.T) {
	limits := archive.Limits{Compressed: 1 << 20, Unpacked: 1 << 20, Files: 10}

	sum, err := read(tgz(t, "go.mod", "module x\n"), limits)
	if err != nil || sum.Files != 1 {
		t.Fatalf("valid archive: %+v %v", sum, err)
	}

	_, err = read(tgz(t, "../escape", "x"), limits)
	var e *errx.Error
	if !errors.As(err, &e) || e.Type != errx.TypeValidation || e.Code != archive.CodeInvalid || e.Details["field"] != "source" {
		t.Fatalf("invalid archive: %#v", err)
	}

	_, err = read(tgz(t, "big", string(make([]byte, 64<<10))), archive.Limits{Compressed: 16, Unpacked: 1 << 20, Files: 10})
	if !errors.As(err, &e) || e.HTTPStatus != 413 || e.Code != archive.CodeInvalid {
		t.Fatalf("too large: %#v", err)
	}

	cut := errors.New("connection reset")
	v := archive.NewVerifier(io.MultiReader(bytes.NewReader([]byte{0x1f}), errReader{cut}), limits)
	defer v.Close()
	if _, err := io.ReadAll(v); !errors.Is(err, cut) {
		t.Fatalf("reader error: %v", err)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// Package archive applies the shared source rules (pkg/srcarchive, also
// used by the cluster's builder) with the platform's limits, and words their
// refusals as the API's errors: SOURCE_INVALID on field "source", 413 over
// the compressed limit.
package archive

import (
	"errors"
	"io"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/pkg/srcarchive"
)

// Limits bound an archive.
type Limits = srcarchive.Limits

// Summary is what a verified archive holds.
type Summary = srcarchive.Summary

// Default are the platform's limits.
func Default() Limits {
	return Limits{Compressed: build.MaxSourceBytes, Unpacked: build.MaxUnpackedBytes, Files: build.MaxSourceFiles}
}

// CodeInvalid is the code of every refusal (the console words it).
const CodeInvalid = build.CodeSourceInvalid

// Verifier passes an archive through while checking it (srcarchive.Verifier),
// with refusals as API errors.
type Verifier struct{ v *srcarchive.Verifier }

// NewVerifier checks r against limits as it is read.
func NewVerifier(r io.Reader, limits Limits) *Verifier {
	return &Verifier{v: srcarchive.NewVerifier(r, limits)}
}

func (v *Verifier) Read(p []byte) (int, error) {
	n, err := v.v.Read(p)
	return n, translate(err)
}

// Summary is the verified archive's summary; valid once Read returned io.EOF.
func (v *Verifier) Summary() Summary { return v.v.Summary() }

// Close stops the checker (a no-op after the end of the stream).
func (v *Verifier) Close() error { return v.v.Close() }

// translate words the shared refusals as API errors; other errors (the
// reader's, io.EOF) pass as they are.
func translate(err error) error {
	var bad *srcarchive.Invalid
	var big *srcarchive.TooLarge
	switch {
	case errors.As(err, &bad):
		return errx.Validation(bad.Message).WithCode(CodeInvalid).WithField("source")
	case errors.As(err, &big):
		return errx.PayloadTooLarge(big.Error()).WithCode(CodeInvalid)
	}
	return err
}

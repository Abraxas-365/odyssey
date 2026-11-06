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

// Subtree writes to w the source of the folder root of the archive read
// from r (srcarchive.Subtree). It runs on a stored source, so every refusal
// is a build's, a Business error with its code: a folder that is not there
// is build.CodeRootMissing; one that breaks a rule or is too large,
// build.CodeSourceInvalid. Other errors (the reader's, w's) pass as they are.
func Subtree(r io.Reader, w io.Writer, root string, limits Limits) (Summary, error) {
	sum, err := srcarchive.Subtree(r, w, root, limits)
	var bad *srcarchive.Invalid
	var big *srcarchive.TooLarge
	switch {
	case errors.Is(err, srcarchive.ErrRootMissing):
		return Summary{}, build.Refused(build.CodeRootMissing, "the folder "+root+" is not in the code")
	case errors.As(err, &bad):
		return Summary{}, build.Refused(build.CodeSourceInvalid, bad.Message)
	case errors.As(err, &big):
		return Summary{}, build.Refused(build.CodeSourceInvalid, "the folder "+root+": "+big.Error())
	}
	return sum, err
}

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

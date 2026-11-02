package sourcesvc

import (
	"errors"
	"io"

	"github.com/Abraxas-365/odyssey/internal/build"
	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
	"github.com/Abraxas-365/odyssey/internal/release"
	"github.com/Abraxas-365/odyssey/internal/source"
	"github.com/Abraxas-365/odyssey/internal/source/tarball"
	"github.com/Abraxas-365/odyssey/internal/workflow"
	"github.com/Abraxas-365/odyssey/pkg/srcarchive"
)

// repack is tarball.Repack with its refusals worded as source failures.
func repack(r io.Reader, w io.Writer, root string, limits srcarchive.Limits) (tarball.Summary, error) {
	sum, err := tarball.Repack(r, w, root, limits)
	var bad *srcarchive.Invalid
	var big *srcarchive.TooLarge
	switch {
	case err == nil:
		return sum, nil
	case errors.Is(err, tarball.ErrRootMissing):
		return sum, errx.Wrap(err, source.Message(source.CodeRootMissing), errx.TypeBusiness).WithCode(source.CodeRootMissing)
	case errors.As(err, &big):
		return sum, errx.Wrap(err, source.Message(source.CodeTooLarge), errx.TypeBusiness).WithCode(source.CodeTooLarge)
	case errors.As(err, &bad):
		return sum, errx.Wrap(err, source.Message(source.CodeImportFailed), errx.TypeBusiness).WithCode(source.CodeImportFailed)
	}
	return sum, err
}

// release_ is the deployment request of an imported source with the
// connection's configuration.
func release_(src build.SourceID, c source.Connection, by identity.UserID) release.DeployUploaded {
	return release.DeployUploaded{Source: src, Config: c.Config, RequestedBy: by}.Normalize()
}

// retryable sorts an import error: a refusal (the commit, the folder, the
// access) fails the run at once; anything else (the provider down, a
// network error, a full quota that may drain) is retried.
func retryable(err error) error {
	if refused(err) {
		return workflow.Fail(err)
	}
	return err
}

// refused reports a definitive answer: retrying would get the same one.
// Conflicts are not: Postgres reports deadlocks and serialization failures
// as Conflict, and a persistent one still ends when the attempts run out.
func refused(err error) bool {
	var e *errx.Error
	if !errors.As(err, &e) {
		return false
	}
	if e.Code == build.CodeSourceQuota { // older sources expire meanwhile
		return false
	}
	switch e.Type {
	case errx.TypeValidation, errx.TypeNotFound, errx.TypeBusiness, errx.TypeAuthorization:
		return true
	}
	return false
}

// failureOf words why a push was not published. Its detail is only text
// written for whoever publishes (the archive's refusal, the deployment's
// refusal): never a transport, provider or database error, which the
// caller logs instead.
func failureOf(cause error) source.Failure {
	var e *errx.Error
	if !errors.As(cause, &e) {
		return source.FailureOf(source.CodeInternal, "")
	}
	switch {
	case e.Code == build.CodeSourceInvalid:
		return source.FailureOf(source.CodeImportFailed, e.Message)
	case e.Code == build.CodeSourceQuota, e.Code == source.CodeDeployRefused:
		return source.FailureOf(source.CodeDeployRefused, e.Message)
	case source.Known(e.Code):
		return source.FailureOf(e.Code, archiveDetail(cause))
	case e.Type == errx.TypeExternal:
		return source.FailureOf(source.CodeProviderDown, "")
	case e.Type == errx.TypeInternal:
		return source.FailureOf(source.CodeInternal, "")
	}
	// a refusal of another domain (release, build): its message is for users
	return source.FailureOf(source.CodeDeployRefused, e.Message)
}

// archiveDetail is the repack's own words about the commit, if that was
// the cause.
func archiveDetail(cause error) string {
	var bad *srcarchive.Invalid
	var big *srcarchive.TooLarge
	switch {
	case errors.As(cause, &bad):
		return bad.Message
	case errors.As(cause, &big):
		return big.Error()
	case errors.Is(cause, tarball.ErrRootMissing):
		return tarball.ErrRootMissing.Error()
	}
	return ""
}

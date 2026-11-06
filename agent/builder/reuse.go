package builder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// ReusedFile is the marker, in the work volume, that the build's image is
// an earlier build's: it holds that image's digest. reuse writes it; prepare
// (fetches nothing) and build (builds nothing) look for it.
const ReusedFile = "reused"

// ReuseOptions configure reuse (from the Job's environment).
type ReuseOptions struct {
	// Image is the earlier build's: <repository>@sha256:<digest>.
	Image string
	// Target is this build's image: <repository>:<tag>.
	Target string
	// Insecure talks to the registry over plain HTTP.
	Insecure bool
	Work     string
	// Out receives progress lines (the pod's log).
	Out io.Writer
	// Head asks the registry whether the manifest of ref is there (default:
	// HEAD, with the credentials of DOCKER_CONFIG). A missing manifest is
	// an error for which IsMissing is true.
	Head func(ctx context.Context, ref name.Digest) (v1.Hash, error)
	// Tag gives the manifest of src another name, dst, in the same
	// repository (default: GET then PUT of the manifest: no blob moves).
	Tag func(ctx context.Context, src name.Digest, dst name.Tag) error
}

func reuseFromEnv() ReuseOptions {
	return ReuseOptions{
		Image: os.Getenv("ODYSSEY_REUSE_IMAGE"), Target: os.Getenv("ODYSSEY_IMAGE"), Work: os.Getenv("ODYSSEY_WORK"),
		Insecure: os.Getenv("ODYSSEY_REGISTRY_INSECURE") == "true", Out: os.Stdout,
	}
}

func registryOptions(ctx context.Context) []remote.Option {
	return []remote.Option{remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain)}
}

func nameOptions(insecure bool) []name.Option {
	if insecure {
		return []name.Option{name.Insecure}
	}
	return nil
}

func defaultHead(ctx context.Context, ref name.Digest) (v1.Hash, error) {
	d, err := remote.Head(ref, registryOptions(ctx)...)
	if err != nil {
		return v1.Hash{}, err
	}
	return d.Digest, nil
}

func defaultTag(ctx context.Context, src name.Digest, dst name.Tag) error {
	opts := registryOptions(ctx)
	d, err := remote.Get(src, opts...)
	if err != nil {
		return err
	}
	return remote.Tag(dst, d, opts...)
}

// IsMissing tells a registry that does not have the manifest (or the
// repository) from any other failure.
func IsMissing(err error) bool {
	var te *transport.Error
	if !errors.As(err, &te) {
		return false
	}
	if te.StatusCode == http.StatusNotFound {
		return true
	}
	for _, d := range te.Errors {
		if d.Code == transport.ManifestUnknownErrorCode || d.Code == transport.NameUnknownErrorCode {
			return true
		}
	}
	return false
}

// Reuse gives this build the image of an earlier build, when the registry
// still has it: the image is tagged as this build's (nothing is built or
// pushed but a manifest) and the marker is written for prepare and build. An
// image that is gone, a registry that fails and a reference that does not
// parse are not failures: the build goes on the long way (an empty Result,
// no marker, the reason in the log). Only what should never happen (the work
// volume cannot be written) fails.
func Reuse(ctx context.Context, o ReuseOptions) Result {
	if o.Head == nil {
		o.Head = defaultHead
	}
	if o.Tag == nil {
		o.Tag = defaultTag
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	say := func(format string, a ...any) { fmt.Fprintf(o.Out, format+"\n", a...) }
	building := func(why string) Result {
		say("%s: building", why)
		return Result{}
	}
	nopts := nameOptions(o.Insecure)
	src, err := name.NewDigest(o.Image, nopts...)
	if err != nil {
		return building("The earlier image is not a valid reference (" + err.Error() + ")")
	}
	dst, err := name.NewTag(o.Target, nopts...)
	if err != nil {
		return building("This build's image is not a valid reference (" + err.Error() + ")")
	}
	if src.Context().String() != dst.Context().String() {
		return building("The earlier image is not in this build's repository")
	}
	digest, err := o.Head(ctx, src)
	switch {
	case err == nil:
	case IsMissing(err):
		return building("The earlier image is gone")
	default:
		return building("Could not look for the earlier image (" + err.Error() + ")")
	}
	if want := src.DigestStr(); digest.String() != want {
		return building(fmt.Sprintf("The registry's earlier image is %s, not %s", digest, want))
	}
	if err := o.Tag(ctx, src, dst); err != nil {
		if IsMissing(err) {
			return building("The earlier image is gone")
		}
		return building("Could not tag the earlier image (" + err.Error() + ")")
	}
	if err := os.WriteFile(filepath.Join(o.Work, ReusedFile), []byte(digest.String()), 0o644); err != nil {
		return Result{Reason: ReasonFailed, Message: "write the reuse marker: " + err.Error()}
	}
	say("Reusing the image of an earlier build: %s", digest)
	return Result{Digest: digest.String(), Reused: true}
}

// reusedDigest is the digest in the work volume's marker ("" when the build
// does not reuse an image).
func reusedDigest(work string) string {
	b, err := os.ReadFile(filepath.Join(work, ReusedFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// runReuse is `odyssey-agent builder reuse`.
func runReuse(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return Reuse(ctx, reuseFromEnv())
}

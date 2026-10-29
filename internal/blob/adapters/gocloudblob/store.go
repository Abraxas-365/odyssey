// Package gocloudblob implements blob.Store on gocloud.dev/blob, so one
// adapter covers every backend, selected by URL:
//
//	s3://bucket?region=us-east-1                                                AWS S3
//	s3://bucket?endpoint=http://minio:9000&use_path_style=true&region=us-east-1 S3-compatible
//	file:///var/lib/odyssey/blobs?create_dir=1&no_tmp_dir=1                      local disk (one node)
//	mem://                                                                      in memory (tests)
//
// Credentials come from each provider's standard environment
// (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY, ...), never from the URL.
package gocloudblob

import (
	"context"
	"hash/fnv"
	"io"
	"net/url"
	"sync"

	"github.com/Abraxas-365/odyssey/internal/blob"
	"github.com/Abraxas-365/odyssey/internal/errx"
	gcblob "gocloud.dev/blob"
	_ "gocloud.dev/blob/fileblob" // file://
	_ "gocloud.dev/blob/memblob"  // mem://
	_ "gocloud.dev/blob/s3blob"   // s3://
	"gocloud.dev/gcerrors"
)

type Store struct {
	bucket *gcblob.Bucket
	// local is set for file:// only: fileblob's IfNotExist is a stat then a
	// rename under a per-writer mutex, so two Puts of one key could both
	// win. Put serialises per key and checks first (file:// is single-node).
	local *stripes
}

var _ blob.Store = (*Store)(nil)

type stripes [64]sync.Mutex

func (l *stripes) lock(key string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	m := &l[h.Sum32()%uint32(len(l))]
	m.Lock()
	return m.Unlock
}

// Open opens the bucket named by rawURL and checks it is reachable, so a bad
// configuration fails at boot instead of on the first upload.
func Open(ctx context.Context, rawURL string) (*Store, error) {
	scheme := "unknown"
	if u, err := url.Parse(rawURL); err == nil {
		scheme = u.Scheme
	}
	bucket, err := gcblob.OpenBucket(ctx, rawURL)
	if err != nil {
		// The URL is not echoed: it may carry account names or endpoints.
		return nil, errx.Wrap(err, "open blob bucket ("+scheme+")", errx.TypeInternal)
	}
	ok, err := bucket.IsAccessible(ctx)
	if err != nil || !ok {
		_ = bucket.Close()
		if err == nil {
			return nil, errx.External("blob bucket (" + scheme + ") does not exist or is not accessible")
		}
		return nil, errx.Wrap(err, "blob bucket ("+scheme+") is not accessible", errx.TypeExternal)
	}
	s := &Store{bucket: bucket}
	if scheme == "file" {
		s.local = &stripes{}
	}
	return s, nil
}

func (s *Store) Close() error { return s.bucket.Close() }

func (s *Store) Put(ctx context.Context, key string, r io.Reader, opts blob.PutOptions) error {
	if err := blob.ValidateKey(key); err != nil {
		return err
	}
	if opts.ContentType == "" {
		return errx.Validation("blob content type is required")
	}
	if s.local != nil {
		defer s.local.lock(key)()
		exists, err := s.bucket.Exists(ctx, key)
		if err != nil {
			return failure(err, "write blob")
		}
		if exists {
			return errx.Conflict("blob already exists")
		}
	}
	// Cancelling the writer's context before Close aborts the upload, so a
	// failed copy never leaves a partial object.
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w, err := s.bucket.NewWriter(wctx, key, &gcblob.WriterOptions{
		ContentType: opts.ContentType, Metadata: opts.Metadata, IfNotExist: true,
	})
	if err != nil {
		return failure(err, "write blob")
	}
	if _, err := io.Copy(w, r); err != nil {
		cancel()
		_ = w.Close()
		var e *errx.Error
		if errx.As(err, &e) {
			return err // the reader's own verdict (a validating reader says why)
		}
		return errx.Wrap(err, "write blob", errx.TypeInternal)
	}
	if err := w.Close(); err != nil {
		return failure(err, "write blob")
	}
	return nil
}

func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.OpenAt(ctx, key, 0)
}

func (s *Store) OpenAt(ctx context.Context, key string, offset int64) (io.ReadCloser, error) {
	if err := blob.ValidateKey(key); err != nil {
		return nil, err
	}
	if offset < 0 {
		return nil, errx.Validation("blob offset must be 0 or more")
	}
	r, err := s.bucket.NewRangeReader(ctx, key, offset, -1, nil)
	if err != nil {
		return nil, failure(err, "read blob")
	}
	return r, nil
}

func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	if err := blob.ValidateKey(key); err != nil {
		return false, err
	}
	ok, err := s.bucket.Exists(ctx, key)
	if err != nil {
		return false, failure(err, "stat blob")
	}
	return ok, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	if err := blob.ValidateKey(key); err != nil {
		return err
	}
	if err := s.bucket.Delete(ctx, key); err != nil && gcerrors.Code(err) != gcerrors.NotFound {
		return failure(err, "delete blob")
	}
	return nil
}

// failure maps gocloud error codes to errx; driver errors never escape raw.
func failure(err error, op string) error {
	switch gcerrors.Code(err) {
	case gcerrors.NotFound:
		return errx.NotFound("blob not found")
	case gcerrors.AlreadyExists, gcerrors.FailedPrecondition:
		return errx.Conflict("blob already exists")
	case gcerrors.InvalidArgument:
		return errx.Wrap(err, op+": invalid argument", errx.TypeValidation)
	case gcerrors.PermissionDenied:
		return errx.Wrap(err, op+": permission denied by the storage backend", errx.TypeExternal)
	case gcerrors.Canceled, gcerrors.DeadlineExceeded:
		return errx.Wrap(err, op+": cancelled", errx.TypeExternal)
	}
	return errx.Wrap(err, op, errx.TypeExternal)
}

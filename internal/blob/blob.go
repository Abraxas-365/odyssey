// Package blob is the port for binary object storage (uploaded sources,
// archived build logs). Adapters live in blob/adapters; domains depend only
// on Store, and only the composition root opens a backend.
//
// Objects are immutable: Put never overwrites, so a stored object cannot be
// silently replaced. Keys are derived from typed IDs (TenantKey), never from
// client input, and Postgres is the index of what exists.
package blob

import (
	"context"
	"io"
	"strings"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/identity"
)

// Store is implemented by every storage backend (S3, Azure, disk, memory).
//
// Errors are *errx.Error: NotFound when the key does not exist, Conflict when
// Put targets an existing key, Validation for a malformed key.
type Store interface {
	// Put streams r under key. It fails with Conflict if key exists, and a
	// failed copy (r's error, a cancelled ctx) leaves no object behind.
	Put(ctx context.Context, key string, r io.Reader, opts PutOptions) error
	// Open opens the object for reading; the caller must Close it.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// OpenAt is Open from byte offset on (past the end reads nothing).
	OpenAt(ctx context.Context, key string, offset int64) (io.ReadCloser, error)
	// Exists reports whether key holds an object.
	Exists(ctx context.Context, key string) (bool, error)
	// Delete removes the object; deleting a missing key is not an error
	// (sweeps run again after a crash).
	Delete(ctx context.Context, key string) error
}

// PutOptions describe a new object.
type PutOptions struct {
	ContentType string            // required
	Metadata    map[string]string // optional, small string pairs
}

// TenantKey builds a key scoped to an organization: "org/<org>/<parts...>".
// Domain code builds keys with it so tenants never share a prefix.
func TenantKey(organization identity.OrganizationID, parts ...string) string {
	return "org/" + organization.String() + "/" + strings.Join(parts, "/")
}

// ValidateKey rejects keys that could escape their prefix or behave
// differently across backends.
func ValidateKey(key string) error {
	switch {
	case key == "":
		return errx.Validation("blob key is empty")
	case len(key) > 1024:
		return errx.Validation("blob key is too long")
	case strings.HasPrefix(key, "/"), strings.HasSuffix(key, "/"):
		return errx.Validation("blob key must not start or end with '/'")
	case strings.ContainsAny(key, "\\\x00"):
		return errx.Validation("blob key has invalid characters")
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errx.Validation("blob key has an empty or relative segment")
		}
	}
	return nil
}

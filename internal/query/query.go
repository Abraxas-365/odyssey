// Package query owns transport-agnostic pagination types used by every
// domain's ports. It imports nothing from internal/.
package query

import "strings"

// Pagination holds limit/offset/search for paginated queries.
type Pagination struct {
	Limit  int
	Offset int
	Search string
}

const (
	DefaultLimit = 20
	MaxLimit     = 100
	MaxOffset    = 10000
)

// Normalize clamps Limit to [1, MaxLimit] and Offset to >= 0.
func (p Pagination) Normalize() Pagination {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	return p
}

// Page is the effective pagination applied plus the total match count.
type Page struct {
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// Paginated is the response envelope:
// {"items": [...], "page": {"total": N, "limit": N, "offset": N}}.
type Paginated[T any] struct {
	Items []T  `json:"items"`
	Page  Page `json:"page"`
}

// NewPaginated builds the envelope; Items is never nil.
func NewPaginated[T any](items []T, total int, p Pagination) Paginated[T] {
	if items == nil {
		items = []T{}
	}
	return Paginated[T]{Items: items, Page: Page{Total: total, Limit: p.Limit, Offset: p.Offset}}
}

// EscapeLike escapes LIKE metacharacters and wraps in % for a contains match.
// Returns "" for empty input — callers should skip the clause.
func EscapeLike(s string) string {
	if s == "" {
		return ""
	}
	return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s) + "%"
}

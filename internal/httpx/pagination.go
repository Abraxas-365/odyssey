package httpx

import (
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/query"
	"github.com/Abraxas-365/odyssey/internal/text"
	"github.com/gofiber/fiber/v2"
)

// Pagination reads ?limit=, ?offset= and ?search= (docs/api.md §4). Absent
// values use the defaults; a limit above query.MaxLimit is clamped; a
// non-numeric or non-positive limit, a negative or non-numeric offset, an
// offset above query.MaxOffset or an invalid search are 400s.
func Pagination(c *fiber.Ctx) (query.Pagination, error) {
	p := query.Pagination{}
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return p, errx.InvalidField("limit", "limit must be an integer of at least 1")
		}
		p.Limit = n
	}
	if raw := c.Query("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > query.MaxOffset {
			return p, errx.InvalidField("offset", "offset must be an integer between 0 and "+strconv.Itoa(query.MaxOffset))
		}
		p.Offset = n
	}
	if raw := strings.TrimSpace(c.Query("search")); raw != "" {
		if err := text.Optional("search", raw, text.MaxSearch); err != nil {
			return p, err
		}
		p.Search = raw
	}
	return p.Normalize(), nil
}

// QueryValues returns every value of a repeated query parameter
// (?status=a&status=b), without empty entries. Repeated values mean OR.
func QueryValues(c *fiber.Ctx, name string) []string {
	var out []string
	for _, v := range c.Context().QueryArgs().PeekMulti(name) {
		if s := strings.TrimSpace(string(v)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// EnumFilter reads a repeated enum query parameter and checks each value
// with valid; msg is the 400 text for a value outside the set.
func EnumFilter[T ~string](c *fiber.Ctx, name string, valid func(T) bool, msg string) ([]T, error) {
	var out []T
	for _, raw := range QueryValues(c, name) {
		v := T(raw)
		if !valid(v) {
			return nil, errx.InvalidField(name, msg)
		}
		out = append(out, v)
	}
	return out, nil
}

// TimeRange reads a pair of date filters (?from=&to=) as [from, before):
// each value is a date "2026-10-11" (a day in loc; to includes the whole
// day) or an RFC 3339 instant. Absent values are zero. A to that is not
// after from is a 400.
func TimeRange(c *fiber.Ctx, from, to string, loc *time.Location) (time.Time, time.Time, error) {
	start, _, err := instant(c, from, loc)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, day, err := instant(c, to, loc)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if day {
		end = end.AddDate(0, 0, 1)
	}
	if !start.IsZero() && !end.IsZero() && !end.After(start) {
		return time.Time{}, time.Time{}, errx.InvalidField(to, to+" must not be before "+from)
	}
	return start, end, nil
}

// instant parses one TimeRange value; day reports a plain date.
func instant(c *fiber.Ctx, name string, loc *time.Location) (t time.Time, day bool, err error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return time.Time{}, false, nil
	}
	if t, err := time.ParseInLocation(time.DateOnly, raw, loc); err == nil {
		return t, true, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, false, nil
	}
	return time.Time{}, false, errx.InvalidField(name, name+" must be a date (YYYY-MM-DD) or an RFC 3339 date-time")
}

// Created answers a create with 201, a Location header and the full
// resource. rel is the new resource's path below the collection the request
// was sent to ("<id>", "2/download"): Location is always the full request
// path (as mounted, e.g. /api/v1/core/risks) plus "/"+rel, so it never
// depends on where the router was mounted.
func Created(c *fiber.Ctx, rel string, body any) error {
	SetLocation(c, rel)
	return c.Status(fiber.StatusCreated).JSON(body)
}

// SetLocation sets Location to the request path plus "/"+rel (see Created).
func SetLocation(c *fiber.Ctx, rel string) {
	c.Set(fiber.HeaderLocation, strings.TrimRight(c.Path(), "/")+"/"+strings.TrimLeft(rel, "/"))
}

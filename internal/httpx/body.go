package httpx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
)

// MaxUploadBody is the largest multipart request any route takes (an upload
// route may take less): the source archive limit plus room for its form.
const MaxUploadBody = 101 << 20

// UploadReadTimeout is how long a streamed upload (multipart or raw) may take
// to arrive (the server's ReadTimeout is for everything else).
const UploadReadTimeout = 15 * time.Minute

// LimitBody (installed globally, before any handler) bounds every request
// body:
//   - a body that is neither multipart nor a raw upload is read whole here, at
//     most MaxJSONBody (413 beyond, by Content-Length before reading or by
//     counting if chunked), so handlers see a buffered body (Body);
//   - a multipart body, or a raw one (Content-Type application/gzip or
//     application/octet-stream: PUT /uploads/{ticket}), over MaxUploadBody by
//     Content-Length is a 413; under it, it stays a stream that only
//     NewUpload / StreamBody read, with its own cap, it may take
//     UploadReadTimeout to arrive, and the connection closes after the
//     response (its body may be unread).
//
// DecodeJSON refuses a streamed body before touching it.
func LimitBody(next Handler) Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if isStreamed(r) {
			w.Header().Set("Connection", "close")
			if r.ContentLength > MaxUploadBody {
				return tooLarge(w, MaxUploadBody)
			}
			// the server's Read/WriteTimeout are for small bodies; an upload may be slow
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Now().Add(UploadReadTimeout))
			_ = rc.SetWriteDeadline(time.Now().Add(UploadReadTimeout + time.Minute))
			return next(w, r)
		}
		if r.ContentLength > MaxJSONBody {
			return tooLarge(w, MaxJSONBody)
		}
		var body []byte
		if r.Body != nil && r.Body != http.NoBody {
			var err error
			body, err = io.ReadAll(io.LimitReader(r.Body, MaxJSONBody+1))
			if err != nil {
				w.Header().Set("Connection", "close")
				return errx.Validation("could not read the request body")
			}
			if len(body) > MaxJSONBody {
				return tooLarge(w, MaxJSONBody)
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), bodyKey{}, body))
		r.Body = io.NopCloser(bytes.NewReader(body))
		return next(w, r)
	}
}

type bodyKey struct{}

// Body is the request's buffered body (read by LimitBody; empty for a
// streamed upload, which only NewUpload / StreamBody read). Without
// LimitBody (a test router) it reads the body, at most MaxJSONBody.
func Body(r *http.Request) ([]byte, error) {
	if b, ok := r.Context().Value(bodyKey{}).([]byte); ok {
		return b, nil
	}
	if isStreamed(r) || r.Body == nil {
		return nil, nil
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, MaxJSONBody+1))
	if err != nil {
		return nil, errx.Validation("could not read the request body")
	}
	if len(b) > MaxJSONBody {
		return nil, errx.PayloadTooLarge("request body is too large (max 1 MiB)")
	}
	*r = *r.WithContext(context.WithValue(r.Context(), bodyKey{}, b))
	return b, nil
}

// tooLarge answers 413 and closes the connection: the unread rest of the
// body would otherwise have to be drained.
func tooLarge(w http.ResponseWriter, limit int64) error {
	w.Header().Set("Connection", "close")
	return errx.PayloadTooLarge("request body is too large (max " + mib(limit) + ")")
}

func mib(n int64) string { return itoa(n>>20) + " MiB" }

func isMultipart(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "multipart/form-data"
}

// Raw upload bodies: an archive sent as is (curl -T -).
const (
	mimeGzip   = "application/gzip"
	mimeStream = "application/octet-stream"
)

// isRaw reports an application/gzip or application/octet-stream Content-Type.
func isRaw(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && (mt == mimeGzip || mt == mimeStream)
}

// isStreamed reports a body LimitBody leaves as a stream.
func isStreamed(r *http.Request) bool { return isMultipart(r) || isRaw(r) }

// StreamBody is the request's raw body (application/gzip or
// application/octet-stream), at most limit bytes: a 415 for another
// Content-Type, a 413 by Content-Length up front or, when chunked, once the
// reader counts past limit (the error the reader returns). The body is never
// buffered; the response closes the connection.
func StreamBody(w http.ResponseWriter, r *http.Request, limit int64) (io.Reader, error) {
	if !isRaw(r) {
		return nil, errx.UnsupportedMediaType("Content-Type must be application/gzip")
	}
	if r.ContentLength > limit {
		return nil, tooLarge(w, limit)
	}
	return rawBody{r: &capped{r: r.Body, left: limit, limit: limit}}, nil
}

// rawBody words a broken stream; the cap's own verdict (a typed error) passes.
type rawBody struct{ r io.Reader }

func (b rawBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	var e *errx.Error
	if err != nil && !errors.Is(err, io.EOF) && !errors.As(err, &e) {
		return n, errx.Validation("could not read the request body")
	}
	return n, err
}

// Upload is a streamed multipart/form-data request body. Read its parts in
// order with Next; the body is cut at the limit given to NewUpload (a part
// read past it fails with a 413).
type Upload struct {
	reader *multipart.Reader
	body   *capped
}

// NewUpload opens the request's multipart body, at most limit bytes in all.
// The response closes the connection.
func NewUpload(w http.ResponseWriter, r *http.Request, limit int64) (*Upload, error) {
	if !isMultipart(r) {
		return nil, errx.UnsupportedMediaType("Content-Type must be multipart/form-data")
	}
	_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	boundary := params["boundary"]
	if boundary == "" {
		return nil, errx.Validation("multipart body has no boundary")
	}
	if r.ContentLength > limit {
		return nil, tooLarge(w, limit)
	}
	body := &capped{r: r.Body, left: limit, limit: limit}
	return &Upload{reader: multipart.NewReader(body, boundary), body: body}, nil
}

// Next returns the next part, or io.EOF after the last one.
func (u *Upload) Next() (*multipart.Part, error) {
	p, err := u.reader.NextPart()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, u.Failure(err)
	}
	return p, err
}

// ReadSmall reads a small part (a JSON field) whole, at most max bytes.
func (u *Upload) ReadSmall(p *multipart.Part, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(p, max+1))
	if err != nil {
		return nil, u.Failure(err)
	}
	if int64(len(b)) > max {
		return nil, errx.PayloadTooLarge("form field " + p.FormName() + " is too large (max " + itoa(max) + " bytes)")
	}
	return b, nil
}

// Failure words an error met reading a part: the body over its limit (413)
// or a reader's own typed verdict as is, anything else as a malformed body.
func (u *Upload) Failure(err error) error {
	var e *errx.Error
	if errx.As(err, &e) {
		return err
	}
	return errx.Validation("malformed multipart body")
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// capped fails with a 413 once more than limit bytes are read.
type capped struct {
	r     io.Reader
	left  int64
	limit int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		// One more byte decides: exactly at the limit is fine.
		var one [1]byte
		if n, _ := c.r.Read(one[:]); n > 0 {
			return 0, errx.PayloadTooLarge("request body is too large (max " + mib(c.limit) + ")")
		}
		return 0, io.EOF
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

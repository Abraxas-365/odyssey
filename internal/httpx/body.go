package httpx

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp"
)

// MaxUploadBody is the largest multipart request any route takes (an upload
// route may take less): the source archive limit plus room for its form.
const MaxUploadBody = 101 << 20

// The server streams request bodies (fiber StreamRequestBody) so an upload
// is never held in memory. Streaming also lifts fasthttp's own body limit and
// its guarantee that a body is read whole before the next request on the
// connection, so LimitBody (installed globally, first) restores both:
//   - a body that is neither multipart nor a raw upload is read whole here, at
//     most MaxJSONBody (413 beyond, by Content-Length before reading or by
//     counting if chunked), so handlers see a buffered body as before;
//   - a multipart body, or a raw one (Content-Type application/gzip or
//     application/octet-stream: PUT /uploads/{ticket}), over MaxUploadBody by
//     Content-Length is a 413; under it, it stays a stream that only
//     NewUpload / StreamBody read, with its own cap, and the connection
//     closes after the response (its body may be unread).
//
// DecodeJSON refuses a streamed body before touching it.
func LimitBody(c *fiber.Ctx) error {
	req := c.Request()
	n := req.Header.ContentLength()
	if isStreamed(c) {
		c.Context().SetConnectionClose()
		if n > MaxUploadBody {
			return tooLarge(c, MaxUploadBody)
		}
		return c.Next()
	}
	if n > MaxJSONBody {
		return tooLarge(c, MaxJSONBody)
	}
	if stream := c.Context().RequestBodyStream(); stream != nil {
		body, err := io.ReadAll(io.LimitReader(stream, MaxJSONBody+1))
		if err != nil {
			c.Context().SetConnectionClose()
			return errx.Validation("could not read the request body")
		}
		if len(body) > MaxJSONBody {
			return tooLarge(c, MaxJSONBody)
		}
		req.SetBodyRaw(body)
	}
	return c.Next()
}

// UploadReadTimeout is how long a streamed upload (multipart or raw) may take
// to arrive (the server's ReadTimeout is for everything else).
const UploadReadTimeout = 15 * time.Minute

// HeaderReceived gives streamed uploads (multipart or raw) UploadReadTimeout;
// install it as the fasthttp server's HeaderReceived.
func HeaderReceived(h *fasthttp.RequestHeader) fasthttp.RequestConfig {
	mt, _, err := mime.ParseMediaType(string(h.ContentType()))
	if err == nil && streamedType(mt) {
		return fasthttp.RequestConfig{ReadTimeout: UploadReadTimeout}
	}
	return fasthttp.RequestConfig{}
}

// tooLarge answers 413 and closes the connection: the unread rest of the
// body would otherwise be parsed as the next request.
func tooLarge(c *fiber.Ctx, limit int64) error {
	c.Context().SetConnectionClose()
	return errx.PayloadTooLarge("request body is too large (max " + mib(limit) + ")")
}

func mib(n int64) string { return itoa(n>>20) + " MiB" }

func isMultipart(c *fiber.Ctx) bool {
	mt, _, err := mime.ParseMediaType(c.Get(fiber.HeaderContentType))
	return err == nil && mt == fiber.MIMEMultipartForm
}

// Raw upload bodies: an archive sent as is (curl -T -).
const (
	mimeGzip   = "application/gzip"
	mimeStream = fiber.MIMEOctetStream
)

func streamedType(mt string) bool {
	return mt == fiber.MIMEMultipartForm || mt == mimeGzip || mt == mimeStream
}

// isRaw reports an application/gzip or application/octet-stream Content-Type.
func isRaw(c *fiber.Ctx) bool {
	mt, _, err := mime.ParseMediaType(c.Get(fiber.HeaderContentType))
	return err == nil && (mt == mimeGzip || mt == mimeStream)
}

// isStreamed reports a body LimitBody leaves as a stream.
func isStreamed(c *fiber.Ctx) bool { return isMultipart(c) || isRaw(c) }

// StreamBody is the request's raw body (application/gzip or
// application/octet-stream), at most limit bytes: a 415 for another
// Content-Type, a 413 by Content-Length up front or, when chunked, once the
// reader counts past limit (the error the reader returns). The body is never
// buffered; the response closes the connection unless it is read to its end.
func StreamBody(c *fiber.Ctx, limit int64) (io.Reader, error) {
	if !isRaw(c) {
		return nil, errx.UnsupportedMediaType("Content-Type must be application/gzip")
	}
	if n := int64(c.Request().Header.ContentLength()); n > limit {
		return nil, tooLarge(c, limit)
	}
	var src io.Reader = c.Context().RequestBodyStream()
	if src == nil { // not streamed (a body without length, or a test app)
		src = bytes.NewReader(c.Request().Body())
	}
	return rawBody{r: &capped{r: src, left: limit, limit: limit}}, nil
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
// The response closes the connection unless the body is read to its end.
func NewUpload(c *fiber.Ctx, limit int64) (*Upload, error) {
	if !isMultipart(c) {
		return nil, errx.UnsupportedMediaType("Content-Type must be multipart/form-data")
	}
	_, params, _ := mime.ParseMediaType(c.Get(fiber.HeaderContentType))
	boundary := params["boundary"]
	if boundary == "" {
		return nil, errx.Validation("multipart body has no boundary")
	}
	if n := int64(c.Request().Header.ContentLength()); n > limit {
		return nil, tooLarge(c, limit)
	}
	var src io.Reader = c.Context().RequestBodyStream()
	if src == nil { // not streamed (a body without length, or a test app)
		src = bytes.NewReader(c.Request().Body())
	}
	body := &capped{r: src, left: limit, limit: limit}
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

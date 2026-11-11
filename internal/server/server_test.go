package server_test

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/server"
)

// routes echo what a JSON route decoded and what an upload route streamed.
type routes struct{}

func (routes) Register(r *httpx.Router) {
	r.Post("/json", func(w http.ResponseWriter, r *http.Request) error {
		var v map[string]any
		if err := httpx.DecodeJSON(r, &v); err != nil {
			return err
		}
		return httpx.JSON(w, httpx.Map{"keys": len(v)})
	})
	r.Put("/raw", func(w http.ResponseWriter, r *http.Request) error {
		body, err := httpx.StreamBody(w, r, 2<<20)
		if err != nil {
			return err
		}
		n, err := io.Copy(io.Discard, body)
		if err != nil {
			return err
		}
		return httpx.JSON(w, httpx.Map{"bytes": n})
	})
	r.Put("/raw-sum", func(w http.ResponseWriter, r *http.Request) error {
		body, err := httpx.StreamBody(w, r, 2<<20)
		if err != nil {
			return err
		}
		h := sha256.New()
		if _, err := io.Copy(h, body); err != nil {
			return err
		}
		return httpx.JSON(w, httpx.Map{"sha256": hex.EncodeToString(h.Sum(nil))})
	})
	r.Post("/upload", func(w http.ResponseWriter, r *http.Request) error {
		u, err := httpx.NewUpload(w, r, 2<<20)
		if err != nil {
			return err
		}
		total := int64(0)
		for {
			p, err := u.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			n, err := io.Copy(io.Discard, p)
			total += n
			if err != nil {
				return u.Failure(err)
			}
		}
		return httpx.JSON(w, httpx.Map{"bytes": total})
	})
}

// listen serves the production app on a real socket (bodies stream as in
// production).
func listen(t *testing.T) string {
	t.Helper()
	app := server.New(func() error { return nil }, server.PublicConfig{}, server.Options{}, nil, routes{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := server.Serve(ln, app)
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String() + "/api/v1"
}

// post sends the request over a raw socket, writing while it reads: a
// server that answers before the body is sent (413) closes the connection,
// which a client that writes first sees only as a reset. length -1 sends
// the body chunked.
func post(t *testing.T, url, contentType string, body io.Reader, length int64) (int, string) {
	t.Helper()
	return send(t, http.MethodPost, url, contentType, body, length)
}

func send(t *testing.T, method, url, contentType string, body io.Reader, length int64) (int, string) {
	t.Helper()
	rest := strings.TrimPrefix(url, "http://")
	host, path, _ := strings.Cut(rest, "/")
	conn, err := net.DialTimeout("tcp", host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	go func() {
		w := bufio.NewWriter(conn)
		fmt.Fprintf(w, "%s /%s HTTP/1.1\r\nHost: x\r\nContent-Type: %s\r\n", method, path, contentType)
		if length < 0 {
			fmt.Fprintf(w, "Transfer-Encoding: chunked\r\n\r\n")
			buf := make([]byte, 32<<10)
			for {
				n, err := body.Read(buf)
				if n > 0 {
					fmt.Fprintf(w, "%x\r\n", n)
					_, _ = w.Write(buf[:n])
					_, _ = w.WriteString("\r\n")
				}
				if err != nil {
					break
				}
			}
			_, _ = w.WriteString("0\r\n\r\n")
		} else {
			fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", length)
			_, _ = io.Copy(w, body)
		}
		_ = w.Flush()
	}()
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(out)
}

func form(t *testing.T, size int) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("config", `{"port":8080}`); err != nil {
		t.Fatal(err)
	}
	f, err := w.CreateFormFile("source", "app.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(bytes.Repeat([]byte{'x'}, size)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

func TestBodies(t *testing.T) {
	base := listen(t)
	small := `{"a":1,"b":2}`
	if code, body := post(t, base+"/json", "application/json", strings.NewReader(small), int64(len(small))); code != 200 || !strings.Contains(body, `"keys":2`) {
		t.Fatalf("small json: %d %s", code, body)
	}
	if code, body := post(t, base+"/json", "application/json", strings.NewReader(small), -1); code != 200 || !strings.Contains(body, `"keys":2`) {
		t.Fatalf("chunked json: %d %s", code, body)
	}
	medium := `{"a":"` + strings.Repeat("x", 600<<10) + `"}` // well past one read buffer
	if code, body := post(t, base+"/json", "application/json", strings.NewReader(medium), int64(len(medium))); code != 200 || !strings.Contains(body, `"keys":1`) {
		t.Fatalf("medium json: %d %s", code, body)
	}
	big := `{"a":"` + strings.Repeat("x", httpx.MaxJSONBody) + `"}`
	if code, _ := post(t, base+"/json", "application/json", strings.NewReader(big), int64(len(big))); code != 413 {
		t.Fatalf("big json: %d", code)
	}
	if code, _ := post(t, base+"/json", "application/json", strings.NewReader(big), -1); code != 413 {
		t.Fatalf("big chunked json: %d", code)
	}

	data, ct := form(t, 1<<20)
	if code, body := post(t, base+"/upload", ct, bytes.NewReader(data), int64(len(data))); code != 200 || !strings.Contains(body, fmt.Sprintf(`"bytes":%d`, 1<<20+13)) {
		t.Fatalf("upload: %d %s", code, body)
	}
	if code, body := post(t, base+"/upload", ct, bytes.NewReader(data), -1); code != 200 {
		t.Fatalf("chunked upload: %d %s", code, body)
	}
	// Over the route's cap: by length up front, by counting when chunked.
	data, ct = form(t, 3<<20)
	if code, _ := post(t, base+"/upload", ct, bytes.NewReader(data), int64(len(data))); code != 413 {
		t.Fatalf("big upload: %d", code)
	}
	if code, _ := post(t, base+"/upload", ct, bytes.NewReader(data), -1); code != 413 {
		t.Fatalf("big chunked upload: %d", code)
	}
	// Over every upload's cap: refused before reading.
	if code, _ := post(t, base+"/upload", ct, io.LimitReader(zeros{}, httpx.MaxUploadBody+1), httpx.MaxUploadBody+1); code != 413 {
		t.Fatalf("huge upload: %d", code)
	}
	// A JSON route refuses a multipart body without reading it.
	if code, _ := post(t, base+"/json", ct, bytes.NewReader(data), int64(len(data))); code != 415 {
		t.Fatalf("multipart to a json route: %d", code)
	}
}

// TestRawBodies: an application/gzip or octet-stream body streams like a
// multipart one (capped, never buffered), and nothing else does.
func TestRawBodies(t *testing.T) {
	base := listen(t)
	put := func(ct string, body io.Reader, length int64) (int, string) {
		t.Helper()
		return send(t, http.MethodPut, base+"/raw", ct, body, length)
	}
	for _, ct := range []string{"application/gzip", "application/octet-stream", "application/gzip; charset=binary"} {
		// well past MaxJSONBody: it is not buffered by LimitBody
		if code, body := put(ct, io.LimitReader(zeros{}, 1500<<10), 1500<<10); code != 200 || !strings.Contains(body, fmt.Sprintf(`"bytes":%d`, 1500<<10)) {
			t.Fatalf("%s: %d %s", ct, code, body)
		}
		if code, body := put(ct, io.LimitReader(zeros{}, 1500<<10), -1); code != 200 || !strings.Contains(body, fmt.Sprintf(`"bytes":%d`, 1500<<10)) {
			t.Fatalf("chunked %s: %d %s", ct, code, body)
		}
	}
	// Over the route's cap: by length up front, by counting when chunked.
	if code, _ := put("application/gzip", io.LimitReader(zeros{}, 3<<20), 3<<20); code != 413 {
		t.Fatalf("big raw body: %d", code)
	}
	if code, _ := put("application/gzip", io.LimitReader(zeros{}, 3<<20), -1); code != 413 {
		t.Fatalf("big chunked raw body: %d", code)
	}
	// Over every upload's cap: refused before reading.
	if code, _ := put("application/gzip", io.LimitReader(zeros{}, httpx.MaxUploadBody+1), httpx.MaxUploadBody+1); code != 413 {
		t.Fatalf("huge raw body: %d", code)
	}
	// Another type is not a raw upload; a JSON route refuses a raw one.
	if code, _ := put("text/plain", strings.NewReader("x"), 1); code != 415 {
		t.Fatalf("text to a raw route: %d", code)
	}
	if code, _ := post(t, base+"/json", "application/gzip", strings.NewReader("x"), 1); code != 415 {
		t.Fatalf("gzip to a json route: %d", code)
	}
	if code, _ := post(t, base+"/json", "application/octet-stream", strings.NewReader("{}"), 2); code != 415 {
		t.Fatalf("octet-stream to a json route: %d", code)
	}
}

// TestRawBodiesArriveIntact: the handler reads exactly the bytes sent (a
// small archive fits one read; a big one streams), by length and
// chunked (curl -T - sends chunked).
func TestRawBodiesArriveIntact(t *testing.T) {
	base := listen(t)
	for _, size := range []int{1, 300, 8 << 10, 70 << 10, 1500 << 10} {
		data := make([]byte, size)
		_, _ = rand.Read(data)
		sum := sha256.Sum256(data)
		want := hex.EncodeToString(sum[:])
		for _, length := range []int64{int64(size), -1} {
			code, body := send(t, http.MethodPut, base+"/raw-sum", "application/gzip", bytes.NewReader(data), length)
			if code != 200 || !strings.Contains(body, want) {
				t.Errorf("size %d length %d: %d %s (want sha256 %s)", size, length, code, body, want)
			}
		}
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// TestKeepAliveAfterAnUnreadUpload: a refused upload closes its connection,
// so its unread body is never parsed as the next request.
func TestKeepAliveAfterAnUnreadUpload(t *testing.T) {
	base := listen(t)
	host := strings.TrimPrefix(strings.TrimSuffix(base, "/api/v1"), "http://")
	conn, err := net.DialTimeout("tcp", host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	body := "--b\r\nContent-Disposition: form-data; name=\"x\"\r\n\r\nPOST /api/v1/json HTTP/1.1\r\n\r\n--b--\r\n"
	fmt.Fprintf(conn, "POST /api/v1/json HTTP/1.1\r\nHost: x\r\nContent-Type: multipart/form-data; boundary=b\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 415 || !res.Close {
		t.Fatalf("status %d, close %v", res.StatusCode, res.Close)
	}
}

// TestCORS: with CORS_ORIGINS set, a listed origin's preflight is answered
// before authentication (a browser sends no token on it) and its requests
// carry Access-Control-Allow-Origin; another origin gets no Access-Control-*
// header. Unset, CORS stays closed.
func TestCORS(t *testing.T) {
	deny := func(httpx.Handler) httpx.Handler {
		return func(http.ResponseWriter, *http.Request) error { return errx.Unauthorized("no token") }
	}
	open := server.New(func() error { return nil }, server.PublicConfig{}, server.Options{CORSOrigins: []string{"https://console.example"}}, []httpx.Middleware{deny}, routes{})
	closed := server.New(func() error { return nil }, server.PublicConfig{}, server.Options{}, []httpx.Middleware{deny}, routes{})
	do := func(app http.Handler, method, path, origin string, preflight bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Origin", origin)
		if preflight {
			req.Header.Set("Access-Control-Request-Method", "POST")
			req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec
	}

	rec := do(open, http.MethodOptions, "/api/v1/json", "https://Console.example", true)
	h := rec.Header()
	if rec.Code != 204 || h.Get("Access-Control-Allow-Origin") != "https://console.example" ||
		!strings.Contains(h.Get("Access-Control-Allow-Headers"), "Authorization") ||
		!strings.Contains(h.Get("Access-Control-Allow-Methods"), "PATCH") || h.Get("Access-Control-Max-Age") != "600" {
		t.Fatalf("preflight: %d %v", rec.Code, h)
	}
	rec = do(open, http.MethodGet, "/api/v1/json", "https://console.example", false)
	if rec.Code != 401 || rec.Header().Get("Access-Control-Allow-Origin") != "https://console.example" ||
		!strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), "WWW-Authenticate") {
		t.Fatalf("request: %d %v", rec.Code, rec.Header())
	}
	rec = do(open, http.MethodOptions, "/api/v1/json", "https://evil.example", true)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("other origin's preflight allowed: %v", rec.Header())
	}
	rec = do(closed, http.MethodOptions, "/api/v1/json", "https://console.example", true)
	for k := range rec.Header() {
		if strings.HasPrefix(k, "Access-Control-") {
			t.Fatalf("CORS closed but sent %s", k)
		}
	}
}

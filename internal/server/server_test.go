package server_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/odyssey/internal/httpx"
	"github.com/Abraxas-365/odyssey/internal/server"
	"github.com/gofiber/fiber/v2"
)

// routes echo what a JSON route decoded and what an upload route streamed.
type routes struct{}

func (routes) Register(r fiber.Router) {
	r.Post("/json", func(c *fiber.Ctx) error {
		var v map[string]any
		if err := httpx.DecodeJSON(c, &v); err != nil {
			return err
		}
		return c.JSON(fiber.Map{"keys": len(v)})
	})
	r.Post("/upload", func(c *fiber.Ctx) error {
		u, err := httpx.NewUpload(c, 2<<20)
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
		return c.JSON(fiber.Map{"bytes": total})
	})
}

// listen serves the production app on a real socket (app.Test does not
// stream bodies the same way).
func listen(t *testing.T) string {
	t.Helper()
	app := server.New(func() error { return nil }, server.PublicConfig{}, server.Options{}, nil, routes{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Listener(ln) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	return "http://" + ln.Addr().String() + "/api/v1"
}

// post sends the request over a raw socket, writing while it reads: a
// server that answers before the body is sent (413) closes the connection,
// which a client that writes first sees only as a reset. length -1 sends
// the body chunked.
func post(t *testing.T, url, contentType string, body io.Reader, length int64) (int, string) {
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
		fmt.Fprintf(w, "POST /%s HTTP/1.1\r\nHost: x\r\nContent-Type: %s\r\n", path, contentType)
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
	medium := `{"a":"` + strings.Repeat("x", 600<<10) + `"}` // past fasthttp's 8 KiB prefetch
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

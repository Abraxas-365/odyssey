package bootstrap

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/odyssey/internal/source/adapters/sourcegithub"
)

const (
	defaultGitHubURL    = "https://github.com"
	defaultGitHubAPIURL = "https://api.github.com"
	githubAppTimeout    = 10 * time.Minute
)

// githubAppConfig is `odyssey-api github-app create` after flag parsing.
type githubAppConfig struct {
	PublicURL  string
	WebhookURL string
	Org        string
	Name       string
	Out        string
	GitHubURL  string
	APIURL     string
	Listen     string
	Force      bool
}

// convertFunc turns the manifest flow's temporary code into the App's
// credentials; tests replace it.
type convertFunc func(ctx context.Context, code string) (sourcegithub.AppCredentials, error)

// CreateGitHubApp registers Odyssey's GitHub App through GitHub's manifest
// flow (`odyssey-api github-app create`): it serves a local page that hands
// the manifest to GitHub, receives the redirect, converts the code and
// writes the App's secrets to files.
func CreateGitHubApp(args []string) error {
	fs := flag.NewFlagSet("github-app create", flag.ContinueOnError)
	var c githubAppConfig
	fs.StringVar(&c.PublicURL, "public-url", "http://localhost:8095", "the API's base URL as GitHub and the browser reach it")
	fs.StringVar(&c.WebhookURL, "webhook-url", "", "where GitHub delivers webhooks (default <public-url>/webhooks/github)")
	fs.StringVar(&c.Org, "org", "", "GitHub organization that owns the App (default: your personal account)")
	fs.StringVar(&c.Name, "name", "Odicea", "the App's name")
	fs.StringVar(&c.Out, "out", ".dev-secrets/github", "directory the secrets are written to")
	fs.StringVar(&c.GitHubURL, "github-url", defaultGitHubURL, "GitHub's web URL (GitHub Enterprise Server)")
	fs.StringVar(&c.APIURL, "api-url", defaultGitHubAPIURL, "GitHub's API URL (GitHub Enterprise Server)")
	fs.StringVar(&c.Listen, "listen", "127.0.0.1:0", "address of the local server that receives GitHub's redirect")
	fs.BoolVar(&c.Force, "force", false, "overwrite existing secret files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	c.PublicURL = strings.TrimRight(strings.TrimSpace(c.PublicURL), "/")
	if strings.TrimSpace(c.WebhookURL) == "" {
		c.WebhookURL = c.PublicURL + "/webhooks/github"
	}
	c.GitHubURL = strings.TrimRight(strings.TrimSpace(c.GitHubURL), "/")
	c.APIURL = strings.TrimRight(strings.TrimSpace(c.APIURL), "/")

	client := &http.Client{Timeout: 30 * time.Second}
	convert := func(ctx context.Context, code string) (sourcegithub.AppCredentials, error) {
		return sourcegithub.ConvertManifest(ctx, client, c.APIURL, code)
	}
	return runGitHubApp(context.Background(), c, convert, os.Stdout, openBrowser)
}

func runGitHubApp(ctx context.Context, c githubAppConfig, convert convertFunc, out io.Writer, open func(string)) error {
	if c.PublicURL == "" {
		return errors.New("--public-url is required")
	}
	// Fail before the user spends the one-time code on a registration whose
	// secrets cannot be saved.
	if err := checkSecretsWritable(c.Out, c.Force); err != nil {
		return err
	}
	state, err := randomHex(16)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", c.Listen, err)
	}
	local := "http://" + ln.Addr().String()
	manifest, err := sourcegithub.Manifest(sourcegithub.ManifestOptions{
		Name:        c.Name,
		PublicURL:   c.PublicURL,
		WebhookURL:  c.WebhookURL,
		Description: "Odyssey: deploys your apps from GitHub repositories.",
	}, local+"/callback")
	if err != nil {
		_ = ln.Close()
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, githubAppTimeout)
	defer cancel()
	h := newManifestHandler(c, state, manifest, convert)
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = srv.Shutdown(sctx)
	}()

	fmt.Fprintf(out, "Abre %s en tu navegador para registrar la GitHub App %q (expira en %s).\n", local, c.Name, githubAppTimeout)
	if open != nil {
		open(local)
	}

	select {
	case res := <-h.done:
		if res.err != nil {
			return res.err
		}
		printGitHubAppNextSteps(out, c, res.creds, res.paths)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting for GitHub's redirect: %w", ctx.Err())
	}
}

type manifestResult struct {
	creds sourcegithub.AppCredentials
	paths githubAppFiles
	err   error
}

// manifestHandler serves the two pages of the flow: / (the form that POSTs
// the manifest to GitHub) and /callback (GitHub's redirect with the code).
type manifestHandler struct {
	cfg      githubAppConfig
	state    string
	manifest []byte
	convert  convertFunc
	done     chan manifestResult // buffered: the first outcome wins

	mu       sync.Mutex
	finished bool
}

func newManifestHandler(c githubAppConfig, state string, manifest []byte, convert convertFunc) *manifestHandler {
	return &manifestHandler{cfg: c, state: state, manifest: manifest, convert: convert, done: make(chan manifestResult, 1)}
}

func (h *manifestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/":
		h.form(w)
	case r.Method == http.MethodGet && r.URL.Path == "/callback":
		h.callback(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *manifestHandler) form(w http.ResponseWriter) {
	action := sourcegithub.ManifestPostURL(h.cfg.GitHubURL, h.cfg.Org) + "?state=" + h.state
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `<!doctype html>
<html lang="es"><head><meta charset="utf-8"><title>Registrar la GitHub App</title></head>
<body>
<p>Redirigiendo a GitHub para registrar la App…</p>
<form id="f" action="%s" method="post">
<input type="hidden" name="manifest" value="%s">
<button type="submit">Registrar la GitHub App en GitHub</button>
</form>
<script>document.getElementById("f").submit();</script>
</body></html>
`, html.EscapeString(action), html.EscapeString(string(h.manifest)))
}

func (h *manifestHandler) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(h.state)) != 1 {
		page(w, http.StatusBadRequest, "Estado inválido", "El parámetro state no coincide; la petición se ignoró.")
		return
	}
	code := q.Get("code")
	if code == "" {
		page(w, http.StatusBadRequest, "Falta el código", "GitHub no envió el parámetro code.")
		return
	}
	h.mu.Lock()
	if h.finished {
		h.mu.Unlock()
		page(w, http.StatusConflict, "Ya procesado", "Este registro ya se completó.")
		return
	}
	h.finished = true
	h.mu.Unlock()

	creds, err := h.convert(r.Context(), code)
	if err != nil {
		page(w, http.StatusBadGateway, "No se pudo completar el registro", "Revisa la terminal.")
		h.done <- manifestResult{err: err}
		return
	}
	files, err := writeGitHubAppFiles(h.cfg.Out, creds, h.cfg.GitHubURL, h.cfg.APIURL, h.cfg.Force)
	if err != nil {
		page(w, http.StatusInternalServerError, "No se pudieron guardar los secretos", "Revisa la terminal.")
		h.done <- manifestResult{err: err}
		return
	}
	page(w, http.StatusOK, "Listo", "Listo, vuelve a la terminal.")
	h.done <- manifestResult{creds: creds, paths: files}
}

func page(w http.ResponseWriter, status int, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html>\n<html lang=\"es\"><head><meta charset=\"utf-8\"><title>%s</title></head><body><p>%s</p></body></html>\n",
		html.EscapeString(title), html.EscapeString(msg))
}

// githubAppFiles are the absolute paths of what was written.
type githubAppFiles struct {
	PrivateKey, WebhookSecret, ClientSecret, Env string
}

func githubAppPaths(dir string) (githubAppFiles, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return githubAppFiles{}, err
	}
	return githubAppFiles{
		PrivateKey:    filepath.Join(abs, "private-key.pem"),
		WebhookSecret: filepath.Join(abs, "webhook-secret"),
		ClientSecret:  filepath.Join(abs, "client-secret"),
		Env:           filepath.Join(abs, "github.env"),
	}, nil
}

func (f githubAppFiles) all() []string {
	return []string{f.PrivateKey, f.WebhookSecret, f.ClientSecret, f.Env}
}

func checkSecretsWritable(dir string, force bool) error {
	files, err := githubAppPaths(dir)
	if err != nil {
		return err
	}
	if force {
		return nil
	}
	for _, p := range files.all() {
		if _, err := os.Lstat(p); err == nil {
			return fmt.Errorf("%s already exists; pass --force to overwrite", p)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// writeGitHubAppFiles stores the App's secrets (0600) in dir (0700) and
// returns their absolute paths. Existing files are only replaced with force.
func writeGitHubAppFiles(dir string, c sourcegithub.AppCredentials, githubURL, apiURL string, force bool) (githubAppFiles, error) {
	files, err := githubAppPaths(dir)
	if err != nil {
		return files, err
	}
	if err := checkSecretsWritable(dir, force); err != nil {
		return files, err
	}
	if err := os.MkdirAll(filepath.Dir(files.Env), 0o700); err != nil {
		return files, err
	}
	pem := c.PEM
	if !strings.HasSuffix(pem, "\n") {
		pem += "\n"
	}
	for _, f := range []struct{ path, data string }{
		{files.PrivateKey, pem},
		{files.WebhookSecret, c.WebhookSecret},
		{files.ClientSecret, c.ClientSecret},
		{files.Env, renderGitHubEnv(c, files, githubURL, apiURL)},
	} {
		if err := writeSecretFile(f.path, []byte(f.data), force); err != nil {
			return files, err
		}
	}
	return files, nil
}

func writeSecretFile(path string, data []byte, force bool) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600) // an overwritten file keeps its old mode otherwise
}

// renderGitHubEnv is github.env: the variables the API reads, pointing at
// the secret files. GITHUB_URL/GITHUB_API_URL appear only when not github.com.
func renderGitHubEnv(c sourcegithub.AppCredentials, f githubAppFiles, githubURL, apiURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "GITHUB_APP_ID=%d\n", c.ID)
	fmt.Fprintf(&b, "GITHUB_APP_SLUG=%s\n", c.Slug)
	fmt.Fprintf(&b, "GITHUB_CLIENT_ID=%s\n", c.ClientID)
	fmt.Fprintf(&b, "GITHUB_APP_PRIVATE_KEY_FILE=%s\n", f.PrivateKey)
	fmt.Fprintf(&b, "GITHUB_WEBHOOK_SECRET_FILE=%s\n", f.WebhookSecret)
	fmt.Fprintf(&b, "GITHUB_CLIENT_SECRET_FILE=%s\n", f.ClientSecret)
	if u := strings.TrimRight(githubURL, "/"); u != "" && u != defaultGitHubURL {
		fmt.Fprintf(&b, "GITHUB_URL=%s\n", u)
	}
	if u := strings.TrimRight(apiURL, "/"); u != "" && u != defaultGitHubAPIURL {
		fmt.Fprintf(&b, "GITHUB_API_URL=%s\n", u)
	}
	return b.String()
}

func printGitHubAppNextSteps(out io.Writer, c githubAppConfig, creds sourcegithub.AppCredentials, f githubAppFiles) {
	fmt.Fprintf(out, "\nGitHub App creada: %s (id %d)\n", creds.Slug, creds.ID)
	if creds.HTMLURL != "" {
		fmt.Fprintf(out, "  Página de la App: %s\n", creds.HTMLURL)
	}
	fmt.Fprintf(out, "\nSecretos escritos en %s:\n", filepath.Dir(f.Env))
	for _, p := range f.all() {
		fmt.Fprintf(out, "  %s\n", p)
	}
	fmt.Fprintf(out, "\nSiguientes pasos:\n")
	fmt.Fprintf(out, "  1. Carga la configuración: `set -a; source %s; set +a` o añade sus líneas a .env.\n", f.Env)
	fmt.Fprintf(out, "  2. Reinicia la API (make run).\n")
	fmt.Fprintf(out, "  3. Instálala desde la consola de Odyssey (el flujo de instalación redirige a %s/github/setup).\n", c.PublicURL)
	if strings.Contains(c.PublicURL, "localhost") || strings.Contains(c.PublicURL, "127.0.0.1") {
		fmt.Fprintf(out, "  Nota: GitHub no alcanza %s; para recibir webhooks usa un túnel (smee, ngrok) y edita la URL del webhook de la App.\n", c.PublicURL)
	}
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// openBrowser is best effort: the URL is printed regardless.
func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		cmd = exec.Command("xdg-open", u)
	default:
		return
	}
	if cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}

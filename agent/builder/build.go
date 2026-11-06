package builder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// BuildOptions configure build (from the Job's environment).
type BuildOptions struct {
	Work string
	// Image is pushed: registry/repository:tag.
	Image string
	// CacheRef is the registry reference of the project's layer cache.
	CacheRef string
	// CacheKey isolates Railpack's mount caches per project.
	CacheKey string
	// Frontend is Railpack's BuildKit frontend image.
	Frontend string
	Platform string
	// Insecure pushes and pulls the registry over plain HTTP.
	Insecure bool
	Out      io.Writer
	// Command runs buildctl (default: buildctl-daemonless.sh, which starts
	// a rootless buildkitd for the build).
	Command string
}

func buildFromEnv() BuildOptions {
	return BuildOptions{
		Work: os.Getenv("ODYSSEY_WORK"), Image: os.Getenv("ODYSSEY_IMAGE"), CacheRef: os.Getenv("ODYSSEY_CACHE_REF"),
		CacheKey: os.Getenv("ODYSSEY_CACHE_KEY"), Frontend: os.Getenv("ODYSSEY_FRONTEND"), Platform: os.Getenv("ODYSSEY_PLATFORM"),
		Insecure: os.Getenv("ODYSSEY_REGISTRY_INSECURE") == "true", Out: os.Stdout,
	}
}

// Args is buildctl's command line for a build in mode.
func (o BuildOptions) Args(mode string) []string {
	insecure := ""
	if o.Insecure {
		insecure = ",registry.insecure=true"
	}
	src := filepath.Join(o.Work, SourceDir)
	args := []string{"build", "--progress=plain", "--local", "context=" + src}
	switch mode {
	case ModeDockerfile:
		args = append(args, "--frontend", "dockerfile.v0", "--local", "dockerfile="+src)
	default:
		args = append(args, "--frontend", "gateway.v0", "--opt", "source="+o.Frontend,
			"--local", "dockerfile="+filepath.Join(o.Work, PlanDir), "--opt", "build-arg:cache-key="+o.CacheKey)
	}
	if o.Platform != "" {
		args = append(args, "--opt", "platform="+o.Platform)
	}
	args = append(args,
		"--output", "type=image,name="+o.Image+",push=true"+insecure,
		"--metadata-file", filepath.Join(o.Work, "metadata.json"))
	if o.CacheRef != "" {
		args = append(args,
			"--import-cache", "type=registry,ref="+o.CacheRef+insecure,
			// a cache that cannot be written must not fail the build
			"--export-cache", "type=registry,ref="+o.CacheRef+",mode=max,ignore-error=true"+insecure)
	}
	return args
}

// Build builds and pushes the image, reporting its digest.
func Build(ctx context.Context, o BuildOptions) Result {
	if o.Command == "" {
		o.Command = "buildctl-daemonless.sh"
	}
	if d := reusedDigest(o.Work); d != "" {
		fmt.Fprintf(o.Out, "Reusing the image of an earlier build: %s (nothing to build)\n", d)
		return Result{Digest: d, Reused: true}
	}
	mode, err := os.ReadFile(filepath.Join(o.Work, ModeFile))
	if err != nil {
		return Result{Reason: ReasonFailed, Message: "the source was not prepared: " + err.Error()}
	}
	tail := &tailBuffer{max: 32 << 10}
	cmd := exec.CommandContext(ctx, o.Command, o.Args(strings.TrimSpace(string(mode)))...)
	out := io.MultiWriter(o.Out, tail)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		text := tail.String()
		return Result{Reason: classify(text), Message: Tail(errorLines(text), maxMessage)}
	}
	meta, err := os.ReadFile(filepath.Join(o.Work, "metadata.json"))
	if err != nil {
		return Result{Reason: ReasonPush, Message: "the build did not say what it pushed: " + err.Error()}
	}
	var m struct {
		Digest string `json:"containerimage.digest"`
	}
	if err := json.Unmarshal(meta, &m); err != nil || m.Digest == "" {
		return Result{Reason: ReasonPush, Message: "the build did not say what it pushed"}
	}
	fmt.Fprintf(o.Out, "Pushed %s@%s\n", o.Image, m.Digest)
	return Result{Digest: m.Digest}
}

var pushFailure = regexp.MustCompile(`(?i)failed to push|pushing .*(denied|unauthorized|forbidden)|failed to do request: .*(/v2/|registry)|server gave HTTP response to HTTPS client|x509:`)

// classify tells a push failure from a build failure by buildctl's words.
func classify(output string) string {
	if pushFailure.MatchString(lastLines(output, 30)) {
		return ReasonPush
	}
	return ReasonFailed
}

// errorLines is the end of the output, preferring buildctl's error and the
// failing step's last lines.
func errorLines(output string) string {
	return lastLines(output, 25)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(bytes.ToValidUTF8(t.buf, []byte("?")))
}

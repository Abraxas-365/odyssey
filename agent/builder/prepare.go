package builder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/odyssey/pkg/srcarchive"
	"github.com/railwayapp/railpack/core"
	"github.com/railwayapp/railpack/core/app"
	"github.com/railwayapp/railpack/core/logger"
	"github.com/railwayapp/railpack/core/mise"
)

// Layout of the work volume shared by prepare and build.
const (
	SourceDir = "src"  // the extracted app (the build context)
	PlanDir   = "plan" // railpack-plan.json, when Railpack builds
	BinDir    = "bin"  // odyssey-agent, copied for the build container
	ModeFile  = "mode" // "dockerfile" or "railpack"
	PlanFile  = "railpack-plan.json"
)

// Build modes.
const (
	ModeDockerfile = "dockerfile"
	ModeRailpack   = "railpack"
)

// PrepareOptions configure prepare (from the Job's environment).
type PrepareOptions struct {
	SourceURL string
	Token     string
	SHA256    string
	Size      int64
	Work      string
	// Self is this binary, copied for the build container.
	Self string
	// Out receives progress lines (the pod's log).
	Out io.Writer
	// Client fetches the source (default: a client with a 10 min timeout).
	Client *http.Client
	// Plan writes the Railpack plan (default: railpack's library).
	Plan func(dir string) (plan []byte, logs []string, err error)
	// RetryWait is the first wait between fetch attempts (default 2 s).
	RetryWait time.Duration
}

func prepareFromEnv() (PrepareOptions, error) {
	size, err := strconv.ParseInt(os.Getenv("ODYSSEY_SOURCE_SIZE"), 10, 64)
	if err != nil {
		return PrepareOptions{}, fmt.Errorf("ODYSSEY_SOURCE_SIZE: %w", err)
	}
	token, err := os.ReadFile(os.Getenv("ODYSSEY_SOURCE_TOKEN_FILE"))
	if err != nil {
		return PrepareOptions{}, fmt.Errorf("read the source token: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return PrepareOptions{}, err
	}
	return PrepareOptions{
		SourceURL: os.Getenv("ODYSSEY_SOURCE_URL"), Token: strings.TrimSpace(string(token)),
		SHA256: os.Getenv("ODYSSEY_SOURCE_SHA256"), Size: size, Work: os.Getenv("ODYSSEY_WORK"), Self: self, Out: os.Stdout,
	}, nil
}

// Prepare fetches, verifies and extracts the source, then decides how to
// build it: the app's own Dockerfile when it has one, else a Railpack plan.
// A failure is a Result with its reason.
func Prepare(ctx context.Context, o PrepareOptions) Result {
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 10 * time.Minute}
	}
	if o.Plan == nil {
		o.Plan = railpackPlan
	}
	say := func(format string, a ...any) { fmt.Fprintf(o.Out, format+"\n", a...) }
	src := filepath.Join(o.Work, SourceDir)
	for _, d := range []string{src, filepath.Join(o.Work, PlanDir), filepath.Join(o.Work, BinDir)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return Result{Reason: ReasonFailed, Message: err.Error()}
		}
	}
	if err := copyFile(o.Self, filepath.Join(o.Work, BinDir, "odyssey-agent"), 0o755); err != nil {
		return Result{Reason: ReasonFailed, Message: "copy the builder: " + err.Error()}
	}

	say("Fetching the source (%s)", srcarchive.MiB(o.Size))
	sum, err := fetchRetrying(ctx, o, src, say)
	if err != nil {
		var bad *srcarchive.Invalid
		var big *srcarchive.TooLarge
		if errors.As(err, &bad) || errors.As(err, &big) {
			return Result{Reason: ReasonSource, Message: "the source is not valid: " + err.Error()}
		}
		return Result{Reason: ReasonSource, Message: "could not fetch the source: " + err.Error()}
	}
	say("Source verified: %d files, %s unpacked", sum.Files, srcarchive.MiB(sum.UnpackedSize))

	mode := ModeRailpack
	if fi, err := os.Lstat(filepath.Join(src, "Dockerfile")); err == nil && fi.Mode().IsRegular() {
		mode = ModeDockerfile
		say("Found a Dockerfile: building with it")
	} else {
		say("No Dockerfile: detecting how to build the app (Railpack)")
		plan, logs, err := o.Plan(src)
		for _, l := range logs {
			say("%s", l)
		}
		if err != nil {
			if errors.Is(err, errUnsupported) {
				return Result{Reason: ReasonUnsupported, Message: Tail(strings.Join(logs, "\n"), maxMessage)}
			}
			return Result{Reason: ReasonFailed, Message: "could not plan the build: " + err.Error()}
		}
		if err := os.WriteFile(filepath.Join(o.Work, PlanDir, PlanFile), plan, 0o644); err != nil {
			return Result{Reason: ReasonFailed, Message: err.Error()}
		}
	}
	if err := os.WriteFile(filepath.Join(o.Work, ModeFile), []byte(mode), 0o644); err != nil {
		return Result{Reason: ReasonFailed, Message: err.Error()}
	}
	return Result{}
}

// transient is a fetch that may work when tried again: the agent replica
// holds no tunnel, the control plane is busy or unreachable.
type transient struct{ error }

// fetchRetrying fetches until it works, fails for good, or the attempts run
// out; each attempt starts from an empty folder.
func fetchRetrying(ctx context.Context, o PrepareOptions, dir string, say func(string, ...any)) (srcarchive.Summary, error) {
	wait := o.RetryWait
	if wait <= 0 {
		wait = 2 * time.Second
	}
	var err error
	for attempt := 1; ; attempt++ {
		var sum srcarchive.Summary
		sum, err = fetch(ctx, o, dir)
		var t transient
		if err == nil || !errors.As(err, &t) || attempt >= fetchAttempts {
			return sum, err
		}
		say("Could not fetch the source yet (%v); retrying", err)
		if err := emptyDir(dir); err != nil {
			return srcarchive.Summary{}, err
		}
		select {
		case <-ctx.Done():
			return srcarchive.Summary{}, ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
}

const fetchAttempts = 8

func emptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// fetch downloads the archive and extracts it while checking it against the
// platform's rules, its size and its digest.
func fetch(ctx context.Context, o PrepareOptions, dir string) (srcarchive.Summary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.SourceURL, nil)
	if err != nil {
		return srcarchive.Summary{}, err
	}
	req.Header.Set("Authorization", "Bearer "+o.Token)
	res, err := o.Client.Do(req)
	if err != nil {
		return srcarchive.Summary{}, transient{err}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		err := fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(msg)))
		switch res.StatusCode {
		case http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusGatewayTimeout:
			return srcarchive.Summary{}, transient{err}
		}
		return srcarchive.Summary{}, err
	}
	limits := srcarchive.Platform()
	if o.Size < limits.Compressed {
		limits.Compressed = o.Size
	}
	sum, err := srcarchive.Extract(res.Body, dir, limits)
	if err != nil {
		var bad *srcarchive.Invalid
		var big *srcarchive.TooLarge
		if !errors.As(err, &bad) && !errors.As(err, &big) {
			return sum, transient{err} // the stream broke
		}
		return sum, err
	}
	if sum.Size != o.Size || !strings.EqualFold(sum.SHA256, o.SHA256) {
		return sum, &srcarchive.Invalid{Message: "the fetched source is not the uploaded one (size or digest differ)"}
	}
	return sum, nil
}

var errUnsupported = errors.New("unsupported app")

// MiseDir is where Railpack keeps mise (a constant of the library, under
// /tmp: an emptyDir in the pod). BakedMise is where the agent's image ships
// the pinned, verified binary (Dockerfile); prepare links it into place so
// the library never downloads one.
const (
	MiseDir   = mise.InstallDir
	BakedMise = "/opt/railpack/mise"
)

// MiseVersion is the mise the Railpack library expects (the Dockerfile
// bakes the same; agent/pins_test.go checks).
func MiseVersion() string { return strings.TrimSpace(mise.Version) }

// placeMise links the baked mise where the library looks for it (nothing
// to do when it is there or the image has none: a dev run). A link, not a
// copy: the binary is ~140 MB and every build would pay for it.
func placeMise(from, to string) error {
	name := "mise-" + MiseVersion()
	src := filepath.Join(from, name)
	if _, err := os.Stat(src); err != nil {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(to, name)); err == nil {
		return nil
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		return err
	}
	return os.Symlink(src, filepath.Join(to, name))
}

// railpackPlan runs Railpack's planner on dir: the plan as JSON, the lines
// it logged, and errUnsupported when it does not know how to build or
// start the app.
func railpackPlan(dir string) ([]byte, []string, error) {
	if err := placeMise(BakedMise, MiseDir); err != nil {
		return nil, nil, fmt.Errorf("place mise: %w", err)
	}
	a, err := app.NewApp(dir)
	if err != nil {
		return nil, nil, err
	}
	res, err := core.GenerateBuildPlan(a, app.NewEnvironment(nil), &core.GenerateBuildPlanOptions{
		RailpackVersion: RailpackVersion, ErrorMissingStartCommand: true,
	})
	var logs []string
	if res != nil {
		for _, m := range res.Logs {
			logs = append(logs, logLine(m))
		}
	}
	if err != nil {
		return nil, logs, err // transient (mise could not reach its sources)
	}
	if !res.Success {
		return nil, logs, errUnsupported
	}
	plan, err := json.Marshal(res.Plan)
	return plan, logs, err
}

func logLine(m logger.Msg) string {
	s := m.Msg
	if m.Level != logger.Info {
		s = string(m.Level) + ": " + s
	}
	if m.DocsPath != "" {
		docs := m.DocsPath
		if strings.HasPrefix(docs, "/") {
			docs = "https://railpack.com" + docs
		}
		s += " (" + docs + ")"
	}
	return s
}

func copyFile(from, to string, mode os.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

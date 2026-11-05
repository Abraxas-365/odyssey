package build

import (
	"strings"
	"time"
)

// Scan is the vulnerability scan of a built image (3c). The cluster's agent
// scans the image right after the build; the build.scan workflow records the
// outcome on the build. The build itself never waits for it: a deployment
// that publishes the image reads it through Build.Scan (release's
// authorize_scan step). A succeeded build always ends up with a Scan, even
// when it could not be done (Error says why), so a reader can tell "not yet"
// (nil) from "will never be".
type Scan struct {
	// Digest is the scanned image's digest (always the build's own).
	Digest   string `json:"digest"`
	Critical int    `json:"critical"`
	High     int    `json:"high"`
	Medium   int    `json:"medium"`
	Low      int    `json:"low"`
	// Tool names the scanner and its version ("trivy 0.76.2").
	Tool string `json:"tool"`
	// DBUpdatedAt is when the scanner's vulnerability database was built
	// (RFC 3339; "" when unknown).
	DBUpdatedAt string `json:"db_updated_at"`
	// Error is why the image could not be scanned ("" when it was), in plain
	// Spanish: the counts then mean nothing and the image counts as not
	// scanned.
	Error     string    `json:"error"`
	ScannedAt time.Time `json:"scanned_at"`
}

// Known reports whether the counts mean something: the scan was done.
func (s Scan) Known() bool { return s.Error == "" }

// BuildScan is the scan of one build, as the batch read Queries.Scans
// answers it.
type BuildScan struct {
	Build ID   `json:"build_id"`
	Scan  Scan `json:"scan"`
}

// MaxScans bounds Queries.Scans.
const MaxScans = 2000

// Why an image has no scan (Scan.Error), in words for whoever publishes.
const (
	// ScanUnsupportedMessage: the cluster's agent does not scan (it does not
	// list the capability) or the runner never does.
	ScanUnsupportedMessage = "Este clúster no revisa vulnerabilidades."
	// ScanTimeoutMessage: the runner never reported.
	ScanTimeoutMessage = "La revisión de vulnerabilidades no terminó a tiempo."
	// ScanMismatchMessage: the runner reported the scan of another image.
	ScanMismatchMessage = "La revisión se hizo sobre otra imagen que la construida."
	// ScanFailedMessage is used when the scanner gave no reason.
	ScanFailedMessage = "No se pudo revisar la imagen."
)

// ScanPhase is where the runner is with a build's scan.
type ScanPhase string

const (
	// ScanPending: the runner scans it and has not finished.
	ScanPending ScanPhase = "pending"
	// ScanDone: the scan ended (successfully or with Scan.Error).
	ScanDone ScanPhase = "done"
	// ScanUnsupported: the runner does not scan (no scanner in the cluster, a
	// fake builder): there will be none.
	ScanUnsupported ScanPhase = "unsupported"
)

// ScanObservation is what the runner says of a build's scan.
type ScanObservation struct {
	Phase ScanPhase
	Scan  Scan // Done only
}

// DigestOf is the digest of a pinned image ("" when it is not pinned).
func DigestOf(image string) string {
	_, d, ok := strings.Cut(image, "@")
	if !ok {
		return ""
	}
	return d
}

// WorkflowScan follows the scan of a succeeded build until the runner
// reports it, and records it (buildsvc).
const WorkflowScan = "build.scan"

// StepScan is the single step of build.scan.
const StepScan = "scan"

// ScanTimeout bounds the wait for a scan: a cluster's first scan downloads
// the vulnerability database (about 2 GB) before it scans anything. The
// deployment that wants the scan gives up sooner (release.ScanWait).
const ScanTimeout = 30 * time.Minute

package fleet

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/odyssey/internal/errx"
)

// Image scans (3c). A cluster whose agent lists CapabilityScan runs a
// vulnerability scan (Trivy, in a Job of the cluster) of every image it
// builds, right after the build succeeds, and reports the outcome through the
// tunnel. The build never waits for it: the build domain follows the scan on
// its own (docs/scan.md).

// CapabilityScan is listed in Capabilities.APIs when the agent scans the
// images it builds.
const CapabilityScan = "odicea.scan"

// MaxReportScans is the most statuses one ScanReport may carry.
const MaxReportScans = 200

// MaxScanTool bounds ScanStatus.Tool; MaxScanError bounds ScanStatus.Error.
const (
	MaxScanTool  = 100
	MaxScanError = 500
	// maxScanCount bounds a severity count: a larger one is a broken report.
	maxScanCount = 10_000_000
)

// CanScan reports whether the cluster lists CapabilityScan.
func (c Capabilities) CanScan() bool { return slices.Contains(c.APIs, CapabilityScan) }

// ScanStatus is the outcome of the scan of one build's image, as the agent
// reported it.
type ScanStatus struct {
	Build BuildID `db:"build_id"`
	// Digest is the image that was scanned (sha256:<hex>): the control plane
	// keeps the scan only when it is the digest the build reported.
	Digest   string `db:"image_digest"`
	Critical int    `db:"critical"`
	High     int    `db:"high"`
	Medium   int    `db:"medium"`
	Low      int    `db:"low"`
	// Tool names the scanner and its version ("trivy 0.76.2").
	Tool string `db:"tool"`
	// DBUpdatedAt is when the scanner's vulnerability database was built
	// (RFC 3339, UTC; "" when unknown).
	DBUpdatedAt string `db:"db_updated_at"`
	// Error is why the scan could not be done ("" when it was): the counts
	// then mean nothing and the image counts as not scanned.
	Error      string    `db:"error"`
	ObservedAt time.Time `db:"observed_at"`
}

// Validate bounds what an agent reports of a scan.
func (s ScanStatus) Validate() error {
	switch {
	case s.Build.IsZero():
		return errx.Validation("a reported scan needs its build id")
	case !digestPattern.MatchString(s.Digest):
		return errx.Validation("a reported scan needs the digest of the image it scanned")
	case s.Critical < 0 || s.High < 0 || s.Medium < 0 || s.Low < 0 ||
		s.Critical > maxScanCount || s.High > maxScanCount || s.Medium > maxScanCount || s.Low > maxScanCount:
		return errx.Validation("a reported scan has an impossible count")
	}
	return nil
}

// Clean is the status with its free text made safe to store and show, and
// the database date normalized (an unreadable one is dropped: it only
// informs).
func (s ScanStatus) Clean() ScanStatus {
	s.Tool = truncateRunes(strings.TrimSpace(cleanText(s.Tool)), MaxScanTool)
	s.Error = truncateRunes(strings.TrimSpace(cleanText(s.Error)), MaxScanError)
	raw := strings.TrimSpace(cleanText(s.DBUpdatedAt))
	s.DBUpdatedAt = ""
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		s.DBUpdatedAt = t.UTC().Format(time.RFC3339)
	}
	if s.Error != "" { // the counts of a failed scan mean nothing
		s.Critical, s.High, s.Medium, s.Low = 0, 0, 0, 0
	}
	return s
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// ScanPhase is where a build's scan is, as the fleet sees it.
type ScanPhase string

const (
	// ScanPending: the cluster scans it and has not reported yet.
	ScanPending ScanPhase = "pending"
	// ScanDone: the scan ended (successfully or with Error).
	ScanDone ScanPhase = "done"
	// ScanUnsupported: the build's cluster does not list CapabilityScan (or
	// the build is unknown): there will be no scan.
	ScanUnsupported ScanPhase = "unsupported"
)

// BuildScan is the answer to Builds.BuildScan.
type BuildScan struct {
	Phase  ScanPhase
	Status ScanStatus // Done only
}

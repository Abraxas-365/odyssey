package builder

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// ScanSummary is what the scan Job's last container reports (in its
// termination message): the findings Trivy wrote, counted. The Job's first
// container is Trivy itself (agent/controllers/scan); this one only reads
// its JSON report, because a report is far larger than a termination
// message and what the control plane keeps is the counts.
type ScanSummary struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	// Tool is the scanner and its version ("trivy 0.76.2").
	Tool string `json:"tool,omitempty"`
	// DBUpdatedAt (RFC 3339) is when the vulnerability database was built.
	DBUpdatedAt string `json:"db_updated_at,omitempty"`
}

// ParseScanSummary reads a termination message; ok is false when it is not
// a ScanSummary (empty, or Kubernetes' own words).
func ParseScanSummary(msg string) (ScanSummary, bool) {
	var s ScanSummary
	if strings.TrimSpace(msg) == "" || json.Unmarshal([]byte(msg), &s) != nil {
		return ScanSummary{}, false
	}
	return s, s.Tool != "" || s.Critical+s.High+s.Medium+s.Low > 0 || s.DBUpdatedAt != ""
}

// MaxScanReport bounds the report Summarize reads (and, with room to spare,
// the Job's `out` volume): a report beyond it is an error, not memory. Trivy's
// reports are tens of kilobytes to a few megabytes; a few hundred thousand
// findings are about 100 MiB.
const MaxScanReport = 256 << 20

// errReportTooLarge is what a report past MaxScanReport ends with.
var errReportTooLarge = fmt.Errorf("the scanner's report is larger than %d MiB", MaxScanReport>>20)

// capped reads at most max bytes and then fails (io.LimitReader would end
// quietly, and a truncated report must not read as a short, valid one).
type capped struct {
	r    io.Reader
	left int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var one [1]byte
		if n, _ := c.r.Read(one[:]); n > 0 {
			return 0, errReportTooLarge
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

type vulnerability struct {
	VulnerabilityID string `json:"VulnerabilityID"`
	PkgName         string `json:"PkgName"`
	Severity        string `json:"Severity"`
}

// Summarize counts a Trivy JSON report by severity. A vulnerability is
// counted once per package: the same CVE in two layers' copies of a package
// (or reported by two Results) is one finding, but the same CVE in two
// packages is two, as Trivy's own table counts them. Severities other than
// critical, high, medium and low (UNKNOWN) are not counted. Tool and
// DBUpdatedAt are left to the caller.
//
// The report is a list of Results (one per OS package set, language lockfile
// or jar), each with its vulnerabilities (any of which may be null). It is
// read as a stream, one vulnerability at a time, never whole, and at most
// MaxScanReport of it.
func Summarize(report io.Reader) (ScanSummary, error) { return summarize(report, MaxScanReport) }

func summarize(report io.Reader, max int64) (ScanSummary, error) {
	dec := json.NewDecoder(&capped{r: report, left: max})
	var s ScanSummary
	seen := map[[2]string]bool{}
	count := func(v vulnerability) {
		k := [2]string{v.VulnerabilityID, v.PkgName}
		if seen[k] {
			return
		}
		seen[k] = true
		switch strings.ToUpper(v.Severity) {
		case "CRITICAL":
			s.Critical++
		case "HIGH":
			s.High++
		case "MEDIUM":
			s.Medium++
		case "LOW":
			s.Low++
		}
	}
	err := walkObject(dec, func(key string) error {
		if key != "Results" {
			return skipValue(dec)
		}
		return walkArray(dec, func() error {
			return walkObject(dec, func(key string) error {
				if key != "Vulnerabilities" {
					return skipValue(dec)
				}
				return walkArray(dec, func() error {
					var v vulnerability
					if err := dec.Decode(&v); err != nil {
						return err
					}
					count(v)
					return nil
				})
			})
		})
	})
	if err != nil {
		if errors.Is(err, errReportTooLarge) {
			return ScanSummary{}, err
		}
		return ScanSummary{}, fmt.Errorf("the scanner's report is not valid JSON: %w", err)
	}
	return s, nil
}

// walkObject reads one JSON object, calling field for each key with the
// decoder positioned at its value (field must consume it). null is an empty
// object.
func walkObject(dec *json.Decoder, field func(key string) error) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if t == nil {
		return nil
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("expected an object, found %v", t)
	}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := k.(string)
		if !ok {
			return fmt.Errorf("expected a key, found %v", k)
		}
		if err := field(key); err != nil {
			return err
		}
	}
	_, err = dec.Token() // }
	return err
}

// walkArray reads one JSON array, calling elem for each element with the
// decoder positioned at it (elem must consume it). null is an empty array.
func walkArray(dec *json.Decoder, elem func() error) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if t == nil {
		return nil
	}
	if d, ok := t.(json.Delim); !ok || d != '[' {
		return fmt.Errorf("expected a list, found %v", t)
	}
	for dec.More() {
		if err := elem(); err != nil {
			return err
		}
	}
	_, err = dec.Token() // ]
	return err
}

// skipValue reads past one JSON value without keeping it.
func skipValue(dec *json.Decoder) error {
	depth := 0
	for {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			default:
				depth--
			}
		}
		if depth == 0 {
			return nil
		}
	}
}

// DBUpdatedAt reads when Trivy's vulnerability database was built from the
// metadata.json it keeps next to it ("" when unreadable: not fatal).
func DBUpdatedAt(metadata io.Reader) string {
	var m struct {
		UpdatedAt time.Time `json:"UpdatedAt"`
	}
	if json.NewDecoder(metadata).Decode(&m) != nil || m.UpdatedAt.IsZero() {
		return ""
	}
	return m.UpdatedAt.UTC().Format(time.RFC3339)
}

// scanSummary is `odyssey-agent builder scan-summary`: it reads the report
// (ODYSSEY_SCAN_REPORT) and the database's metadata (ODYSSEY_SCAN_DB_METADATA)
// and returns the counts. An error is the message of the failed Job.
func scanSummary() (ScanSummary, error) {
	path := os.Getenv("ODYSSEY_SCAN_REPORT")
	if path == "" {
		return ScanSummary{}, errors.New("no report to read (ODYSSEY_SCAN_REPORT)")
	}
	f, err := os.Open(path)
	if err != nil {
		return ScanSummary{}, fmt.Errorf("the scanner wrote no report: %w", err)
	}
	defer f.Close()
	s, err := Summarize(f)
	if err != nil {
		return ScanSummary{}, err
	}
	s.Tool = os.Getenv("ODYSSEY_SCAN_TOOL")
	if meta, err := os.Open(os.Getenv("ODYSSEY_SCAN_DB_METADATA")); err == nil {
		defer meta.Close()
		s.DBUpdatedAt = DBUpdatedAt(meta)
	}
	return s, nil
}

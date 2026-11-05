package builder

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestSummarizeCountsBySeverityOncePerPackage(t *testing.T) {
	report := `{"Results":[
	  {"Target":"alpine","Vulnerabilities":[
	    {"VulnerabilityID":"CVE-1","PkgName":"openssl","Severity":"CRITICAL"},
	    {"VulnerabilityID":"CVE-1","PkgName":"openssl","Severity":"CRITICAL"},
	    {"VulnerabilityID":"CVE-1","PkgName":"libssl","Severity":"CRITICAL"},
	    {"VulnerabilityID":"CVE-2","PkgName":"busybox","Severity":"high"},
	    {"VulnerabilityID":"CVE-3","PkgName":"busybox","Severity":"MEDIUM"},
	    {"VulnerabilityID":"CVE-4","PkgName":"zlib","Severity":"LOW"},
	    {"VulnerabilityID":"CVE-5","PkgName":"zlib","Severity":"UNKNOWN"}]},
	  {"Target":"node","Vulnerabilities":null},
	  {"Target":"jar"},
	  {"Target":"node-pkg","Vulnerabilities":[
	    {"VulnerabilityID":"CVE-2","PkgName":"busybox","Severity":"HIGH"},
	    {"VulnerabilityID":"CVE-6","PkgName":"lodash","Severity":"HIGH"}]}]}`
	got, err := Summarize(strings.NewReader(report))
	if err != nil {
		t.Fatal(err)
	}
	if got.Critical != 2 || got.High != 2 || got.Medium != 1 || got.Low != 1 {
		t.Errorf("got %+v, want 2 critical, 2 high, 1 medium, 1 low", got)
	}
}

func TestSummarizeAnEmptyOrBrokenReport(t *testing.T) {
	if got, err := Summarize(strings.NewReader(`{"Results":null}`)); err != nil || got != (ScanSummary{}) {
		t.Errorf("no results: %+v, %v", got, err)
	}
	if got, err := Summarize(strings.NewReader(`{}`)); err != nil || got != (ScanSummary{}) {
		t.Errorf("empty report: %+v, %v", got, err)
	}
	for _, bad := range []string{``, `not json`, `{"Results":[{"Vulnerabilities":[`} {
		if _, err := Summarize(strings.NewReader(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// synthetic is a Trivy report of n findings (a thousand distinct ones, so the
// summary stays small) produced as it is read: it is never held whole.
type synthetic struct {
	n, i int
	buf  []byte
	head bool
	done bool
}

func (s *synthetic) Read(p []byte) (int, error) {
	for len(s.buf) < len(p) && !s.done {
		switch {
		case !s.head:
			s.head = true
			s.buf = append(s.buf, `{"SchemaVersion":2,"Metadata":{"OS":{"Family":"alpine","Name":"3.19"},"ImageConfig":{"history":[{"a":[1,2,{"b":null}]}]}},"Results":[{"Target":"big","Vulnerabilities":[`...)
		case s.i < s.n:
			if s.i > 0 {
				s.buf = append(s.buf, ',')
			}
			sev := []string{"CRITICAL", "HIGH", "MEDIUM", "LOW"}[s.i%4]
			s.buf = fmt.Appendf(s.buf, `{"VulnerabilityID":"CVE-%d","PkgName":"pkg","Severity":%q,"Description":%q,"References":["https://a","https://b"]}`,
				s.i%1000, sev, strings.Repeat("x", 200))
			s.i++
		default:
			s.buf = append(s.buf, `]}]}`...)
			s.done = true
		}
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	if n == 0 && s.done {
		return 0, io.EOF
	}
	return n, nil
}

// A large report is counted as a stream (every field but the findings is
// skipped); one past the cap is an error, never a short, valid-looking count.
func TestSummarizeStreamsALargeReportAndCapsIt(t *testing.T) {
	// ~230 bytes a finding: 200k of them are ~46 MB.
	got, err := Summarize(&synthetic{n: 200_000})
	if err != nil {
		t.Fatal(err)
	}
	// CVE-i%1000 with severity i%4: each id has one severity (1000 % 4 == 0)
	if got.Critical != 250 || got.High != 250 || got.Medium != 250 || got.Low != 250 {
		t.Errorf("got %+v, want 250 of each", got)
	}
	_, err = summarize(&synthetic{n: 200_000}, 1<<20)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("a report past the cap: %v", err)
	}
}

func TestSummarizeSkipsWhatItDoesNotCount(t *testing.T) {
	report := `{"Metadata":{"x":[{"Results":[{"Vulnerabilities":[{"VulnerabilityID":"CVE-9","PkgName":"p","Severity":"CRITICAL"}]}]}]},
	  "Results":[{"Misconfigurations":[{"Vulnerabilities":1}],"Vulnerabilities":[{"VulnerabilityID":"CVE-1","PkgName":"p","Severity":"HIGH","Extra":{"a":[1,[2]]}}]}]}`
	got, err := Summarize(strings.NewReader(report))
	if err != nil || got.Critical != 0 || got.High != 1 {
		t.Errorf("%+v %v", got, err)
	}
	for _, bad := range []string{`[]`, `{"Results":{}}`, `{"Results":[1]}`, `{"Results":[{"Vulnerabilities":{}}]}`} {
		if _, err := Summarize(strings.NewReader(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestScanSummaryMessage(t *testing.T) {
	s, ok := ParseScanSummary(`{"critical":1,"high":2,"tool":"trivy 0.76.2","db_updated_at":"2026-11-05T07:00:54Z"}`)
	if !ok || s.Critical != 1 || s.High != 2 || s.Tool != "trivy 0.76.2" || s.DBUpdatedAt != "2026-11-05T07:00:54Z" {
		t.Errorf("%+v %v", s, ok)
	}
	// A clean image still has a tool: it is a summary.
	if _, ok := ParseScanSummary(`{"tool":"trivy 0.76.2"}`); !ok {
		t.Error("a summary with no findings is not a summary")
	}
	for _, not := range []string{``, `  `, `OOMKilled`, `{"reason":"failed"}`, `[1]`} {
		if _, ok := ParseScanSummary(not); ok {
			t.Errorf("%q read as a summary", not)
		}
	}
}

func TestDBUpdatedAt(t *testing.T) {
	if got := DBUpdatedAt(strings.NewReader(`{"Version":2,"UpdatedAt":"2026-11-05T07:00:54.926777429+01:00","DownloadedAt":"x"}`)); got != "2026-11-05T06:00:54Z" {
		t.Errorf("got %q", got)
	}
	for _, bad := range []string{``, `{}`, `{"UpdatedAt":"yesterday"}`} {
		if got := DBUpdatedAt(strings.NewReader(bad)); got != "" {
			t.Errorf("%q: %q", bad, got)
		}
	}
}

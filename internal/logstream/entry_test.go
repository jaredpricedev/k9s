package logstream

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const (
	testFormatLogfmt = "logfmt"
	testFormatRaw    = "raw"
)

func TestParseJSONAndLogfmt(t *testing.T) {
	rt := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	e := Parse(Source{Pod: "p"}, rt, `{"level":"WARNING","message":"[red]hi","http":{"status":500},"id":9007199254740993,"time":"2020-01-01T00:00:00Z"}`)
	if e.Level != "warn" || e.Message != "[red]hi" || e.RuntimeTime != rt || e.ApplicationTime.Year() != 2020 {
		t.Fatalf("entry: %+v", e)
	}
	if n, ok := e.Field("id"); !ok || n != json.Number("9007199254740993") {
		t.Fatalf("numeric fidelity: %v", n)
	}
	if n, ok := e.Field("http.status"); !ok || n != json.Number("500") {
		t.Fatalf("nested: %v", n)
	}
	e = Parse(Source{}, rt, `level=ERR msg="hello\nworld" duration=12 ok=true`)
	if e.Format != testFormatLogfmt || e.Level != "error" || e.Message != "hello\nworld" {
		t.Fatalf("logfmt: %+v", e)
	}
	for _, raw := range []string{`{"x":1} garbage`, `level="unclosed`, `key=ok bare`, `ordinary [red] text`, `{"x":`} {
		if e := Parse(Source{}, rt, raw); e.Format != testFormatRaw || e.Raw != raw {
			t.Fatalf("fallback %q: %+v", raw, e)
		}
	}
}
func TestQueries(t *testing.T) {
	e := Parse(Source{}, time.Time{}, `{"level":"ERROR","message":"request failed","http":{"status":500},"ok":false,"n":9007199254740993}`)
	for _, expr := range []string{`level >= warn AND http.status >= 500`, `level=error ok=false`, `message="request failed"`, `n > 9007199254740992`, `failed`} {
		q, err := CompileQuery(expr, nil)
		if err != nil || !q.Match(e) {
			t.Fatalf("%q: %v", expr, err)
		}
	}
	for _, expr := range []string{`missing!=x`, `http.status < 500`, `level > error`, `ok=true`} {
		q, err := CompileQuery(expr, nil)
		if err != nil || q.Match(e) {
			t.Fatalf("%q: %v", expr, err)
		}
	}
	for _, expr := range []string{`level >`, `level=error AND`, `level===error`, `message="unclosed`, `[broken`, `level=error OR level=warn`} {
		if _, err := CompileQuery(expr, nil); err == nil {
			t.Fatalf("accepted invalid %q", expr)
		}
	}
	q, _ := CompileQuery(`failed`, []string{"request"})
	if q.Match(e) {
		t.Fatal("exclusion ignored")
	}
	q, _ = CompileQuery(`HTTP 500`, nil)
	if !q.Match(Parse(Source{}, time.Time{}, "HTTP 500")) {
		t.Fatal("raw regex mismatch")
	}
}
func TestSafeTextAndEntry(t *testing.T) {
	//nolint:gosec // Synthetic credential shapes are required to exercise the redactor realistically.
	raw := "\x1b[31mBearer secret.access.token [red]\x00\n-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\nAKIA1234567890123456 eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.signature"
	s := SafeText(raw)
	for _, secret := range []string{"secret.access.token", "abc", "AKIA1234567890123456", "eyJhbGci", "\x1b", "\x00"} {
		if strings.Contains(s, secret) {
			t.Fatalf("leak %q in %q", secret, s)
		}
	}
	if !strings.Contains(s, "[red]") {
		t.Fatal("domain must preserve UI markup literal")
	}
	e := Parse(Source{}, time.Time{}, `{"msg":"Bearer secret","nested":{"value":"Bearer other"}}`)
	safe := SafeEntry(e)
	if strings.Contains(safe.Raw, "Bearer secret") || strings.Contains(safe.Fields["msg"].(string), "secret") {
		t.Fatal("raw or fields leaked")
	}
	if !strings.Contains(e.Raw, "secret") {
		t.Fatal("mutated original")
	}
}
func TestClusterProvenance(t *testing.T) {
	a := Source{Cluster: "cluster-a", Context: "context"}
	b := Source{Cluster: "cluster-b", Context: "context"}
	if a.Key() == b.Key() {
		t.Fatal("cluster identity omitted")
	}
	e := Parse(a, time.Now(), "message")
	if !strings.Contains(Snippet([]Entry{e}, ExportContext{}), "cluster=cluster-a") {
		t.Fatal("cluster missing from snippet")
	}
	e.Source.Cluster = "Bearer cluster-secret"
	if strings.Contains(SafeEntry(e).Source.Cluster, "cluster-secret") {
		t.Fatal("cluster was not sanitized")
	}
}

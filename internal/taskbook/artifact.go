// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package taskbook retains portable read-only runbooks. Decoding and previewing
// are offline operations; execution is a separate, explicit boundary.
package taskbook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/provider"
	"github.com/derailed/k9s/internal/workspace"
	"k8s.io/apimachinery/pkg/util/validation"
)

const Version = 1
const ResourceGet = "resource-get"
const MaxBytes = 4 << 20

// Artifact keeps original selected evidence separate from latest explicit reads.
// No provider executable, imported argv, raw workspace objects or credentials
// are part of the execution contract.
type Artifact struct {
	Version   int               `json:"version"`
	CreatedAt time.Time         `json:"created_at"`
	Summary   string            `json:"summary"`
	Scopes    []workspace.Scope `json:"scopes,omitempty"`
	Evidence  inspect.Bundle    `json:"evidence"`
	Checks    []Check           `json:"checks"`
	Questions []string          `json:"questions,omitempty"`
	Links     []string          `json:"evidence_links,omitempty"`
	Limits    []string          `json:"limits"`
	Latest    []Result          `json:"latest_explicit_results,omitempty"`
}

type Check struct {
	ID          string `json:"id"`
	Observation int    `json:"observation"`
}

type Result struct {
	Check       Check                `json:"check"`
	ObservedAt  time.Time            `json:"observed_at"`
	State       string               `json:"state"`
	Source      string               `json:"source"`
	Detail      string               `json:"detail"`
	Observation *inspect.Observation `json:"observation,omitempty"`
}

//nolint:gocritic // Retained evidence is passed by value and copied at SafeBundle.
func New(summary string, evidence inspect.Bundle) (Artifact, error) {
	a := Artifact{Version: Version, CreatedAt: time.Now().UTC(), Summary: summary, Evidence: evidence,
		Limits: []string{"Offline replay retains original source/time/coverage; it is not current cluster state.",
			"Only explicitly requested fixed checks rerun. No writes, authentication, context switching or imported commands.",
			"API reads require saved UID; replacements are stale and their bodies excluded. Secret kinds excluded before reads.",
			"Logs/flows are selected frozen snippets with their own coverage; no automatic recapture. Redaction is heuristic; review before sharing."}}
	for i := range evidence.Observations {
		a.Checks = append(a.Checks, Check{ID: ResourceGet, Observation: i})
	}
	return Safe(a)
}

// Safe validates the complete artifact and returns independent sanitized data.
//
//nolint:gocritic // Value boundary intentionally owns an independent snapshot.
func Safe(a Artifact) (Artifact, error) {
	if a.Version != Version || a.CreatedAt.IsZero() {
		return Artifact{}, errors.New("unsupported taskbook version or missing creation time")
	}
	if len(a.Checks) > 16 || len(a.Scopes) > 8 || len(a.Questions) > 32 || len(a.Links) > 32 || len(a.Limits) > 32 || len(a.Latest) > 16 {
		return Artifact{}, errors.New("taskbook exceeds collection limits")
	}
	var err error
	a.Evidence, err = inspect.SafeBundle(a.Evidence)
	if err != nil {
		return Artifact{}, err
	}
	for _, o := range a.Evidence.Observations {
		if !validGVR.MatchString(o.Identity.GVR) ||
			len(validation.IsDNS1123Subdomain(o.Identity.Name)) != 0 ||
			(o.Identity.Namespace != "" && len(validation.IsDNS1123Label(o.Identity.Namespace)) != 0) {
			return Artifact{}, errors.New("invalid explicit resource identity")
		}
	}
	if len(a.Summary) > 4096 {
		return Artifact{}, errors.New("summary exceeds 4 KiB")
	}
	a.Summary = logstream.SafeText(a.Summary)
	if strings.TrimSpace(a.Summary) == "" {
		return Artifact{}, errors.New("task summary is required")
	}
	scopes := make([]workspace.Scope, len(a.Scopes))
	for i, s := range a.Scopes {
		scopes[i], err = workspace.NormalizeScope(s)
		if err != nil {
			return Artifact{}, err
		}
	}
	a.Scopes = scopes
	a.Checks = append([]Check(nil), a.Checks...)
	seen := map[Check]bool{}
	for _, c := range a.Checks {
		if !knownCheck(c.ID) || c.Observation < 0 || c.Observation >= len(a.Evidence.Observations) || seen[c] {
			return Artifact{}, errors.New("unknown, duplicate or out-of-scope task check")
		}
		seen[c] = true
	}
	clean := func(values []string) ([]string, error) {
		out := make([]string, len(values))
		for i, s := range values {
			if len(s) > 8192 {
				return nil, errors.New("task text exceeds 8 KiB")
			}
			out[i] = logstream.SafeText(s)
		}
		return out, nil
	}
	for _, values := range []*[]string{&a.Questions, &a.Links, &a.Limits} {
		*values, err = clean(*values)
		if err != nil {
			return Artifact{}, err
		}
	}
	a.Latest, err = safeResults(a.Latest, seen, a.Evidence.Observations)
	if err != nil {
		return Artifact{}, err
	}
	b, err := json.Marshal(a)
	if err != nil {
		return Artifact{}, err
	}
	if len(b) > MaxBytes {
		return Artifact{}, errors.New("taskbook exceeds 4 MiB")
	}
	return a, nil
}

var validGVR = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?/)?v[0-9]+(?:alpha[0-9]+|beta[0-9]+)?/[a-z][a-z0-9]*$`)

func knownCheck(id string) bool {
	switch id {
	case ResourceGet, "kubectl-version", "helm-version", "flux-version", "cilium-version":
		return true
	}
	return false
}

// Decode never reads a provider, API, linked path or recorded context.
func Decode(r io.Reader) (Artifact, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return Artifact{}, err
	}
	if len(b) > MaxBytes {
		return Artifact{}, errors.New("taskbook exceeds 4 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var a Artifact
	if err = d.Decode(&a); err != nil {
		return Artifact{}, errors.New("invalid taskbook JSON")
	}
	var tail any
	if d.Decode(&tail) != io.EOF {
		return Artifact{}, errors.New("unexpected content after taskbook")
	}
	return Safe(a)
}

func Read(path string) (Artifact, error) {
	if !filepath.IsAbs(path) {
		return Artifact{}, errors.New("choose an absolute taskbook path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Artifact{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxBytes {
		return Artifact{}, errors.New("taskbook must be a regular file of at most 4 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return Artifact{}, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return Artifact{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxBytes {
		return Artifact{}, errors.New("taskbook must be a regular file of at most 4 MiB")
	}
	return Decode(f)
}

// Save only creates a new private file, never overwriting an existing artifact.
//
//nolint:gocritic // Snapshot is independent of mutable caller data.
func Save(path string, a Artifact) error {
	if !filepath.IsAbs(path) || strings.ToLower(filepath.Ext(path)) != ".json" {
		return errors.New("choose a new absolute .json taskbook path")
	}
	safe, err := Safe(a)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(safe, "", "  ")
	if err != nil {
		return err
	}
	if len(b)+1 > MaxBytes {
		return errors.New("formatted taskbook exceeds 4 MiB")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(b, '\n'))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.Join(writeErr, closeErr)
	}
	return nil
}

// Preview has one scrollable column, meaningful without icons or colors.
//
//nolint:gocritic // Presentation owns sanitized snapshot values.
func Preview(a Artifact) (string, error) {
	a, err := Safe(a)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "TASK HANDOFF\n%s\nOffline retained evidence. r: explicit rerun; s: save; q/Esc: return.\nCreated: %s\n",
		a.Summary, a.CreatedAt.UTC().Format(time.RFC3339))
	for _, s := range a.Scopes {
		fmt.Fprintf(&b, "\nScope: %s context=%s namespaces=%s selector=%s kinds=%s\n",
			s.Name, s.Context, strings.Join(s.Namespaces, ","), s.LabelSelector, strings.Join(s.Kinds, ","))
	}
	for i, o := range a.Evidence.Observations {
		fmt.Fprintf(&b, "\nTarget %d: %s %s/%s\nContext: %s\nUID: %s\nRetained: %s\nSource: %s\nTime: %s\n%s\n",
			i, o.Identity.GVR, o.Identity.Namespace, o.Identity.Name, o.Identity.Context, o.Identity.UID,
			o.State, o.Source, o.ObservedAt.UTC().Format(time.RFC3339), o.Reason)
		for _, limit := range o.Limits {
			fmt.Fprintf(&b, "Coverage: %s\n", limit)
		}
	}
	b.WriteString("\nFIXED READ-ONLY CHECKS\n")
	for _, c := range a.Checks {
		fmt.Fprintf(&b, "%s (target %d)\n", c.ID, c.Observation)
	}
	b.WriteString("\nLATEST EXPLICIT RERUN (original evidence unchanged)\n")
	if len(a.Latest) == 0 {
		b.WriteString("Not rerun. Opening never runs checks.\n")
	}
	for _, r := range a.Latest {
		fmt.Fprintf(&b, "%s: %s\nSource: %s\nTime: %s\n%s\n", r.Check.ID, r.State, r.Source, r.ObservedAt.UTC().Format(time.RFC3339), r.Detail)
	}
	for _, section := range []struct {
		name   string
		values []string
	}{
		{"UNRESOLVED QUESTIONS", a.Questions},
		{"EVIDENCE LINKS (not opened automatically)", a.Links},
		{"REPLAY LIMITS", a.Limits},
		{"RETAINED NOTES", a.Evidence.Notes},
		{"EVIDENCE COVERAGE", a.Evidence.Limits},
	} {
		fmt.Fprintf(&b, "\n%s\n", section.name)
		for _, value := range section.values {
			fmt.Fprintln(&b, value)
		}
	}
	for _, s := range a.Evidence.Snippets {
		fmt.Fprintf(&b, "\nFROZEN EVIDENCE\nSource: %s\nTime: %s\nCoverage: %s\n%s\n", s.Source, s.ObservedAt.UTC().Format(time.RFC3339), s.Limits, s.Text)
	}
	for _, o := range a.Evidence.Observations {
		if o.Object != nil {
			encoded, err := json.MarshalIndent(o.Object, "", "  ")
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "\nRETAINED SNAPSHOT %s/%s\n%s\n", o.Identity.Namespace, o.Identity.Name, encoded)
		}
	}
	for _, r := range a.Latest {
		if r.Observation != nil && r.Observation.Object != nil {
			encoded, err := json.MarshalIndent(r.Observation.Object, "", "  ")
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "\nLATEST EXPLICIT SNAPSHOT %s/%s\n%s\n", r.Observation.Identity.Namespace, r.Observation.Identity.Name, encoded)
		}
	}
	if b.Len() > inspect.MaxComparisonText {
		return "", errors.New("task preview exceeds 256 KiB")
	}
	return b.String(), nil
}

func safeResults(results []Result, seen map[Check]bool, observations []inspect.Observation) ([]Result, error) {
	latest := make([]Result, len(results))
	resultSeen := map[Check]bool{}
	for i, r := range results {
		if !seen[r.Check] || resultSeen[r.Check] || r.ObservedAt.IsZero() || len(r.Source) > 4096 || len(r.Detail) > 8192 {
			return nil, errors.New("invalid latest check result")
		}
		resultSeen[r.Check] = true
		switch r.State {
		case inspect.ObservationComplete, inspect.ObservationDenied, inspect.ObservationStale, inspect.ObservationIncomplete, inspect.ObservationUnknown,
			"available", "absent", string(provider.Unavailable), "incompatible":
		default:
			return nil, errors.New("unknown result state")
		}
		r.Source = logstream.SafeText(r.Source)
		r.Detail = logstream.SafeText(r.Detail)
		if r.Observation != nil {
			o := inspect.SafeObservation(*r.Observation)
			if r.Check.ID != ResourceGet || o.State != r.State || o.ObservedAt.IsZero() {
				return nil, errors.New("latest observation check/state/time inconsistent")
			}
			expected := observations[r.Check.Observation].Identity
			if o.Identity != expected {
				return nil, errors.New("result identity differs from saved target")
			}
			if r.State != inspect.ObservationComplete {
				o.Object = nil
			}
			r.Observation = &o
		}
		latest[i] = r
	}
	return latest, nil
}

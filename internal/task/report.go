// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
// Package task provides bounded, linear read-only task reports.
package task

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/workspace"
)

const Version = 1
const MaxReportBytes = 4 << 20

type Coverage struct {
	Source string `json:"source"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

type Fact struct {
	Subject  string                    `json:"subject"`
	State    string                    `json:"state"`
	Detail   string                    `json:"detail,omitempty"`
	Resource *inspect.ResourceIdentity `json:"resource,omitempty"`
}

type Report struct {
	Version    int             `json:"version"`
	Task       string          `json:"task"`
	Context    string          `json:"context"`
	Namespaces []string        `json:"namespaces,omitempty"`
	Kinds      []string        `json:"kinds,omitempty"`
	Selector   string          `json:"selector,omitempty"`
	ObservedAt time.Time       `json:"observedAt"`
	Complete   bool            `json:"complete"`
	Coverage   []Coverage      `json:"coverage"`
	Facts      []Fact          `json:"facts"`
	Limits     []string        `json:"limits"`
	Evidence   *inspect.Bundle `json:"evidence,omitempty"`
}

// Workspace projects existing bounded collection results, excluding raw object
// bodies. Findings describe observations; incomplete coverage is never health.
//
//nolint:gocritic // Scope is an immutable public input; projection does not modify caller-owned fields.
func Workspace(scope workspace.Scope, snapshot workspace.Snapshot) Report {
	r := Report{Version: Version, Task: "workspace", Context: scope.Context, Namespaces: scope.Namespaces,
		Kinds: scope.Kinds, Selector: scope.LabelSelector, ObservedAt: snapshot.ObservedAt, Complete: true,
		Limits: []string{"Read-only snapshot; no historical claim", "Raw object bodies and Secret contents excluded",
			"Empty findings do not prove health when coverage is incomplete"}}
	for _, c := range snapshot.Coverage {
		r.Coverage = append(r.Coverage, Coverage{Source: c.GVR + " / " + c.Namespace, State: c.State, Detail: c.Detail})
		if c.State != "complete" || c.Truncated {
			r.Complete = false
		}
	}
	if len(snapshot.Coverage) == 0 {
		r.Complete = false
	}
	for index := range snapshot.Findings {
		finding := &snapshot.Findings[index]
		ref := finding.Ref
		identity := inspect.ResourceIdentity{Context: scope.Context, GVR: ref.GVR, Namespace: ref.Namespace, Name: ref.Name, UID: ref.UID}
		r.Facts = append(r.Facts, Fact{Subject: finding.Kind + " " + ref.Namespace + "/" + ref.Name,
			State: finding.Severity + " / " + finding.Reason, Detail: finding.Detail, Resource: &identity})
	}
	if len(r.Facts) == 0 {
		r.Facts = append(r.Facts, Fact{Subject: "Selected workspace", State: "No findings in collected evidence"})
	}
	return r
}

//nolint:gocritic // Observation is an immutable captured snapshot; the output retains independent safe evidence.
func Investigation(observation inspect.Observation, investigation *inspect.Investigation) Report {
	observation = inspect.SafeObservation(observation)
	r := Report{Version: Version, Task: "investigate", Context: observation.Identity.Context, Namespaces: []string{observation.Identity.Namespace},
		ObservedAt: observation.ObservedAt,
		Complete:   observation.State == inspect.ObservationComplete,
		Limits: append([]string{"Current state and last termination are separate observations; historical exit is not a current cause"},
			observation.Limits...)}
	r.Coverage = append(r.Coverage, Coverage{Source: observation.Source, State: observation.State, Detail: observation.Reason})
	if investigation != nil {
		for index := range investigation.Containers {
			c := &investigation.Containers[index]
			r.Facts = append(r.Facts, Fact{Subject: c.Role + " container " + c.Name, State: "CURRENT " + c.CurrentLabel(), Detail: c.Message})
			if c.LastTermination != nil {
				r.Facts = append(r.Facts, Fact{Subject: c.Name, State: "PREVIOUS " + c.LastTermination.Reason, Detail: c.LastTermination.Message})
			}
		}
		for _, c := range investigation.Conditions {
			r.Facts = append(r.Facts, Fact{Subject: "Condition " + c.Type, State: c.Status + " / " + c.Reason, Detail: c.Message})
		}
		for _, c := range investigation.Coverage {
			r.Coverage = append(r.Coverage, Coverage{Source: c.Source, State: c.State, Detail: c.Detail})
			if c.State != "complete" && c.State != "not collected" {
				r.Complete = false
			}
		}
		for _, e := range investigation.Events {
			r.Facts = append(r.Facts, Fact{Subject: "Event " + e.Reason, State: e.Type, Detail: e.Message})
		}
	}
	if len(r.Facts) == 0 {
		r.Facts = append(r.Facts, Fact{Subject: observation.Identity.Namespace + "/" + observation.Identity.Name, State: observation.State, Detail: observation.Reason})
	}
	identity := observation.Identity
	for index := range r.Facts {
		r.Facts[index].Resource = &identity
	}
	b := inspect.NewBundle([]inspect.Observation{observation})
	if safe, err := inspect.SafeBundle(b); err == nil {
		r.Evidence = &safe
	}
	return r
}

//nolint:gocritic // The public offline bundle value is sanitized into independent copies.
func Evidence(bundle inspect.Bundle) (Report, error) {
	safe, err := inspect.SafeBundle(bundle)
	if err != nil {
		return Report{}, err
	}
	r := Report{Version: Version, Task: "evidence", ObservedAt: safe.CreatedAt, Complete: true, Evidence: &safe,
		Limits: append([]string{"Offline retained evidence; no API requests or provider execution",
			"Observation timestamps describe original collection; replay does not make them current"}, safe.Limits...)}
	for index := range safe.Observations {
		o := &safe.Observations[index]
		if o.Identity.Namespace != "" && !slices.Contains(r.Namespaces, o.Identity.Namespace) {
			r.Namespaces = append(r.Namespaces, o.Identity.Namespace)
		}
		if r.Context == "" {
			r.Context = o.Identity.Context
		} else if r.Context != o.Identity.Context {
			r.Context = "multiple captured contexts"
		}
		r.Coverage = append(r.Coverage, Coverage{Source: o.Source, State: o.State, Detail: o.Reason})
		r.Facts = append(r.Facts, Fact{Subject: o.Identity.GVR + " " + o.Identity.Namespace + "/" + o.Identity.Name + " UID " + o.Identity.UID,
			State: o.State, Detail: "Observed " + o.ObservedAt.Format(time.RFC3339)})
		if o.State != inspect.ObservationComplete {
			r.Complete = false
		}
	}
	for _, note := range safe.Notes {
		r.Facts = append(r.Facts, Fact{Subject: "Operator note", State: "retained", Detail: note})
	}
	return r, nil
}

// Write sanitizes a private report copy before emitting any output.
//
//nolint:gocritic // This value API explicitly isolates the retained report; nested data is copied before sanitization.
func Write(w io.Writer, report Report, format string) error {
	if format != "text" && format != "json" {
		return fmt.Errorf("output must be text or json")
	}
	if err := sanitizeReport(&report); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if len(encoded)+1 > MaxReportBytes {
		return fmt.Errorf("report exceeds the %d byte output limit", MaxReportBytes)
	}
	if format == "json" {
		_, err = w.Write(append(encoded, '\n'))
		return err
	}
	return writeText(w, &report)
}

func sanitizeReport(report *Report) error {
	report.Namespaces = slices.Clone(report.Namespaces)
	report.Kinds = slices.Clone(report.Kinds)
	report.Coverage = slices.Clone(report.Coverage)
	report.Facts = slices.Clone(report.Facts)
	report.Limits = slices.Clone(report.Limits)
	if report.Evidence != nil {
		safe, err := inspect.SafeBundle(*report.Evidence)
		if err != nil {
			return err
		}
		report.Evidence = &safe
		for index := range safe.Observations {
			if safe.Observations[index].State != inspect.ObservationComplete {
				report.Complete = false
			}
		}
	}
	clean := logstream.SafeText
	report.Task = clean(report.Task)
	report.Context = clean(report.Context)
	report.Selector = clean(report.Selector)
	for index := range report.Kinds {
		report.Kinds[index] = clean(report.Kinds[index])
	}
	for index := range report.Namespaces {
		report.Namespaces[index] = clean(report.Namespaces[index])
	}
	for index := range report.Coverage {
		c := &report.Coverage[index]
		c.Source, c.State, c.Detail = clean(c.Source), clean(c.State), clean(c.Detail)
	}
	for index := range report.Facts {
		f := &report.Facts[index]
		f.Subject, f.State, f.Detail = clean(f.Subject), clean(f.State), clean(f.Detail)
		if f.Resource != nil {
			r := *f.Resource
			r.Context, r.GVR, r.Namespace, r.Name, r.UID = clean(r.Context), clean(r.GVR), clean(r.Namespace), clean(r.Name), clean(r.UID)
			f.Resource = &r
		}
	}
	for index := range report.Limits {
		report.Limits[index] = clean(report.Limits[index])
	}
	return nil
}

func writeText(w io.Writer, report *Report) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Task: %s\nContext: %s\nNamespaces: %s\nObserved: %s\nRequested evidence complete: %t\n",
		report.Task, report.Context, strings.Join(report.Namespaces, ", "), report.ObservedAt.Format(time.RFC3339), report.Complete)
	if len(report.Kinds) != 0 || report.Selector != "" {
		fmt.Fprintf(&b, "Kinds: %s\nSelector: %s\n", strings.Join(report.Kinds, ", "), report.Selector)
	}
	b.WriteString("\nCoverage\n")
	for _, c := range report.Coverage {
		fmt.Fprintf(&b, "%s: %s — %s\n", c.Source, c.State, c.Detail)
	}
	b.WriteString("\nObserved facts\n")
	for _, f := range report.Facts {
		fmt.Fprintf(&b, "%s: %s\n", f.Subject, f.State)
		if f.Resource != nil {
			fmt.Fprintf(&b, "  Resource: %s %s/%s UID %s\n", f.Resource.GVR, f.Resource.Namespace, f.Resource.Name, f.Resource.UID)
		}
		if f.Detail != "" {
			fmt.Fprintf(&b, "  %s\n", f.Detail)
		}
	}
	b.WriteString("\nEvidence limits\n")
	for _, limit := range report.Limits {
		fmt.Fprintf(&b, "- %s\n", limit)
	}
	if b.Len() > MaxReportBytes {
		return fmt.Errorf("report exceeds the %d byte output limit", MaxReportBytes)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

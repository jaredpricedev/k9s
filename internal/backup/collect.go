// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package backup

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const MaxObjects = 100
const MaxFieldBytes = 256
const MaxDeclaredItems = 32
const projectionPartial = "partial / projection bound"
const unreported = "Unreported"
const stateComplete = "complete"
const stateEmpty = "empty"
const ReadTimeout = 3 * time.Second

var Tabs = []string{"Overview", "Schedules", "Backups", "Restores", "Sources"}

type Scope struct{ Context, Namespace, ControllerNamespace string }
type Identity struct{ Context, GVR, Namespace, Name, UID, ResourceVersion string }
type Coverage struct {
	Source, State string
	ReadAt        time.Time
	Visible       int
}
type Record struct {
	Identity                                                                     Identity
	CreatedAt                                                                    *time.Time
	Kind, Phase, Schedule, Backup, Retention                                     string
	IncludesNamespaces, ExcludesNamespaces, IncludesResources, ExcludesResources []string
	Warnings, Errors, SnapshotsAttempted, SnapshotsCompleted                     *int64
}

// Snapshot retains only bounded declared fields, never raw API objects.
type Snapshot struct {
	Scope      Scope
	CapturedAt time.Time
	Records    []Record
	Coverage   []Coverage
}

func (s *Snapshot) Partial() bool {
	for _, c := range s.Coverage {
		if c.State != stateComplete && c.State != stateEmpty {
			return true
		}
	}
	return false
}

// CollectionFailed distinguishes a fresh failure from an observed empty page.
func (s *Snapshot) CollectionFailed() bool {
	for _, coverage := range s.Coverage {
		if coverage.State == stateComplete || coverage.State == stateEmpty || strings.HasPrefix(coverage.State, "partial") {
			return false
		}
	}
	return true
}
func bounded(value string) string {
	if len(value) > MaxFieldBytes {
		value = value[:MaxFieldBytes]
	}
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, value)
}
func scalar(obj *unstructured.Unstructured, path ...string) string {
	v, _, _ := unstructured.NestedString(obj.Object, path...)
	return bounded(v)
}
func list(obj *unstructured.Unstructured, path ...string) []string {
	values, _ := declaredProjection(obj, path...)
	return values
}

// Read only the retained prefix without copying an oversized API array.
func declaredProjection(obj *unstructured.Unstructured, path ...string) ([]string, bool) {
	raw, found, err := unstructured.NestedFieldNoCopy(obj.Object, path...)
	if !found {
		return nil, err != nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, true
	}
	out := make([]string, 0, min(len(values), MaxDeclaredItems))
	limited := len(values) > MaxDeclaredItems
	for _, rawValue := range values[:min(len(values), MaxDeclaredItems)] {
		value, ok := rawValue.(string)
		if !ok {
			limited = true
			continue
		}
		if len(value) > MaxFieldBytes {
			limited = true
		}
		out = append(out, bounded(value))
	}
	return out, limited
}
func count(obj *unstructured.Unstructured, path ...string) *int64 {
	v, ok, _ := unstructured.NestedInt64(obj.Object, path...)
	if !ok || v < 0 {
		return nil
	}
	return &v
}

// NamedAbsent requires a structured Kubernetes object-specific 404. Proxy 404s
// and collection endpoint failures do not establish absence.
func NamedAbsent(err error, gvr schema.GroupVersionResource, name string) bool {
	if apierrors.IsUnexpectedServerError(err) {
		return false
	}
	status, ok := err.(apierrors.APIStatus)
	if !ok {
		return false
	}
	s := status.Status()
	return s.Code == 404 && s.Reason == metav1.StatusReasonNotFound && s.Details != nil &&
		s.Details.Group == gvr.Group && s.Details.Kind == gvr.Resource && s.Details.Name == name
}
func errorState(err error) string {
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return "denied"
	}
	return "unknown / unavailable"
}

// Collect reads exactly one page per Velero v1 kind in the explicit controller
// namespace. Includes/excludes are declarations, not proof of app coverage.
func Collect(parent context.Context, reader dynamic.Interface, scope *Scope, now time.Time) *Snapshot {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	s := &Snapshot{Scope: *scope, CapturedAt: now}
	if scope.Context == "" || scope.Namespace == "" || scope.ControllerNamespace == "" {
		s.Coverage = []Coverage{{Source: "Scope", State: "unknown / explicit namespace required", ReadAt: now}}
		return s
	}
	for _, source := range []struct{ resource, kind string }{{"schedules", "Schedule"}, {"backups", "Backup"}, {"restores", "Restore"}} {
		gvr := schema.GroupVersionResource{Group: "velero.io", Version: "v1", Resource: source.resource}
		readCtx, stop := context.WithTimeout(ctx, ReadTimeout)
		page, err := reader.Resource(gvr).Namespace(scope.ControllerNamespace).List(readCtx, metav1.ListOptions{Limit: MaxObjects + 1})
		stop()
		c := Coverage{Source: source.kind, State: stateComplete, ReadAt: time.Now()}
		if err != nil {
			c.State = errorState(err)
			s.Coverage = append(s.Coverage, c)
			continue
		}
		if page == nil {
			c.State = "unknown / unavailable"
			s.Coverage = append(s.Coverage, c)
			continue
		}
		if len(page.Items) > MaxObjects || page.GetContinue() != "" || (page.GetRemainingItemCount() != nil && *page.GetRemainingItemCount() > 0) {
			c.State = "partial / retained page"
		}
		limit := min(len(page.Items), MaxObjects)
		for i := range limit {
			obj := &page.Items[i]
			if obj.GetAPIVersion() != "velero.io/v1" || obj.GetKind() != source.kind || obj.GetNamespace() != scope.ControllerNamespace ||
				obj.GetUID() == "" || obj.GetName() == "" ||
				len(obj.GetUID()) > MaxFieldBytes || len(obj.GetResourceVersion()) > MaxFieldBytes {
				c.State = "partial / identity rejected"
				continue
			}
			spec := []string{"spec"}
			if source.kind == "Schedule" {
				spec = append(spec, "template")
			}
			path := func(field string) []string { return append(append([]string{}, spec...), field) }
			for _, field := range []string{"includedNamespaces", "excludedNamespaces", "includedResources", "excludedResources"} {
				if _, limited := declaredProjection(obj, path(field)...); limited {
					c.State = projectionPartial
				}
			}
			for _, field := range [][]string{{"status", "phase"}, {"spec", "backupName"}, path("ttl"), {"spec", "schedule"}} {
				value, _, _ := unstructured.NestedString(obj.Object, field...)
				if len(value) > MaxFieldBytes {
					c.State = projectionPartial
				}
			}
			createdAt := obj.GetCreationTimestamp().Time
			var createdAtValue *time.Time
			if !createdAt.IsZero() {
				createdAt = createdAt.UTC()
				createdAtValue = &createdAt
			}
			r := Record{
				Identity: Identity{Context: scope.Context, GVR: gvr.String(), Namespace: obj.GetNamespace(),
					Name: bounded(obj.GetName()), UID: bounded(string(obj.GetUID())), ResourceVersion: bounded(obj.GetResourceVersion())},
				CreatedAt: createdAtValue, Kind: source.kind, Phase: scalar(obj, "status", "phase"),
				Schedule: bounded(obj.GetLabels()["velero.io/schedule-name"]), Backup: scalar(obj, "spec", "backupName"),
				Retention: scalar(obj, path("ttl")...), IncludesNamespaces: list(obj, path("includedNamespaces")...),
				ExcludesNamespaces: list(obj, path("excludedNamespaces")...), IncludesResources: list(obj, path("includedResources")...),
				ExcludesResources: list(obj, path("excludedResources")...), Warnings: count(obj, "status", "warnings"),
				Errors: count(obj, "status", "errors"), SnapshotsAttempted: count(obj, "status", "volumeSnapshotsAttempted"),
				SnapshotsCompleted: count(obj, "status", "volumeSnapshotsCompleted"),
			}
			if source.kind == "Schedule" {
				r.Schedule = scalar(obj, "spec", "schedule")
			}
			s.Records = append(s.Records, r)
			c.Visible++
		}
		if c.Visible == 0 && c.State == stateComplete {
			c.State = stateEmpty
		}
		s.Coverage = append(s.Coverage, c)
	}
	return s
}
func value(n *int64) string {
	if n == nil {
		return unreported
	}
	return fmt.Sprint(*n)
}
func (s *Snapshot) Render(tab int) string {
	var out strings.Builder
	if tab == 0 {
		fmt.Fprintf(&out, "Application namespace: %s\nVelero controller namespace: %s\n\n", s.Scope.Namespace, s.Scope.ControllerNamespace)
		fmt.Fprintln(&out, "Successful backup status does not prove recoverability.")
		fmt.Fprintln(&out, "Application restore-test evidence: Unreported.")
		fmt.Fprintln(&out, "Completed Restore reports controller workflow only.")
		fmt.Fprintln(&out, "Namespace/resource declarations require review; no application coverage inferred.")
		fmt.Fprintln(&out, "CSI/operator providers: Unreported; no agents or probes.")
		return out.String()
	}
	if tab == 4 {
		fmt.Fprintf(&out, "Captured %s\nContext %s | application %s | controller %s\n",
			s.CapturedAt.UTC().Format(time.RFC3339), s.Scope.Context, s.Scope.Namespace, s.Scope.ControllerNamespace)
		for _, c := range s.Coverage {
			fmt.Fprintf(&out, "%s: %s (%d retained, bound %d)\nAPI read %s\n", c.Source, c.State, c.Visible, MaxObjects, c.ReadAt.UTC().Format(time.RFC3339))
		}
		fmt.Fprintln(&out, "Single pages; no global latest claim. No storage-location or credential reads.")
		return out.String()
	}
	kind := Tabs[tab]
	kind = strings.TrimSuffix(kind, "s")
	for index := range s.Records {
		r := &s.Records[index]
		if r.Kind != kind {
			continue
		}
		createdAt := unreported
		if r.CreatedAt != nil {
			createdAt = r.CreatedAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(&out, "%s  phase=%s\nUID %s | RV %s\nCreated %s\n", r.Identity.Name, reported(r.Phase), r.Identity.UID, r.Identity.ResourceVersion, createdAt)
		fmt.Fprintf(&out, "Warnings %s | errors %s | snapshots %s/%s attempted/completed\n",
			value(r.Warnings), value(r.Errors), value(r.SnapshotsAttempted), value(r.SnapshotsCompleted))
		fmt.Fprintf(&out, "Namespace includes %s; excludes %s\nResource includes %s; excludes %s\nTTL %s\n",
			declared(r.IncludesNamespaces), declared(r.ExcludesNamespaces), declared(r.IncludesResources), declared(r.ExcludesResources), reported(r.Retention))
		if r.Schedule != "" {
			fmt.Fprintf(&out, "Schedule declaration/name: %s (name link; ownership unverified)\n", r.Schedule)
		}
		if r.Backup != "" {
			fmt.Fprintf(&out, "Backup name reference: %s (ownership unverified)\n", r.Backup)
		}
		fmt.Fprintln(&out)
	}
	if out.Len() == 0 {
		fmt.Fprintln(&out, "No retained records. Review Sources for denied/missing API coverage.")
	}
	return out.String()
}

func declared(values []string) string {
	if values == nil {
		return unreported
	}
	return fmt.Sprint(values)
}
func reported(value string) string {
	if value == "" {
		return unreported
	}
	return value
}

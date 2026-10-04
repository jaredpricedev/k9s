// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package inspect

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/logstream"
)

const (
	ObservationComplete   = "complete"
	ObservationDenied     = "denied"
	ObservationUnknown    = "unknown"
	ObservationIncomplete = "incomplete"
	ObservationStale      = "stale"
	MaxSnapshotBytes      = 1024 * 1024
	MaxSnapshotFields     = 10000
	MaxComparisonChanges  = 2000
	MaxComparisonText     = 256 * 1024
)

// ResourceIdentity labels a concrete observed resource, not a desired source.
type ResourceIdentity struct {
	Context   string `json:"context"`
	GVR       string `json:"gvr"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
}

// Observation retains only an independent, sanitized projection. Unknown or
// denied inputs are evidence of an unavailable observation, never empty state.
type Observation struct {
	Identity   ResourceIdentity `json:"identity"`
	Source     string           `json:"source"`
	ObservedAt time.Time        `json:"observedAt"`
	State      string           `json:"state"`
	Reason     string           `json:"reason,omitempty"`
	Limits     []string         `json:"limits,omitempty"`
	Object     map[string]any   `json:"object,omitempty"`
}

//nolint:gocritic // Capture a resource identity by value at the immutable observation boundary.
func NewObservation(identity ResourceIdentity, source string, at time.Time, object map[string]any) Observation {
	return SafeObservation(Observation{Identity: identity, Source: source, ObservedAt: at, State: ObservationComplete, Object: object})
}

// SafeObservation is also the shared boundary for evidence export/import. It
// never mutates caller maps, and never retains a partial large object as though
// it were a complete observation. Redaction is heuristic, not a secrecy proof.
//
//nolint:gocritic // Public value API returns an independent observation.
func SafeObservation(o Observation) Observation {
	o.Identity = ResourceIdentity{
		Context: safeLabel(o.Identity.Context), GVR: safeLabel(o.Identity.GVR),
		Namespace: safeLabel(o.Identity.Namespace), Name: safeLabel(o.Identity.Name), UID: safeLabel(o.Identity.UID),
	}
	o.Source, o.Reason = safeLabel(o.Source), safeLabel(o.Reason)
	if o.Source == "" {
		o.Source = "source unknown"
	}
	if o.State != ObservationComplete && o.State != ObservationDenied && o.State != ObservationIncomplete && o.State != ObservationStale {
		o.State = ObservationUnknown
	}
	limits := make([]string, 0, min(len(o.Limits), 16)+2)
	for _, limit := range o.Limits[:min(len(o.Limits), 16)] {
		limits = append(limits, safeLabel(limit))
	}
	o.Limits = limits
	if secretSnapshotKind(o.Object) || strings.HasSuffix(o.Identity.GVR, "/secrets") || o.Identity.GVR == "secrets" {
		o.Object = nil
		o.State = ObservationIncomplete
		o.Reason = "Secret content excluded by default"
		o.Limits = appendUnique(o.Limits, "Secret values are excluded from presentation, copy and export")
		return o
	}
	if o.Object == nil {
		if o.State == ObservationComplete {
			o.State = ObservationUnknown
			o.Reason = "No resource object was obtained"
		}
		return o
	}
	budget := snapshotBudget{}
	projection, ok := sanitizeSnapshotValue(o.Object, 0, &budget)
	if !ok {
		o.Object = nil
		o.State = ObservationIncomplete
		if budget.problem != "" {
			o.Reason = budget.problem
			o.Limits = appendUnique(o.Limits, "Snapshot excluded because sanitization would lose field identity")
			return o
		}
		o.Reason = "Resource exceeds snapshot limits"
		o.Limits = appendUnique(o.Limits, "Snapshot limits: 1 MiB, 10,000 fields, 32 levels, 64 KiB per string")
		return o
	}
	o.Object, _ = projection.(map[string]any)
	encoded, err := json.Marshal(o.Object)
	if err != nil || len(encoded) > MaxSnapshotBytes {
		o.Object = nil
		o.State = ObservationIncomplete
		o.Reason = "Resource exceeds snapshot limits or contains unsupported data"
		o.Limits = appendUnique(o.Limits, "Snapshot JSON limit: 1 MiB")
	}
	if budget.redacted {
		o.Limits = appendUnique(o.Limits, "Sensitive field names and recognizable credentials redacted heuristically")
	}
	return o
}

type snapshotBudget struct {
	bytes, fields int
	redacted      bool
	problem       string
}

func secretSnapshotKind(object map[string]any) bool {
	for key, value := range object {
		if logstream.SafeText(key) == "kind" && strings.EqualFold(logstream.SafeText(fmt.Sprint(value)), "Secret") {
			return true
		}
	}
	return false
}

func sensitiveSnapshotName(object map[string]any) bool {
	for key, value := range object {
		if logstream.SafeText(key) != "name" {
			continue
		}
		name := strings.ToLower(logstream.SafeText(fmt.Sprint(value)))
		if sensitiveSnapshotKey(name) || strings.Contains(name, "secret") {
			return true
		}
	}
	return false
}

func sanitizeSnapshotValue(v any, depth int, b *snapshotBudget) (any, bool) {
	b.fields++
	if depth > 32 || b.fields > MaxSnapshotFields || b.bytes > MaxSnapshotBytes {
		return nil, false
	}
	switch value := v.(type) {
	case map[string]any:
		if len(value) > MaxSnapshotFields-b.fields {
			return nil, false
		}
		if secretSnapshotKind(value) {
			b.redacted = true
			return "[SECRET CONTENT EXCLUDED]", true
		}
		result := make(map[string]any, min(len(value), MaxSnapshotFields))
		for key, child := range value {
			b.bytes += len(key)
			if len(key) > 4096 || b.bytes > MaxSnapshotBytes {
				return nil, false
			}
			safeKey := logstream.SafeText(key)
			if _, exists := result[safeKey]; exists {
				b.problem = "Resource field names collide after terminal sanitization"
				return nil, false
			}
			normalizedKey := strings.ToLower(safeKey)
			if sensitiveSnapshotKey(safeKey) ||
				(normalizedKey == "value" && sensitiveSnapshotName(value)) ||
				normalizedKey == "args" || normalizedKey == "command" {
				b.fields++
				result[safeKey] = "[REDACTED]"
				b.redacted = true
				continue
			}
			cleaned, ok := sanitizeSnapshotValue(child, depth+1, b)
			if !ok {
				return nil, false
			}
			result[safeKey] = cleaned
		}
		return result, true
	case []any:
		if len(value) > MaxSnapshotFields-b.fields {
			return nil, false
		}
		result := make([]any, len(value))
		for i, child := range value {
			cleaned, ok := sanitizeSnapshotValue(child, depth+1, b)
			if !ok {
				return nil, false
			}
			result[i] = cleaned
		}
		return result, true
	case string:
		b.bytes += len(value)
		if len(value) > 64*1024 || b.bytes > MaxSnapshotBytes {
			return nil, false
		}
		cleaned := logstream.SafeText(value)
		b.redacted = b.redacted || cleaned != value
		return cleaned, true
	case nil, bool, int, int32, int64, uint, uint32, uint64, float32, float64, json.Number:
		b.bytes += 32
		return value, b.bytes <= MaxSnapshotBytes
	default:
		return nil, false
	}
}

func sensitiveSnapshotKey(key string) bool {
	k := strings.ToLower(key)
	for _, sensitive := range []string{
		"api-key", "api_key", "apikey", "access-key", "access_key", "accesskey", "secret-key", "secret_key", "secretkey",
		"password", "passwd", "token", "credential", "privatekey", "private-key", "private_key",
		"client-secret", "clientsecret", "authorization", "last-applied-configuration",
	} {
		if strings.Contains(k, sensitive) {
			return true
		}
	}
	return k == "secret" || k == "secretvalue" || k == "secretdata" || k == "stringdata" || k == "data"
}

func safeLabel(s string) string {
	if len(s) > 4096 {
		s = s[:4096]
	}
	return logstream.SafeText(s)
}

func appendUnique(values []string, value string) []string {
	if !slices.Contains(values, value) {
		values = append(values, value)
	}
	return values
}

type Change struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"` // added, removed, changed
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

type Comparison struct {
	A, B       Observation
	Changes    []Change
	Omitted    []string
	Recreated  bool
	Comparable bool
	Truncated  bool
	Notice     string
}

var snapshotNoisePaths = []string{"/metadata/managedFields", "/metadata/resourceVersion", "/metadata/selfLink"}

// Compare never changes either observation. Normalization suppresses only the
// documented API bookkeeping paths; disabling it reveals those fields again.
//
//nolint:gocritic // Compare owns defensive snapshots of both arguments.
func Compare(a, b Observation, normalize bool) Comparison {
	c := Comparison{A: SafeObservation(a), B: SafeObservation(b)}
	aID, bID := c.A.Identity, c.B.Identity
	c.Recreated = aID.Context == bID.Context && aID.GVR == bID.GVR && aID.Namespace == bID.Namespace && aID.Name == bID.Name &&
		aID.UID != "" && bID.UID != "" && aID.UID != bID.UID
	if c.A.State != ObservationComplete || c.B.State != ObservationComplete || c.A.Object == nil || c.B.Object == nil {
		c.Notice = "Comparison unavailable: both inputs must be complete observations; unavailable data does not mean no changes"
		return c
	}
	c.Comparable = true
	var walk func(string, any, bool, any, bool)
	walk = func(path string, av any, hasA bool, bv any, hasB bool) {
		if c.Truncated {
			return
		}
		if normalize && slices.Contains(snapshotNoisePaths, path) {
			c.Omitted = append(c.Omitted, path)
			return
		}
		am, aMap := av.(map[string]any)
		bm, bMap := bv.(map[string]any)
		if aMap && bMap {
			keys := make([]string, 0, len(am)+len(bm))
			for key := range am {
				keys = append(keys, key)
			}
			for key := range bm {
				if _, ok := am[key]; !ok {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			for _, key := range keys {
				ac, aok := am[key]
				bc, bok := bm[key]
				walk(path+"/"+escapeJSONPointer(key), ac, aok, bc, bok)
			}
			return
		}
		if hasA == hasB && reflect.DeepEqual(av, bv) {
			return
		}
		if len(c.Changes) == MaxComparisonChanges {
			c.Truncated = true
			c.Notice = "Changes truncated at 2,000; this is an incomplete comparison"
			return
		}
		kind := "changed"
		if !hasA {
			kind = "added"
		} else if !hasB {
			kind = "removed"
		}
		c.Changes = append(c.Changes, Change{Path: path, Kind: kind, Before: av, After: bv})
	}
	walk("", c.A.Object, true, c.B.Object, true)
	return c
}

func escapeJSONPointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

//nolint:gocritic // The report consumes a value and never mutates its retained observations.
func ComparisonText(c Comparison) string {
	var out strings.Builder
	out.WriteString("RESOURCE OBSERVATION COMPARISON\n\n")
	for i, o := range []Observation{c.A, c.B} {
		label := "A · chosen baseline"
		if i == 1 {
			label = "B · comparison observation"
		}
		observed := "unknown"
		if !o.ObservedAt.IsZero() {
			observed = o.ObservedAt.UTC().Format(time.RFC3339Nano)
		}
		fmt.Fprintf(&out, "%s\n  Context: %s\n  Resource: %s %s/%s\n  UID: %s\n  Source: %s\n  Observed: %s\n  State: %s\n",
			label, o.Identity.Context, o.Identity.GVR, o.Identity.Namespace, o.Identity.Name,
			valueOrUnknown(o.Identity.UID), o.Source, observed, o.State)
		if o.Reason != "" {
			fmt.Fprintf(&out, "  Reason: %s\n", o.Reason)
		}
		for _, limit := range o.Limits {
			fmt.Fprintf(&out, "  Limit: %s\n", limit)
		}
		out.WriteString("\n")
	}
	if c.Recreated {
		out.WriteString("RECREATED IDENTITY: same context/resource/name, different UIDs. B is a replacement object.\n\n")
	}
	if !c.Comparable {
		out.WriteString(c.Notice + "\n")
		return boundedComparisonText(out.String())
	}
	if len(c.Omitted) != 0 {
		out.WriteString("API noise omitted (n reveals it): " + strings.Join(c.Omitted, ", ") + "\n\n")
	}
	if len(c.Changes) == 0 {
		out.WriteString("No differences in the retained compared fields. This is not a health or desired-state verdict.\n")
	}
	for _, change := range c.Changes {
		before, _ := json.Marshal(change.Before)
		after, _ := json.Marshal(change.After)
		entry := fmt.Sprintf("%s %s\n  A: %s\n  B: %s\n", strings.ToUpper(change.Kind), change.Path, before, after)
		if out.Len()+len(entry) > MaxComparisonText {
			out.WriteString("Display truncated at 256 KiB; additional changes are retained within comparison limits.\n")
			break
		}
		out.WriteString(entry)
	}
	if c.Notice != "" {
		out.WriteString(c.Notice + "\n")
	}
	out.WriteString("\nr captures B again; A stays fixed. n toggles API bookkeeping normalization.\n" +
		"Credential redaction is heuristic. No desired configuration source was selected.\n")
	return boundedComparisonText(out.String())
}

func boundedComparisonText(text string) string {
	if len(text) <= MaxComparisonText {
		return text
	}
	const suffix = "\nDisplay truncated at 256 KiB; this display is incomplete.\n"
	return strings.ToValidUTF8(text[:MaxComparisonText-len(suffix)], "") + suffix
}

func valueOrUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

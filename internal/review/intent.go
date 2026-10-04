// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
	"k8s.io/apimachinery/pkg/api/resource"
)

// CompareIntent reviews only explicitly authored fields. It is a local
// projection, not a server-side apply, admission, ownership or prune preview.
// Raw values never cross its result boundary; redacted fields are unreviewed.
//
//nolint:gocritic // Manifest is a small immutable source descriptor.
func CompareIntent(manifest Manifest, live map[string]any) IntentResult {
	r := IntentResult{Unreviewed: []string{
		"Live-only fields and omitted resources are not proposed for removal",
		"Server defaulting, admission and field ownership are not evaluated",
	}}
	if manifest.SecretExcluded || strings.EqualFold(manifest.Kind, "Secret") {
		r.Unreviewed = append(r.Unreviewed, "Secret content excluded")
		return r
	}
	desiredSafe := inspect.NewObservation(inspect.ResourceIdentity{}, "authored", time.Time{}, manifest.Object)
	liveProjection := live
	if liveProjection == nil {
		liveProjection = map[string]any{}
	}
	liveSafe := inspect.NewObservation(inspect.ResourceIdentity{}, "live", time.Time{}, liveProjection)
	if desiredSafe.State != inspect.ObservationComplete || liveSafe.State != inspect.ObservationComplete {
		r.Truncated = true
		r.Unreviewed = append(r.Unreviewed, "Object could not be safely projected within comparison limits")
		return r
	}
	w := intentWalker{result: &r, manifest: manifest}
	for _, notice := range r.Unreviewed {
		w.bytes += len(notice)
	}
	w.walk("", manifest.Object, desiredSafe.Object, live, liveSafe.Object, true)
	return r
}

type intentWalker struct {
	result   *IntentResult
	manifest Manifest
	fields   int
	bytes    int
}

func (w *intentWalker) omit(message string) {
	if len(w.result.Unreviewed) < 128 && !slices.Contains(w.result.Unreviewed, message) {
		message = logstream.SafeText(message)
		if w.retainText(len(message)) {
			w.result.Unreviewed = append(w.result.Unreviewed, message)
		}
	}
}

func (w *intentWalker) retainText(size int) bool {
	if size > inspect.MaxComparisonText-w.bytes {
		w.result.Truncated = true
		return false
	}
	w.bytes += size
	return true
}

func ignoredIntentPath(path string) bool {
	if path == "/status" || path == "/apiVersion" || path == "/kind" {
		return true
	}
	for _, field := range []string{
		"name", "namespace", "uid", "resourceVersion", "generation", "creationTimestamp",
		"deletionTimestamp", "deletionGracePeriodSeconds", "managedFields", "selfLink",
	} {
		if path == "/metadata/"+field {
			return true
		}
	}
	return false
}

func intentPointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func (w *intentWalker) walk(path string, authored, safeAuthored, live, safeLive any, present bool) {
	if ignoredIntentPath(path) || w.result.Truncated {
		return
	}
	if len(path) > 4096 {
		w.omit("Authored path exceeds the display identity limit")
		w.result.Truncated = true
		return
	}
	w.fields++
	if w.fields > inspect.MaxSnapshotFields {
		w.result.Truncated = true
		w.omit("Comparison field limit reached")
		return
	}
	if object, ok := authored.(map[string]any); ok {
		w.walkObject(path, object, safeAuthored, live, safeLive, present)
		return
	}
	if values, ok := authored.([]any); ok {
		safeValues, safeOK := safeAuthored.([]any)
		if !safeOK {
			w.omit(path + ": sensitive or excluded fields")
			return
		}
		if w.namedList(path) {
			liveValues, _ := live.([]any)
			safeLiveValues, _ := safeLive.([]any)
			if w.walkNamedList(path, values, safeValues, liveValues, safeLiveValues) {
				return
			}
		}
		w.omit(path + ": list merge and replacement semantics are not evaluated")
	}
	if excludedIntentMarker(safeAuthored) || (present && excludedIntentMarker(safeLive)) ||
		!reflect.DeepEqual(authored, safeAuthored) || (present && !reflect.DeepEqual(live, safeLive)) {
		w.omit(path + ": redacted or sanitized value not compared")
		return
	}
	w.result.DeclaredFields++
	if (!present && authored == nil) || (present && w.equalValue(path, safeAuthored, safeLive)) {
		w.result.MatchedFields++
		return
	}
	if len(w.result.Changes) == MaxChanges {
		w.result.Truncated = true
		w.omit("Changes truncated at 2,000")
		return
	}
	kind := "change"
	if !present {
		kind = "set"
	}
	before := "[absent]"
	if present {
		before = intentValue(safeLive)
	}
	after := intentValue(safeAuthored)
	if w.retainText(len(path) + len(kind) + len(before) + len(after)) {
		w.result.Changes = append(w.result.Changes, Change{Path: path, Kind: kind, Before: before, After: after})
	}
}

func (w *intentWalker) walkObject(path string, object map[string]any, safeAuthored, live, safeLive any, present bool) {
	safeObject, safeOK := safeAuthored.(map[string]any)
	if !safeOK {
		w.omit(path + ": sensitive or excluded fields")
		return
	}
	liveObject, liveOK := live.(map[string]any)
	safeLiveObject, safeLiveOK := safeLive.(map[string]any)
	if present && liveOK && !safeLiveOK {
		w.omit(path + ": live fields excluded")
		return
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		safeKey := logstream.SafeText(key)
		if safeKey != key {
			w.omit("Field with unsafe name excluded")
			continue
		}
		value, hasLive := liveObject[key]
		w.walk(path+"/"+intentPointer(key), object[key], safeObject[key], value, safeLiveObject[key], present && liveOK && hasLive)
	}
}

func excludedIntentMarker(value any) bool {
	switch v := value.(type) {
	case string:
		return v == "[REDACTED]" || v == "[SECRET CONTENT EXCLUDED]"
	case []any:
		for _, item := range v {
			if excludedIntentMarker(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if excludedIntentMarker(item) {
				return true
			}
		}
	}
	return false
}

func intentValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "[unavailable]"
	}
	if len(encoded) > 4096 {
		return "[value exceeds display limit]"
	}
	return logstream.SafeText(string(encoded))
}

func (w *intentWalker) nativePodSpec() string {
	switch w.manifest.APIVersion + "/" + w.manifest.Kind {
	case "v1/Pod":
		return "/spec"
	case "apps/v1/Deployment", "apps/v1/StatefulSet", "apps/v1/DaemonSet", "apps/v1/ReplicaSet",
		"batch/v1/Job", "v1/ReplicationController":
		return "/spec/template/spec"
	case "batch/v1/CronJob":
		return "/spec/jobTemplate/spec/template/spec"
	}
	return ""
}

func (w *intentWalker) namedList(path string) bool {
	base := w.nativePodSpec()
	if base == "" || !strings.HasPrefix(path, base+"/") {
		return false
	}
	relative := strings.TrimPrefix(path, base+"/")
	for _, field := range []string{"containers", "initContainers", "ephemeralContainers", "volumes", "imagePullSecrets"} {
		if relative == field {
			return true
		}
	}
	parts := strings.Split(relative, "/")
	return len(parts) == 3 && slices.Contains([]string{"containers", "initContainers", "ephemeralContainers"}, parts[0]) &&
		slices.Contains([]string{"env", "volumeMounts", "volumeDevices"}, parts[2])
}

func namedIntentIndex(values []any, field string) (map[string]int, bool) {
	index := make(map[string]int, len(values))
	for i, value := range values {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		name, ok := object[field].(string)
		if !ok || name == "" || logstream.SafeText(name) != name {
			return nil, false
		}
		if _, duplicate := index[name]; duplicate {
			return nil, false
		}
		index[name] = i
	}
	return index, true
}

func (w *intentWalker) walkNamedList(path string, authored, safeAuthored, live, safeLive []any) bool {
	field := "name"
	if strings.HasSuffix(path, "/volumeMounts") {
		field = "mountPath"
	} else if strings.HasSuffix(path, "/volumeDevices") {
		field = "devicePath"
	}
	authoredIndex, authoredOK := namedIntentIndex(authored, field)
	liveIndex, liveOK := namedIntentIndex(live, field)
	if !authoredOK || !liveOK || len(authored) != len(safeAuthored) || len(live) != len(safeLive) {
		return false
	}
	for key := range liveIndex {
		if _, authoredEntry := authoredIndex[key]; !authoredEntry {
			w.omit(path + ": live-only named entries are not proposed for removal")
			break
		}
	}
	for i, value := range authored {
		name := value.(map[string]any)[field].(string)
		livePosition, exists := liveIndex[name]
		var current, safeCurrent any
		if exists {
			current, safeCurrent = live[livePosition], safeLive[livePosition]
		}
		w.walk(path+"/"+strconv.Itoa(i), value, safeAuthored[i], current, safeCurrent, exists)
	}
	return true
}

func (w *intentWalker) equalValue(path string, authored, live any) bool {
	if (w.nativePodSpec() != "" || w.manifest.APIVersion+"/"+w.manifest.Kind == "v1/PersistentVolumeClaim") &&
		(strings.Contains(path, "/resources/requests/") || strings.Contains(path, "/resources/limits/")) {
		a, aOK := intentQuantity(authored)
		b, bOK := intentQuantity(live)
		if aOK && bOK {
			qa, ea := resource.ParseQuantity(a)
			qb, eb := resource.ParseQuantity(b)
			if ea == nil && eb == nil {
				return qa.Cmp(qb) == 0
			}
		}
	}
	a, aErr := json.Marshal(authored)
	b, bErr := json.Marshal(live)
	return aErr == nil && bErr == nil && bytes.Equal(a, b)
}

func intentQuantity(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case int, int32, int64, uint, uint32, uint64, float32, float64, json.Number:
		return fmt.Sprint(v), true
	default:
		return "", false
	}
}

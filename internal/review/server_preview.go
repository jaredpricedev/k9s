// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

const PreviewAccepted = "admission accepted"
const PreviewConflict = "field conflict"
const PreviewFieldManager = "k9plus-preview"

type ServerPreviewEntry struct {
	Identity               Identity
	State, Reason, Request string
	ObservedAt             time.Time
	Admission              IntentResult
	Projection             inspect.Comparison
	Ownership              []string
}
type ServerPreview struct {
	Source     SourceIdentity
	Scope      Scope
	ObservedAt time.Time
	Entries    []ServerPreviewEntry
}

// PreviewServer is a separately invoked admission/defaulting preview. Every
// create/SSA request uses DryRunAll; acceptance describes this request at this
// time, never a persisted write, future apply, healthy rollout or prune plan.
//
//nolint:gocritic // Source and scope are captured for this explicit request, independent of the workspace.
func PreviewServer(ctx context.Context, reader dynamic.Interface, resolve Resolver, source Source, scope Scope, now time.Time) ServerPreview {
	scope = copyScope(&scope)
	out := ServerPreview{Source: source.Identity, Scope: scope, ObservedAt: now}
	ctx, cancel := context.WithTimeout(ctx, CollectionTimeout)
	defer cancel()
	selector, selectorErr := labels.Parse(scope.LabelSelector)
	prepared := prepareTargets(ctx, resolve, &source, &scope, selector, selectorErr)
	for i := range prepared {
		current := &prepared[i]
		entry := ServerPreviewEntry{Identity: current.entry.Identity, State: current.entry.State, Reason: current.entry.Reason, ObservedAt: now}
		if entry.State == "" {
			entry = previewTarget(ctx, reader, current, selector, now)
		}
		out.Entries = append(out.Entries, entry)
	}
	return out
}
func previewTarget(ctx context.Context, reader dynamic.Interface, current *resolvedManifest, selector labels.Selector, at time.Time) ServerPreviewEntry {
	entry := ServerPreviewEntry{Identity: current.entry.Identity, ObservedAt: at}
	if reader == nil || ctx.Err() != nil {
		entry.State, entry.Reason = StateUnknown, "Server preview unavailable or canceled"
		return entry
	}
	named := boundedGet(ctx, reader, current.mapping.GVR, current.manifest.Namespace, current.manifest.Name)
	create := apierrors.IsNotFound(named.err) && current.captured == ""
	if named.err != nil && !create {
		readFailure(current, named.err)
		entry.State, entry.Reason = current.entry.State, current.entry.Reason
		return entry
	}
	live := named.object
	if !create {
		if !liveMatches(&current.manifest, live) || live.GetUID() == "" || live.GetResourceVersion() == "" {
			entry.State, entry.Reason = StateUnknown, "Named live identity or resourceVersion could not be verified"
			return entry
		}
		if current.captured != "" && live.GetUID() != current.captured {
			entry.State, entry.Reason = StateStale, "Live UID changed; reopen scope before previewing replacement"
			return entry
		}
		if current.captured == "" && !selector.Matches(labels.Set(live.GetLabels())) {
			entry.State, entry.Reason = StateOutScope, "Live labels are outside the captured selector"
			return entry
		}
		entry.Identity.UID = live.GetUID()
		entry.Ownership = ownershipMarkers(live)
	}
	object := &unstructured.Unstructured{Object: deepCopyManifest(current.manifest.Object)}
	object.SetName(current.manifest.Name)
	object.SetNamespace(current.manifest.Namespace)
	// Source-provided server-managed metadata never chooses a different identity.
	for _, field := range []string{
		"uid", "resourceVersion", "managedFields", "creationTimestamp", "generation", "deletionTimestamp", "deletionGracePeriodSeconds", "selfLink",
	} {
		unstructured.RemoveNestedField(object.Object, "metadata", field)
	}
	unstructured.RemoveNestedField(object.Object, "status")
	resource := reader.Resource(current.mapping.GVR).Namespace(current.manifest.Namespace)
	requestCtx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	var admitted *unstructured.Unstructured
	var err error
	if create {
		entry.Request = "create dry-run"
		admitted, err = boundedPreview(requestCtx, func() (*unstructured.Unstructured, error) {
			return resource.Create(requestCtx, object, metav1.CreateOptions{
				DryRun: []string{metav1.DryRunAll}, FieldManager: PreviewFieldManager, FieldValidation: metav1.FieldValidationStrict,
			})
		})
	} else {
		entry.Request = "server-side apply dry-run"
		object.SetUID(live.GetUID())
		object.SetResourceVersion(live.GetResourceVersion())
		var payload []byte
		payload, err = json.Marshal(object.Object)
		if err == nil {
			admitted, err = boundedPreview(requestCtx, func() (*unstructured.Unstructured, error) {
				return resource.Patch(requestCtx, current.manifest.Name, types.ApplyPatchType, payload, metav1.PatchOptions{
					DryRun: []string{metav1.DryRunAll}, FieldManager: PreviewFieldManager, FieldValidation: metav1.FieldValidationStrict,
				})
			})
		}
	}
	if requestCtx.Err() != nil {
		err = requestCtx.Err()
	}
	if err != nil {
		entry.State, entry.Reason = previewFailure(err)
		return entry
	}
	if !liveMatches(&current.manifest, admitted) || !create && admitted.GetUID() != live.GetUID() {
		entry.State, entry.Reason = StateUnknown, "Dry-run response identity differed; result rejected"
		return entry
	}
	entry.State = PreviewAccepted
	entry.Reason = "Admission/defaulting accepted this dry-run at the shown time; no resource was persisted. " +
		"Other changes, pruning and controller outcomes remain unknown"
	entry.Admission = CompareIntent(current.manifest, admitted.Object)
	a, b := map[string]any{}, previewProjectionObject(admitted.Object)
	if live != nil {
		a = previewProjectionObject(live.Object)
	}
	identity := inspect.ResourceIdentity{
		Context: entry.Identity.Context, GVR: entry.Identity.GVR.String(), Namespace: entry.Identity.Namespace,
		Name: entry.Identity.Name, UID: string(entry.Identity.UID),
	}
	entry.Projection = inspect.Compare(inspect.NewObservation(identity, "live before dry-run", at, a),
		inspect.NewObservation(identity, "server dry-run response", at, b), false)
	return entry
}
func deepCopyManifest(object map[string]any) map[string]any {
	var copyValue func(any) any
	copyValue = func(value any) any {
		switch value := value.(type) {
		case map[string]any:
			out := make(map[string]any, len(value))
			for key, item := range value {
				out[key] = copyValue(item)
			}
			return out
		case []any:
			out := make([]any, len(value))
			for i, item := range value {
				out[i] = copyValue(item)
			}
			return out
		default:
			return value
		}
	}
	return copyValue(object).(map[string]any)
}

func boundedPreview(ctx context.Context, invoke func() (*unstructured.Unstructured, error)) (*unstructured.Unstructured, error) {
	results := make(chan namedResult, 1)
	go func() { object, err := invoke(); results <- namedResult{object: object, err: err} }()
	select {
	case result := <-results:
		return result.object, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Server bookkeeping and controller status are not a predicted future identity
// or rollout outcome. Retain target identity separately from projected fields.
func previewProjectionObject(object map[string]any) map[string]any {
	out := deepCopyManifest(object)
	unstructured.RemoveNestedField(out, "status")
	for _, field := range []string{"uid", "resourceVersion", "managedFields", "creationTimestamp", "generation", "selfLink"} {
		unstructured.RemoveNestedField(out, "metadata", field)
	}
	return out
}

func previewFailure(err error) (state, reason string) {
	state = StateUnknown
	reason = "Dry-run request failed; no persisted write was requested"
	switch {
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		state = StateDenied
		reason = "Dry-run create/patch permission denied; named-read permission does not grant preview permission"
	case apierrors.IsConflict(err):
		state = PreviewConflict
		reason = "Field ownership or identity conflict; Force was not requested"
	case apierrors.IsInvalid(err) || apierrors.IsBadRequest(err):
		reason = "Server validation or admission rejected this dry-run"
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		reason = "Dry-run canceled or timed out; no persisted write was requested"
	}
	// API diagnostics can quote authored confidential fields; retain classification only.
	return state, reason
}

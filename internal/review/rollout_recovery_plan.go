// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

var recoveryDeploymentGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
var recoveryRevisionGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}

const recoveryPrepared = "server preview accepted; not executed"

// RolloutRecoveryPlan presents a safe server dry-run comparison while its
// private core pins exact source bytes and optimistic-concurrency preconditions.
// It restores the selected native Deployment Pod template only; it does not
// emulate kubectl's Deployment annotation copying or historical config recovery.
type RolloutRecoveryPlan struct {
	Identity, RevisionIdentity                                        inspect.ResourceIdentity
	Generation                                                        int64
	ResourceVersion, RevisionResourceVersion                          string
	SourceTemplateSHA256, BeforeTemplateSHA256, PreviewTemplateSHA256 string
	PreparedAt                                                        time.Time
	State, Reason                                                     string
	Unreviewed                                                        bool
	OwnershipMarkers                                                  []string
	Before, After                                                     inspect.Observation
	Comparison                                                        inspect.Comparison
	core                                                              recoveryPlanCore
}

type recoveryPlanCore struct {
	target, source                                       inspect.ResourceIdentity
	generation                                           int64
	targetRV, sourceRV, beforeSHA, sourceSHA, previewSHA string
	patch                                                []byte
}

type RolloutRecoveryHooks struct {
	BeforeWrite func(context.Context)
	Accepted    func(context.Context, string)
}

type RolloutRecoveryAcceptance struct {
	Identity                      inspect.ResourceIdentity
	AcceptedAt                    time.Time
	Generation                    int64
	TemplateSHA256, State, Reason string
}

// PrepareRolloutRecovery is explicitly invoked, never browsing work. It reads
// two named identities and submits only a DryRunAll JSON patch. Missing, removed,
// replaced or changed captured sources fail before dry-run admission.
func PrepareRolloutRecovery(ctx context.Context, reader dynamic.Interface, snapshot *RolloutSnapshot, revisionUID string) (*RolloutRecoveryPlan, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	core, err := recoveryCapturedSource(snapshot, revisionUID)
	if err != nil || reader == nil {
		return nil, errors.New("Native Deployment identity, captured resource versions and source template are required; refresh review")
	}
	before, revision, err := readRecoverySources(ctx, reader, &core)
	if err != nil {
		return nil, err
	}
	template, _, _ := unstructured.NestedMap(revision.Object, "spec", "template")
	rolloutRemoveHash(template)
	if core.beforeSHA == core.sourceSHA {
		return nil, errors.New("Selected template already matches the current Deployment; no recovery request needed")
	}
	core.patch, err = recoveryJSONPatch(&core, template)
	if err != nil {
		return nil, err
	}
	preview, err := recoveryPatch(ctx, reader, &core, true)
	if err != nil {
		return nil, recoveryReadError(err)
	}
	if !recoveryObjectMatches(preview, core.target, rolloutDeployment) {
		return nil, errors.New("Server preview identity could not be verified; nothing persisted")
	}
	core.previewSHA = rolloutTemplateDigest(preview)
	if core.previewSHA == "" {
		return nil, errors.New("Server preview template unavailable; nothing persisted")
	}
	at := time.Now().UTC()
	plan := &RolloutRecoveryPlan{Identity: core.target, RevisionIdentity: core.source, Generation: core.generation,
		ResourceVersion: core.targetRV, RevisionResourceVersion: core.sourceRV, SourceTemplateSHA256: core.sourceSHA,
		BeforeTemplateSHA256: core.beforeSHA, PreviewTemplateSHA256: core.previewSHA, PreparedAt: at,
		State: recoveryPrepared, Reason: "Selected Pod template JSON patch admitted in this dry-run; no persistent change submitted", core: core}
	plan.OwnershipMarkers = recoveryOwnershipMarkers(before)
	plan.Before = rolloutTemplateObservation(before, core.target, at, false)
	plan.After = rolloutTemplateObservation(preview, core.target, at, false)
	plan.Comparison = inspect.Compare(plan.Before, plan.After, false)
	if !plan.Comparison.Comparable || plan.Comparison.Truncated {
		return nil, errors.New("Server preview comparison incomplete; recovery execution unavailable")
	}
	plan.Unreviewed = recoveryRedacted(&plan.Before) || recoveryRedacted(&plan.After)
	return plan, nil
}

func recoveryCapturedSource(s *RolloutSnapshot, uid string) (recoveryPlanCore, error) {
	var c recoveryPlanCore
	if s == nil || s.Kind != rolloutDeployment || s.Identity.GVR != "apps/v1/deployments" || s.Identity.UID == "" ||
		!validReviewName(s.Identity.Name) || !validReviewName(s.Identity.Namespace) || s.Generation == nil || *s.Generation <= 0 ||
		s.ResourceVersion == "" || len(s.TemplateSHA256) != 64 || s.DeploymentTemplate.State != inspect.ObservationComplete {
		return c, errors.New("Captured Deployment evidence unavailable")
	}
	for i := range s.Revisions {
		r := &s.Revisions[i]
		if r.Identity.UID != uid || uid == "" {
			continue
		}
		if r.Identity.Context != s.Identity.Context || r.Identity.Namespace != s.Identity.Namespace || r.Identity.GVR != "apps/v1/replicasets" ||
			!validReviewName(r.Identity.Name) || r.ResourceVersion == "" || len(r.TemplateSHA256) != 64 || r.Template.State != inspect.ObservationComplete {
			break
		}
		return recoveryPlanCore{target: s.Identity, source: r.Identity, generation: *s.Generation, targetRV: s.ResourceVersion,
			sourceRV: r.ResourceVersion, beforeSHA: s.TemplateSHA256, sourceSHA: r.TemplateSHA256}, nil
	}
	return c, errors.New("Exact selected retained revision unavailable")
}

func readRecoverySources(
	ctx context.Context, reader dynamic.Interface, c *recoveryPlanCore,
) (currentObject, revisionObject *unstructured.Unstructured, err error) {
	current := boundedGet(ctx, reader, recoveryDeploymentGVR, c.target.Namespace, c.target.Name)
	if current.err != nil {
		return nil, nil, recoveryReadError(current.err)
	}
	if !recoveryObjectMatches(current.object, c.target, rolloutDeployment) || current.object.GetResourceVersion() != c.targetRV ||
		current.object.GetGeneration() != c.generation || rolloutTemplateDigest(current.object) != c.beforeSHA {
		return nil, nil, errors.New("Deployment identity/version/generation/template changed; refresh and prepare again; no recovery submitted")
	}
	revision := boundedGet(ctx, reader, recoveryRevisionGVR, c.source.Namespace, c.source.Name)
	if revision.err != nil {
		return nil, nil, recoveryReadError(revision.err)
	}
	if !recoveryObjectMatches(revision.object, c.source, rolloutReplicaSet) || revision.object.GetResourceVersion() != c.sourceRV ||
		!rolloutOwnedBy(revision.object, rolloutDeployment, c.target.UID) || rolloutTemplateDigest(revision.object) != c.sourceSHA {
		return nil, nil, errors.New("Selected revision identity/version/owner/template changed; refresh and explicitly choose again; no recovery submitted")
	}
	return current.object, revision.object, nil
}

//nolint:gocritic // Compare a captured identity without modifying it.
func recoveryObjectMatches(o *unstructured.Unstructured, id inspect.ResourceIdentity, kind string) bool {
	return o != nil && o.GetAPIVersion() == rolloutAppsAPI && o.GetKind() == kind && o.GetName() == id.Name &&
		o.GetNamespace() == id.Namespace && string(o.GetUID()) == id.UID && id.UID != ""
}

func recoveryJSONPatch(c *recoveryPlanCore, template map[string]any) ([]byte, error) {
	patch, err := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": c.target.UID},
		{"op": "test", "path": "/metadata/resourceVersion", "value": c.targetRV},
		{"op": "test", "path": "/metadata/generation", "value": c.generation},
		{"op": "replace", "path": "/spec/template", "value": template},
	})
	if err != nil || len(patch) > MaxSourceBytes {
		return nil, errors.New("Selected template exceeds bounded native recovery limits")
	}
	return patch, nil
}

func recoveryRedacted(o *inspect.Observation) bool {
	for _, limit := range o.Limits {
		if strings.Contains(limit, "redacted") {
			return true
		}
	}
	return false
}

type recoveryError struct {
	cause   error
	message string
}

func (e *recoveryError) Error() string { return e.message }
func (e *recoveryError) Unwrap() error { return e.cause }

func recoveryReadError(err error) error {
	message := "Recovery API request unavailable; raw server diagnostics withheld"
	switch {
	case errors.Is(err, context.Canceled):
		message = "Recovery request canceled; persistent acceptance may be unknown if a write was attempted"
	case errors.Is(err, context.DeadlineExceeded):
		message = "Recovery request deadline reached; persistent acceptance may be unknown if a write was attempted"
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		message = "Recovery API request denied"
	case apierrors.IsNotFound(err):
		message = "Captured Deployment or selected revision disappeared; refresh and choose again"
	case apierrors.IsConflict(err):
		message = "Recovery API precondition conflict; refresh and prepare again; no automatic retry"
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		message = "Recovery server validation rejected the request; raw field values withheld"
	}
	return &recoveryError{cause: err, message: message}
}

// ApplyRolloutRecovery rechecks both named sources and submits the exact preview
// payload once. Hook integration belongs to the guarded operation runner.
func ApplyRolloutRecovery(ctx context.Context, reader dynamic.Interface, plan *RolloutRecoveryPlan, hooks RolloutRecoveryHooks) (*RolloutRecoveryAcceptance, error) {
	if plan == nil || reader == nil || plan.State != recoveryPrepared || len(plan.core.patch) == 0 {
		return nil, errors.New("Explicit server-previewed native recovery plan required")
	}
	c := plan.core
	if _, _, err := readRecoverySources(ctx, reader, &c); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, recoveryReadError(err)
	}
	if hooks.BeforeWrite != nil {
		hooks.BeforeWrite(ctx)
	}
	result, err := recoveryPatch(ctx, reader, &c, false)
	if err != nil {
		return nil, recoveryReadError(err)
	}
	if hooks.Accepted != nil {
		hooks.Accepted(ctx, "selected native Deployment Pod-template recovery API acknowledgment")
	}
	accepted := &RolloutRecoveryAcceptance{Identity: c.target, AcceptedAt: time.Now().UTC(), State: "accepted; controller outcome not yet observed"}
	if !recoveryObjectMatches(result, c.target, rolloutDeployment) || result.GetGeneration() <= c.generation {
		return accepted, &recoveryError{cause: io.ErrUnexpectedEOF,
			message: "Recovery API acknowledged; resulting identity/generation unavailable; inspect captured destination before retrying"}
	}
	accepted.Generation, accepted.TemplateSHA256 = result.GetGeneration(), rolloutTemplateDigest(result)
	if accepted.TemplateSHA256 == "" || accepted.TemplateSHA256 != c.previewSHA {
		return accepted, &recoveryError{cause: io.ErrUnexpectedEOF,
			message: "Recovery API acknowledged; resulting template differs from server preview; outcome unknown; inspect before retrying"}
	}
	accepted.Reason = "Persistent patch acknowledged separately from controller progression or completion"
	return accepted, nil
}

func recoveryPatch(ctx context.Context, reader dynamic.Interface, c *recoveryPlanCore, dryRun bool) (*unstructured.Unstructured, error) {
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	opts := metav1.PatchOptions{FieldValidation: "Strict"}
	if dryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	result := make(chan namedResult, 1)
	go func() {
		o, err := reader.Resource(recoveryDeploymentGVR).Namespace(c.target.Namespace).Patch(ctx, c.target.Name, types.JSONPatchType, slices.Clone(c.patch), opts)
		result <- namedResult{object: o, err: err}
	}()
	select {
	case response := <-result:
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return response.object, response.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Metadata signals possible reconciliation; it does not prove controller ownership.
// Values are withheld and the fixed key set bounds retained output.
func recoveryOwnershipMarkers(object *unstructured.Unstructured) []string {
	labels, annotations := object.GetLabels(), object.GetAnnotations()
	var markers []string
	for _, marker := range []struct {
		name string
		keys []string
	}{
		{"Managed-by label", []string{"app.kubernetes.io/managed-by"}},
		{"Argo CD tracking", []string{"argocd.argoproj.io/instance", "argocd.argoproj.io/tracking-id"}},
		{"Flux Kustomization tracking", []string{"kustomize.toolkit.fluxcd.io/name", "kustomize.toolkit.fluxcd.io/namespace"}},
		{"Flux HelmRelease tracking", []string{"helm.toolkit.fluxcd.io/name", "helm.toolkit.fluxcd.io/namespace"}},
		{"Helm release tracking", []string{"meta.helm.sh/release-name", "meta.helm.sh/release-namespace"}},
	} {
		for _, key := range marker.keys {
			if labels[key] != "" || annotations[key] != "" {
				markers = append(markers, marker.name+" present (unverified metadata)")
				break
			}
		}
	}
	return markers
}

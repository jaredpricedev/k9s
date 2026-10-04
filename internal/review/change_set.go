// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/gitops"
	"github.com/derailed/k9s/internal/inspect"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

const (
	ChangeSetPrepared     = "preview accepted; not submitted"
	ChangeSetBlocked      = "source/controller review required"
	ChangeSetFieldManager = "k9plus-change-set"
	ChangeSetPlanLifetime = 5 * time.Minute
)

// OwnershipReader must use captured native clients and honor ctx. It observes
// only a bounded named-reference graph; it does not prove a resource unmanaged.
type OwnershipReader func(context.Context, *inspect.ResourceIdentity) (*gitops.Snapshot, error)

// ChangeSetPlan exposes safe evidence. The execution core is private and pins
// immutable payloads, source fingerprints and identity/ownership preconditions.
// Editing display fields cannot change an executable target or payload.
type ChangeSetPlan struct {
	Source                SourceIdentity
	Scope                 Scope
	PreparedAt, ExpiresAt time.Time
	Entries               []ChangeSetEntry
	core                  *changeSetCore
}

type ChangeSetEntry struct {
	Identity                                Identity
	Document                                int
	State, Reason, Request, ResourceVersion string
	Generation                              int64
	ExpectedAbsent                          bool
	Intent, Admission                       IntentResult
	Projection                              inspect.Comparison
	Ownership                               *gitops.Snapshot
	Policy                                  gitops.Policy
	PreparedAt                              time.Time
	LiveReadAt, PreviewReadAt               time.Time
}

type changeSetCore struct {
	source    SourceIdentity
	expiresAt time.Time
	entries   []changeSetTarget
	mu        sync.Mutex
}

type changeSetTarget struct {
	manifest                                Manifest
	identity                                Identity
	rv, graphSHA, metadataSHA, admissionSHA string
	generation                              int64
	expiresAt                               time.Time
	expectedAbsent, eligible, claimed       bool
	payload                                 *unstructured.Unstructured
}

type ChangeSetHooks struct {
	BeforeWrite func(context.Context)
	Accepted    func(context.Context, string)
}

// ChangeSetAcceptance keeps API acceptance separate from its later named read.
// OBSERVED means the admitted object fields were independently observed at the
// captured identity. It does not mean controller completion or runtime adoption.
type ChangeSetAcceptance struct {
	Identity                                   Identity
	Generation                                 int64
	ResourceVersion                            string
	AcceptedAt                                 time.Time
	ObservedGeneration                         int64
	ObservedResourceVersion, ObservationSource string
	ObservedAt                                 time.Time
	Observed                                   bool
}

var ErrChangeSetOutcomeUnknown = errors.New("accepted change-set outcome unknown; inspect the captured destination before retrying")

// PrepareChangeSet is called only after explicit dry-run confirmation. Every
// admission request is strict and DryRunAll, using the eventual field manager.
// No pruning, force ownership transfer or persisted write is requested.
//
//nolint:gocritic // Source/scope are immutable input descriptors, copied into the private plan.
func PrepareChangeSet(parent context.Context, reader dynamic.Interface, resolve Resolver,
	owner OwnershipReader, source Source, scope Scope,
) (*ChangeSetPlan, error) {
	if reader == nil || resolve == nil || owner == nil {
		return nil, errors.New("captured native read/admission/ownership clients required")
	}
	ctx, cancel := context.WithTimeout(parent, CollectionTimeout)
	defer cancel()
	if err := VerifyRetainedSource(ctx, &source.Identity); err != nil {
		return nil, errors.New("source fingerprint unavailable or changed; explicitly reload and review the source")
	}
	scope = copyScope(&scope)
	now := time.Now().UTC()
	plan := &ChangeSetPlan{Source: source.Identity, Scope: scope, PreparedAt: now, ExpiresAt: now.Add(ChangeSetPlanLifetime)}
	plan.Source.Options = slices.Clone(source.Identity.Options)
	plan.core = &changeSetCore{source: plan.Source, expiresAt: plan.ExpiresAt}
	selector, selectorErr := labels.Parse(scope.LabelSelector)
	prepared := prepareTargets(ctx, resolve, &source, &scope, selector, selectorErr)
	for index := range prepared {
		current := &prepared[index]
		entry := ChangeSetEntry{Identity: current.entry.Identity, Document: current.entry.Document,
			State: current.entry.State, Reason: current.entry.Reason, PreparedAt: now}
		target := changeSetTarget{identity: entry.Identity}
		if entry.State == "" {
			prepareChangeSetTarget(ctx, reader, owner, current, selector, &entry, &target)
		}
		plan.Entries = append(plan.Entries, entry)
		plan.core.entries = append(plan.core.entries, target)
	}
	if err := VerifyRetainedSource(ctx, &plan.core.source); err != nil {
		return nil, errors.New("source changed during preview or collection was canceled; no executable plan retained")
	}
	return plan, nil
}

func prepareChangeSetTarget(ctx context.Context, reader dynamic.Interface, owner OwnershipReader,
	current *resolvedManifest, selector labels.Selector, entry *ChangeSetEntry, target *changeSetTarget,
) {
	manifest := current.manifest
	manifest.Object = deepCopyManifest(manifest.Object)
	target.manifest = manifest
	entry.Policy = gitops.DeclaredRouting(manifest.Object)
	if entry.Policy.Route != gitops.RouteNative {
		entry.State, entry.Reason = ChangeSetBlocked, entry.Policy.Reason
		return
	}
	live, err := changeSetGet(ctx, reader, &entry.Identity)
	entry.LiveReadAt = time.Now().UTC()
	create := verifiedChangeSetAbsence(err, &entry.Identity) && current.captured == ""
	if err != nil && !create {
		entry.State, entry.Reason = changeSetFailure(err, "Named live precondition could not be established")
		return
	}
	entry.ExpectedAbsent, target.expectedAbsent = create, create
	if !create {
		if !liveMatches(&manifest, live) || live.GetUID() == "" || live.GetResourceVersion() == "" ||
			current.captured != "" && current.captured != live.GetUID() {
			entry.State, entry.Reason = StateStale, "Captured live UID/API identity unavailable or replaced; reopen review"
			return
		}
		if current.captured == "" && !selector.Matches(labels.Set(live.GetLabels())) {
			entry.State, entry.Reason = StateOutScope, "Current labels are outside the captured selector"
			return
		}
		entry.Identity.UID = live.GetUID()
		entry.ResourceVersion, entry.Generation = live.GetResourceVersion(), live.GetGeneration()
		entry.Ownership, err = changeSetOwnership(ctx, owner, &entry.Identity)
		if err != nil {
			entry.State, entry.Reason = StateUnknown, "Ownership observation unavailable; direct application blocked"
			return
		}
		entry.Policy = gitops.Routing(entry.Ownership)
		if entry.Policy.Route != gitops.RouteNative {
			entry.State, entry.Reason = ChangeSetBlocked, entry.Policy.Reason
			return
		}
		target.graphSHA = gitops.OwnershipFingerprint(entry.Ownership)
		target.metadataSHA = gitops.DeclaredOwnershipFingerprint(live.Object)
		if target.graphSHA == "" || target.metadataSHA == "" {
			entry.State, entry.Reason = StateUnknown, "Ownership metadata malformed or fingerprint unavailable; direct admission/application blocked"
			return
		}
	}
	var before map[string]any
	if live != nil {
		before = live.Object
	}
	entry.Intent = CompareIntent(manifest, before)
	if entry.Intent.Truncated {
		entry.State, entry.Reason = StateUnknown, "Intent projection incomplete; reduce the source before execution"
		return
	}
	payload := changeSetPayload(&manifest, live)
	target.identity, target.rv, target.generation = entry.Identity, entry.ResourceVersion, entry.Generation
	target.payload = payload
	admitted, err := changeSetWrite(ctx, reader, target, true)
	entry.PreviewReadAt = time.Now().UTC()
	if err != nil {
		entry.State, entry.Reason = changeSetAdmissionFailure(err, live)
		return
	}
	if !liveMatches(&manifest, admitted) || !create && admitted.GetUID() != entry.Identity.UID {
		entry.State, entry.Reason = StateUnknown, "Dry-run response identity differed; result rejected"
		return
	}
	if policy := gitops.DeclaredRouting(admitted.Object); policy.Route != gitops.RouteNative {
		entry.Policy, entry.State, entry.Reason = policy, ChangeSetBlocked, "Admission introduced management/owner metadata; review the source/controller before applying"
		return
	}
	finishChangeSetPreview(&manifest, live, admitted, entry, target)
}

func finishChangeSetPreview(manifest *Manifest, live, admitted *unstructured.Unstructured, entry *ChangeSetEntry, target *changeSetTarget) {
	entry.Request = "strict server-side apply"
	if target.expectedAbsent {
		entry.Request = "strict create (reviewed named absence)"
	}
	entry.Admission = CompareIntent(*manifest, admitted.Object)
	a := map[string]any{}
	if live != nil {
		a = previewProjectionObject(live.Object)
	}
	id := changeSetInspectIdentity(&entry.Identity)
	entry.Projection = inspect.Compare(inspect.NewObservation(id, "named live before change-set dry-run", entry.LiveReadAt, a),
		inspect.NewObservation(id, "strict change-set dry-run response; not persisted", entry.PreviewReadAt, previewProjectionObject(admitted.Object)), false)
	if !entry.Projection.Comparable || entry.Projection.Truncated || entry.Admission.Truncated {
		entry.State, entry.Reason = StateUnknown, "Admission projection incomplete; executable preview rejected"
		return
	}
	target.admissionSHA = changeSetObjectSHA(admitted)
	target.eligible = target.admissionSHA != "" && (target.expectedAbsent || target.graphSHA != "" && target.metadataSHA != "")
	if !target.eligible {
		entry.State, entry.Reason = StateUnknown, "Captured source/ownership/admission preconditions incomplete"
		return
	}
	entry.State, entry.Reason = ChangeSetPrepared, "Dry-run accepted at this time only; explicit apply rechecks preconditions. Batch operations are not atomic"
	if target.expectedAbsent {
		entry.Reason += ". A later apply may require separate field-ownership review after this creation"
	}
}

func changeSetAdmissionFailure(err error, live *unstructured.Unstructured) (state, reason string) {
	state, reason = changeSetFailure(err, "Strict dry-run admission failed; nothing persisted")
	if state != PreviewConflict || live == nil || !apierrors.HasStatusCause(err, metav1.CauseTypeFieldManagerConflict) {
		return state, reason
	}
	for _, field := range live.GetManagedFields() {
		if field.Manager == ChangeSetFieldManager && field.Operation == metav1.ManagedFieldsOperationUpdate {
			reason = "Field ownership conflict. A prior native create uses Update ownership and can conflict with this apply. " +
				"Review field ownership separately; no automatic migration, Force or retry"
			break
		}
	}
	return state, reason
}

func changeSetPayload(manifest *Manifest, live *unstructured.Unstructured) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: deepCopyManifest(manifest.Object)}
	object.SetName(manifest.Name)
	object.SetNamespace(manifest.Namespace)
	for _, field := range []string{previewUID, previewResourceVersion, previewManagedFields, previewCreationTimestamp,
		previewGeneration, "deletionTimestamp", "deletionGracePeriodSeconds", previewSelfLink} {
		unstructured.RemoveNestedField(object.Object, collectMetadata, field)
	}
	unstructured.RemoveNestedField(object.Object, "status")
	if live != nil {
		object.SetUID(live.GetUID())
		object.SetResourceVersion(live.GetResourceVersion())
	}
	return object
}

func changeSetGet(parent context.Context, reader dynamic.Interface, identity *Identity) (*unstructured.Unstructured, error) {
	ctx, cancel := context.WithTimeout(parent, ReadTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	object, err := reader.Resource(identity.GVR).Namespace(identity.Namespace).Get(ctx, identity.Name, metav1.GetOptions{})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return object, err
}

func changeSetWrite(parent context.Context, reader dynamic.Interface, target *changeSetTarget, dryRun bool) (*unstructured.Unstructured, error) {
	ctx, cancel := context.WithTimeout(parent, ReadTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var dry []string
	if dryRun {
		dry = []string{metav1.DryRunAll}
	}
	resource := reader.Resource(target.identity.GVR).Namespace(target.identity.Namespace)
	var object *unstructured.Unstructured
	var err error
	if target.expectedAbsent {
		object, err = resource.Create(ctx, target.payload.DeepCopy(), metav1.CreateOptions{
			DryRun: dry, FieldManager: ChangeSetFieldManager, FieldValidation: metav1.FieldValidationStrict})
	} else {
		payload, marshalErr := json.Marshal(target.payload.Object)
		if marshalErr != nil {
			return nil, errors.New("captured apply payload unavailable")
		}
		object, err = resource.Patch(ctx, target.identity.Name, types.ApplyPatchType, payload, metav1.PatchOptions{
			DryRun: dry, FieldManager: ChangeSetFieldManager, FieldValidation: metav1.FieldValidationStrict})
	}
	if ctx.Err() != nil && (dryRun || err != nil || object == nil) {
		return object, ctx.Err()
	}
	return object, err
}

func changeSetOwnership(ctx context.Context, owner OwnershipReader, identity *Identity) (*gitops.Snapshot, error) {
	request := changeSetInspectIdentity(identity)
	snapshot, err := owner(ctx, &request)
	if err != nil || ctx.Err() != nil || snapshot == nil || snapshot.Request.Target != request || len(snapshot.Nodes) == 0 || snapshot.Nodes[0].Identity != request {
		return nil, errors.New("bounded ownership graph did not match captured identity")
	}
	return snapshot, nil
}

func changeSetInspectIdentity(identity *Identity) inspect.ResourceIdentity {
	return inspect.ResourceIdentity{Context: identity.Context, GVR: identity.GVR.GroupVersion().String() + "/" + identity.GVR.Resource,
		Namespace: identity.Namespace, Name: identity.Name, UID: string(identity.UID)}
}

func verifiedChangeSetAbsence(err error, identity *Identity) bool {
	if err == nil || apierrors.IsUnexpectedServerError(err) {
		return false
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return false
	}
	value := status.Status()
	return value.Code == 404 && value.Reason == metav1.StatusReasonNotFound && value.Details != nil &&
		value.Details.Name == identity.Name && value.Details.Group == identity.GVR.Group && value.Details.Kind == identity.GVR.Resource
}

func changeSetObjectSHA(object *unstructured.Unstructured) string {
	if object == nil {
		return ""
	}
	data, err := json.Marshal(previewProjectionObject(object.Object))
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// Target returns the private captured execution identity. The bool is false for
// blocked, expired or already claimed entries, regardless of display mutations.
func (p *ChangeSetPlan) Target(index int) (Identity, bool) {
	if p == nil || p.core == nil {
		return Identity{}, false
	}
	p.core.mu.Lock()
	defer p.core.mu.Unlock()
	if index < 0 || index >= len(p.core.entries) {
		return Identity{}, false
	}
	target := &p.core.entries[index]
	return target.identity, target.eligible && !target.claimed && time.Now().Before(p.core.expiresAt)
}

// ApplyChangeSetTarget claims a reviewed target once. It never retries an
// uncertain write or executes blocked entries. Caller authorization and an
// explicit native confirmation precede this call. Navigation does not change
// these captured clients, payloads or optimistic-concurrency preconditions.
func ApplyChangeSetTarget(ctx context.Context, reader dynamic.Interface, owner OwnershipReader,
	plan *ChangeSetPlan, index int, hooks ChangeSetHooks,
) (*ChangeSetAcceptance, error) {
	target, source, err := claimChangeSetTarget(plan, index)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithDeadline(ctx, target.expiresAt)
	defer cancel()
	if reader == nil || owner == nil || ctx.Err() != nil {
		return nil, changeSetSafeError(ctx.Err(), "Captured native clients unavailable or operation canceled before submission")
	}
	if sourceErr := VerifyRetainedSource(ctx, &source); sourceErr != nil {
		return nil, errors.New("Source changed or fingerprint unavailable; no write submitted, prepare a new change set")
	}
	if preconditionErr := checkChangeSetPreconditions(ctx, reader, owner, target); preconditionErr != nil {
		return nil, preconditionErr
	}
	if sourceErr := VerifyRetainedSource(ctx, &source); sourceErr != nil {
		return nil, errors.New("Source changed during preflight; no write submitted, prepare a fresh change set")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if hooks.BeforeWrite != nil {
		hooks.BeforeWrite(ctx)
	}
	accepted, err := changeSetWrite(ctx, reader, target, false)
	if err != nil {
		return nil, changeSetSafeError(err, "Persistent request failed; no automatic retry. Inspect the captured destination")
	}
	return observeChangeSetAccepted(ctx, reader, target, accepted, hooks)
}

func observeChangeSetAccepted(ctx context.Context, reader dynamic.Interface, target *changeSetTarget,
	accepted *unstructured.Unstructured, hooks ChangeSetHooks,
) (*ChangeSetAcceptance, error) {
	at := time.Now().UTC()
	result := &ChangeSetAcceptance{Identity: target.identity, AcceptedAt: at}
	if accepted != nil {
		result.Identity.UID, result.ResourceVersion, result.Generation = accepted.GetUID(), accepted.GetResourceVersion(), accepted.GetGeneration()
	}
	if hooks.Accepted != nil {
		hooks.Accepted(ctx, fmt.Sprintf("%s %s/%s · UID %s · RV %s · generation %d · API accepted %s",
			target.identity.Kind, target.identity.Namespace, target.identity.Name, result.Identity.UID,
			result.ResourceVersion, result.Generation, at.Format(time.RFC3339Nano)))
	}
	if !liveMatches(&target.manifest, accepted) || result.Identity.UID == "" || result.ResourceVersion == "" ||
		!target.expectedAbsent && result.Identity.UID != target.identity.UID || changeSetObjectSHA(accepted) != target.admissionSHA {
		return result, ErrChangeSetOutcomeUnknown
	}
	if ctx.Err() != nil {
		return result, errors.Join(ErrChangeSetOutcomeUnknown, ctx.Err())
	}
	observed, err := changeSetGet(ctx, reader, &result.Identity)
	if err != nil {
		return result, errors.Join(ErrChangeSetOutcomeUnknown, changeSetSafeError(err, "Independent post-write observation unavailable"))
	}
	if !liveMatches(&target.manifest, observed) || observed.GetUID() != result.Identity.UID || observed.GetGeneration() != result.Generation ||
		observed.GetResourceVersion() == "" || changeSetObjectSHA(observed) != target.admissionSHA {
		return result, ErrChangeSetOutcomeUnknown
	}
	result.Observed, result.ObservedAt = true, time.Now().UTC()
	result.ObservedResourceVersion, result.ObservedGeneration = observed.GetResourceVersion(), observed.GetGeneration()
	result.ObservationSource = "independent named API GET; admitted object fields, no controller/runtime readiness assertion"
	return result, nil
}

func claimChangeSetTarget(plan *ChangeSetPlan, index int) (*changeSetTarget, SourceIdentity, error) {
	if plan == nil || plan.core == nil {
		return nil, SourceIdentity{}, errors.New("Reviewed change-set plan unavailable")
	}
	core := plan.core
	core.mu.Lock()
	defer core.mu.Unlock()
	if index < 0 || index >= len(core.entries) || !core.entries[index].eligible || core.entries[index].claimed || time.Now().After(core.expiresAt) {
		return nil, SourceIdentity{}, errors.New("Target blocked, preview expired or already submitted; prepare a fresh change set")
	}
	core.entries[index].claimed = true
	target := core.entries[index]
	target.expiresAt = core.expiresAt
	return &target, core.source, nil
}

func checkChangeSetPreconditions(ctx context.Context, reader dynamic.Interface, owner OwnershipReader, target *changeSetTarget) error {
	live, err := changeSetGet(ctx, reader, &target.identity)
	if target.expectedAbsent {
		if !verifiedChangeSetAbsence(err, &target.identity) {
			return changeSetSafeError(err, "Reviewed absence changed or became unverified; create was not submitted")
		}
		return nil
	}
	if err != nil {
		return changeSetSafeError(err, "Named live precondition unavailable; no write submitted")
	}
	if !liveMatches(&target.manifest, live) || live.GetUID() != target.identity.UID || live.GetResourceVersion() != target.rv || live.GetGeneration() != target.generation ||
		gitops.DeclaredOwnershipFingerprint(live.Object) != target.metadataSHA {
		return errors.New("Live UID/resourceVersion/generation or management metadata changed; prepare a fresh preview")
	}
	graph, err := changeSetOwnership(ctx, owner, &target.identity)
	if err != nil || gitops.Routing(graph).Route != gitops.RouteNative || gitops.OwnershipFingerprint(graph) != target.graphSHA {
		return errors.New("Ownership evidence changed or became incomplete; review the source/controller before native application")
	}
	return ctx.Err()
}

type changeSetClassifiedError struct {
	reason string
	cause  error
}

func (e *changeSetClassifiedError) Error() string { return e.reason }
func (e *changeSetClassifiedError) Unwrap() error { return e.cause }

func changeSetSafeError(err error, reason string) error {
	return &changeSetClassifiedError{reason: reason, cause: err}
}

func changeSetFailure(err error, fallback string) (state, reason string) {
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return StateDenied, "Named read or dry-run permission denied; no persisted write requested"
	case apierrors.IsConflict(err):
		return PreviewConflict, "Identity or field ownership conflict; Force was not requested"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return StateUnknown, "Preview canceled or timed out; nothing persisted"
	default:
		return StateUnknown, fallback
	}
}

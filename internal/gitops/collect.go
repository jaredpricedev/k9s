// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package gitops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var ErrAPIAbsent = errors.New("requested API absent")

type traversal struct {
	object       *unstructured.Unstructured
	index, depth int
}

type collector struct {
	reader   Reader
	snapshot *Snapshot
	seen     map[string]int
	queue    []traversal
	reads    int
}

// Collect performs named reads only after an explicit review command. Clients
// must honor ctx; native REST adapters apply these deadlines to transport calls.
// Returned projections contain no raw manifests, Secret values or diagnostics.
func Collect(parent context.Context, reader Reader, request *Request) (*Snapshot, error) {
	if request == nil || reader == nil {
		return nil, errors.New("Captured resource identity and native reader required")
	}
	captured := *request
	request = &captured
	gvr, err := capturedGVR(&request.Target)
	if err != nil || request.Target.Context == "" || request.Target.UID == "" || !validName(request.Target.Name) ||
		(request.Target.Namespace != "" && !validNamespace(request.Target.Namespace)) ||
		(request.ArgoNamespace != "" && !validNamespace(request.ArgoNamespace)) {
		return nil, errors.New("Captured context/GVR/namespace/name/UID or explicit Argo namespace invalid")
	}
	ctx, cancel := context.WithTimeout(parent, CollectTimeout)
	defer cancel()
	if canceled := ctx.Err(); canceled != nil {
		return nil, canceled
	}
	c := collector{reader: reader, snapshot: &Snapshot{Request: captured, CapturedAt: time.Now().UTC(), CollectionState: Complete}, seen: make(map[string]int)}
	object, err := c.read(ctx, gvr, request.Target.Namespace, request.Target.Name)
	if err != nil {
		c.snapshot.CollectionState = Partial
		return c.snapshot, nil
	}
	if !objectMatches(object, gvr, request.Target.Namespace, request.Target.Name, request.Target.UID) {
		return nil, errors.New("Selected resource identity changed or unavailable; reopen GitOps review")
	}
	c.appendNode(object, &request.Target, gvr, 0)
	for len(c.queue) > 0 && ctx.Err() == nil {
		entry := c.queue[0]
		c.queue = c.queue[1:]
		c.visit(ctx, &entry)
	}
	if ctx.Err() != nil {
		c.snapshot.CollectionState = Partial
		c.snapshot.Limits = append(c.snapshot.Limits, "Collection canceled or timed out; remaining references unobserved")
	}
	c.snapshot.Limits = append(c.snapshot.Limits,
		"Independent named API observations; not an atomic snapshot or workload-readiness proof",
		"Tracking metadata and inventory lack owner UIDs; readable name-only targets do not prove ownership",
		"Secret bodies, raw manifests, Helm values and free-form controller diagnostics excluded")
	return c.snapshot, nil
}

func (c *collector) appendNode(object *unstructured.Unstructured, identity *inspect.ResourceIdentity, gvr schema.GroupVersionResource, depth int) int {
	index := len(c.snapshot.Nodes)
	c.snapshot.Nodes = append(c.snapshot.Nodes, projectNode(object, identity, time.Now().UTC()))
	c.seen[resourceKey(gvr, identity.Namespace, identity.Name)] = index
	c.queue = append(c.queue, traversal{object: object, index: index, depth: depth})
	return index
}

func (c *collector) visit(ctx context.Context, entry *traversal) {
	refs := references(entry.object, &c.snapshot.Request)
	for index := range refs {
		if ctx.Err() != nil {
			return
		}
		ref := &refs[index]
		link := Link{From: entry.index, To: -1, Relation: ref.relation, Reference: referenceLabel(ref), Certainty: ref.certainty, State: Unknown}
		if ref.reason != "" {
			link.Reason = ref.reason
		} else if entry.depth >= MaxDepth || len(c.snapshot.Nodes) >= MaxNodes || c.reads >= MaxNodes {
			link.State, link.Reason = Partial, "Depth, node or named-read budget reached; remaining reference unobserved"
		} else {
			c.observeReference(ctx, entry, ref, &link)
		}
		c.snapshot.Links = append(c.snapshot.Links, link)
	}
}

func (c *collector) observeReference(ctx context.Context, entry *traversal, ref *reference, link *Link) {
	if !validName(ref.name) || ref.kind == "Secret" || ref.kind == "" ||
		(ref.group != "" && !validName(ref.group)) || (ref.version != "" && !validName(ref.version)) {
		link.Reason = "Malformed or excluded Secret reference; nothing read"
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, ReadTimeout)
	resolved, err := c.reader.Resolve(readCtx, ref.group, ref.version, ref.kind)
	if readCtx.Err() != nil {
		err = readCtx.Err()
	}
	cancel()
	if err != nil {
		link.State, link.Reason = readFailure(err)
		return
	}
	gvr := resolved.GVR
	namespace := ref.namespace
	if !resolved.Namespaced {
		if ref.relation != "controller reference" {
			link.Reason = "Declared namespaced reference resolved to cluster scope; not followed"
			return
		}
		namespace = ""
	} else if !validNamespace(namespace) {
		link.Reason = "Named reference namespace unavailable; nothing guessed"
		return
	}
	if !validName(gvr.Version) || !validName(gvr.Resource) || gvr.Resource == secretResource ||
		gvr.Group != ref.group || (ref.version != "" && gvr.Version != ref.version) {
		link.Reason = "Resolved API does not match declared reference or is an excluded Secret API"
		return
	}
	key := resourceKey(gvr, namespace, ref.name)
	if existing, found := c.seen[key]; found {
		c.acceptLink(ref, link, existing)
		if link.State == Complete && c.hasPath(existing, entry.index, make(map[int]bool)) {
			link.State, link.Reason = Partial, "Reference cycle detected; observed identities retained without an ownership conclusion"
		} else if link.State == Complete {
			c.compareSourceRevision(entry.index, existing, ref)
		}
		return
	}
	object, err := c.read(ctx, gvr, namespace, ref.name)
	if err != nil {
		link.State, link.Reason = namedReadFailure(err, gvr.GroupResource(), ref.name)
		return
	}
	if !objectMatches(object, gvr, namespace, ref.name, ref.uid) || object.GetKind() != ref.kind {
		link.Reason = "Referenced kind/namespace/name/UID changed or unavailable; no replacement substituted"
		return
	}
	identity := inspect.ResourceIdentity{Context: c.snapshot.Request.Target.Context, GVR: gvrString(gvr), Namespace: namespace, Name: ref.name, UID: string(object.GetUID())}
	index := c.appendNode(object, &identity, gvr, entry.depth+1)
	c.acceptLink(ref, link, index)
	c.compareSourceRevision(entry.index, index, ref)
}

func (c *collector) hasPath(from, to int, visited map[int]bool) bool {
	if from == to {
		return true
	}
	if visited[from] {
		return false
	}
	visited[from] = true
	for _, link := range c.snapshot.Links {
		if link.From == from && link.To >= 0 && link.State == Complete && c.hasPath(link.To, to, visited) {
			return true
		}
	}
	return false
}

func (c *collector) compareSourceRevision(from, to int, ref *reference) {
	controller, source := &c.snapshot.Nodes[from], &c.snapshot.Nodes[to]
	if ref.relation != "declared source reference" || controller.Kind != "Kustomization" {
		return
	}
	var applied, artifact string
	for _, version := range controller.Versions {
		if version.Source == "status.lastAppliedRevision" {
			applied = version.Value
		}
	}
	for _, version := range source.Versions {
		if version.Source == "status.artifact.revision" {
			artifact = version.Value
		}
	}
	if applied != "" && artifact != "" && applied != artifact {
		controller.Gaps = append(controller.Gaps, "Reported applied revision differs from independently observed source artifact; reconciliation progress unknown")
	}
}

func (c *collector) acceptLink(ref *reference, link *Link, index int) {
	node := &c.snapshot.Nodes[index]
	if ref.uid != "" && ref.uid != node.Identity.UID || node.Kind != ref.kind {
		link.Reason = "Captured reference UID/kind does not match observed identity"
		return
	}
	link.To, link.State, link.Reason = index, Complete, "Named reference target observed; independent source time retained"
	if ref.uid != "" {
		link.Certainty = "controller UID verified"
	}
}

func (c *collector) read(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	if gvr.Resource == secretResource {
		return nil, errors.New("Secret bodies excluded")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.reads++
	readCtx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	object, err := c.reader.Get(readCtx, gvr, namespace, name)
	if readCtx.Err() != nil {
		err = readCtx.Err()
	}
	state, reason := Complete, "Named GET; captured transport and declared reference scope"
	if err != nil {
		state, reason = namedReadFailure(err, gvr.GroupResource(), name)
	}
	c.snapshot.Coverage = append(c.snapshot.Coverage, Coverage{Source: gvrString(gvr), Scope: namespace + "/" + name,
		State: state, Reason: reason, ObservedAt: time.Now().UTC()})
	return object, err
}

func readFailure(err error) (state, reason string) {
	switch {
	case errors.Is(err, context.Canceled):
		return Unknown, "Read canceled; referenced state unknown"
	case errors.Is(err, context.DeadlineExceeded):
		return Unknown, "Read deadline reached; referenced state unknown"
	case errors.Is(err, ErrAPIAbsent):
		return Absent, "Requested controller/source API not installed or served"
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return Denied, "Requested reference read denied"
	default:
		return Unknown, "Read unavailable; raw server diagnostics withheld"
	}
}

func namedReadFailure(err error, expected schema.GroupResource, name string) (state, reason string) {
	var status apierrors.APIStatus
	if apierrors.IsNotFound(err) && !apierrors.IsUnexpectedServerError(err) && errors.As(err, &status) {
		observation := status.Status()
		if observation.Code == http.StatusNotFound && observation.Reason == metav1.StatusReasonNotFound && observation.Details != nil &&
			observation.Details.Name == name && observation.Details.Kind == expected.Resource && observation.Details.Group == expected.Group {
			return Missing, "Requested named reference missing at this observation"
		}
	}
	return readFailure(err)
}

func capturedGVR(identity *inspect.ResourceIdentity) (schema.GroupVersionResource, error) {
	parts := strings.Split(identity.GVR, "/")
	var gvr schema.GroupVersionResource
	switch len(parts) {
	case 2:
		gvr.Version, gvr.Resource = parts[0], parts[1]
	case 3:
		gvr.Group, gvr.Version, gvr.Resource = parts[0], parts[1], parts[2]
	default:
		return gvr, errors.New("Malformed captured GVR")
	}
	if !validName(gvr.Version) || !validName(gvr.Resource) || gvr.Resource == secretResource || (gvr.Group != "" && !validName(gvr.Group)) {
		return gvr, errors.New("Malformed or excluded captured API")
	}
	return gvr, nil
}

func objectMatches(object *unstructured.Unstructured, gvr schema.GroupVersionResource, namespace, name, uid string) bool {
	return object != nil && object.GetAPIVersion() == gvr.GroupVersion().String() && object.GetNamespace() == namespace && object.GetName() == name &&
		object.GetUID() != "" && (uid == "" || string(object.GetUID()) == uid) && object.GetKind() != "" && object.GetKind() != "Secret"
}
func resourceKey(gvr schema.GroupVersionResource, namespace, name string) string {
	return fmt.Sprintf("%s/%s:%s/%s", gvr.Group, gvr.Resource, namespace, name)
}
func gvrString(gvr schema.GroupVersionResource) string {
	if gvr.Group == "" {
		return gvr.Version + "/" + gvr.Resource
	}
	return gvr.Group + "/" + gvr.Version + "/" + gvr.Resource
}

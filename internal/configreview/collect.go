// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package configreview

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

var ErrReplaced = errors.New("captured source identity changed; reopen configuration review")

// SecretMetadata must negotiate only PartialObjectMetadata. The ordinary
// metadata client permits full-JSON fallback and is not suitable here.
type SecretMetadata func(context.Context, string, string) (*metav1.PartialObjectMetadata, error)

type Readers struct {
	Objects dynamic.Interface
	Secrets SecretMetadata
}

type Scope struct{ Identity Identity }

type resource struct {
	gvr  schema.GroupVersionResource
	kind string
	spec []string
}

var consumerResources = []resource{
	{schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "Pod", []string{"spec"}},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "Deployment", []string{"spec", "template", "spec"}},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, "StatefulSet", []string{"spec", "template", "spec"}},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, "DaemonSet", []string{"spec", "template", "spec"}},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, "ReplicaSet", []string{"spec", "template", "spec"}},
	{schema.GroupVersionResource{Version: "v1", Resource: "replicationcontrollers"}, "ReplicationController", []string{"spec", "template", "spec"}},
	{schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, "Job", []string{"spec", "template", "spec"}},
	{schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}, "CronJob", []string{"spec", "jobTemplate", "spec", "template", "spec"}},
}

func canonicalGVR(gvr schema.GroupVersionResource) string {
	return gvr.GroupVersion().String() + "/" + gvr.Resource
}

func sourceResource(gvr string) (resource, bool) {
	for _, r := range consumerResources {
		if gvr == canonicalGVR(r.gvr) {
			return r, true
		}
	}
	if gvr == "v1/configmaps" {
		return resource{gvr: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, kind: ConfigMapKind}, true
	}
	if gvr == "v1/secrets" {
		return resource{gvr: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, kind: SecretKind}, true
	}
	return resource{}, false
}

func (s *Scope) Validate() error {
	_, supported := sourceResource(s.Identity.GVR)
	if !supported || s.Identity.Context == "" || s.Identity.UID == "" ||
		len(validation.IsDNS1123Label(s.Identity.Namespace)) != 0 ||
		len(validation.IsDNS1123Subdomain(s.Identity.Name)) != 0 {
		return errors.New("select a captured-UID native Pod, workload, ConfigMap or Secret in one explicit namespace")
	}
	return nil
}

type collector struct {
	readers  Readers
	snapshot *Snapshot
	topics   map[string]struct{}
	seen     map[Reference]struct{}
}

// Collect uses named configuration GETs and eight bounded same-namespace consumer
// LISTs. It never lists Secrets, discovers identities, or reads runtime values.
func Collect(ctx context.Context, readers *Readers, scope *Scope) (*Snapshot, error) {
	if scope == nil || readers == nil {
		return nil, errors.New("configuration review readers/scope unavailable")
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c := &collector{readers: *readers, snapshot: &Snapshot{Identity: scope.Identity, CapturedAt: time.Now().UTC(), ConsumersComplete: true},
		topics: make(map[string]struct{}), seen: make(map[Reference]struct{})}
	r, _ := sourceResource(scope.Identity.GVR)
	c.snapshot.Identity.Kind = r.kind
	if err := c.source(ctx); err != nil {
		return nil, err
	}
	if len(c.topics) > 0 {
		c.consumers(ctx)
	}
	c.configurations(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return c.snapshot, nil
}

func (c *collector) source(ctx context.Context) error {
	id := c.snapshot.Identity
	r, _ := sourceResource(id.GVR)
	if r.kind == SecretKind {
		if c.readers.Secrets == nil {
			return errors.New("strict Secret metadata reader unavailable")
		}
		metadata, err := c.readers.Secrets(ctx, id.Namespace, id.Name)
		if err != nil {
			state, _ := readState(err)
			return errors.New("captured Secret metadata " + state + "; no full-object fallback")
		}
		if metadata == nil || string(metadata.UID) != id.UID || metadata.Namespace != id.Namespace || metadata.Name != id.Name {
			return ErrReplaced
		}
		c.snapshot.Objects = append(c.snapshot.Objects, metadataObject(&id, SecretKind, metadata))
		c.topics[objectKey(id.Namespace, SecretKind, id.Name)] = struct{}{}
	} else {
		if c.readers.Objects == nil {
			return errors.New("captured resource reader unavailable")
		}
		object, err := c.readers.Objects.Resource(r.gvr).Namespace(id.Namespace).Get(ctx, id.Name, metav1.GetOptions{})
		if err != nil {
			state, _ := readState(err)
			return errors.New("captured source " + state + "; retained evidence unchanged")
		}
		if !matches(object, &id, &r) {
			return ErrReplaced
		}
		if r.kind == ConfigMapKind {
			c.snapshot.Objects = append(c.snapshot.Objects, configMapObject(&id, object))
			c.topics[objectKey(id.Namespace, ConfigMapKind, id.Name)] = struct{}{}
		} else {
			refs, err := objectReferences(object, &id, &r)
			if err != nil {
				return errors.New("captured Pod template unavailable")
			}
			if len(refs) > MaxReferences {
				c.partial("Reference declarations", "Source declarations exceed bounded projection; further references unknown")
			}
			for i := range refs {
				ref := &refs[i]
				if c.add(ref) {
					c.topics[objectKey(ref.Consumer.Namespace, ref.Kind, ref.Name)] = struct{}{}
				}
			}
		}
	}
	c.coverage("Captured source named GET", inspect.ObservationComplete, "Source UID verified; declarations/metadata only")
	return nil
}

func matches(object *unstructured.Unstructured, id *Identity, r *resource) bool {
	return object != nil && object.GetNamespace() == id.Namespace && object.GetName() == id.Name &&
		string(object.GetUID()) == id.UID && object.GetKind() == r.kind && object.GetAPIVersion() == r.gvr.GroupVersion().String()
}

func objectReferences(object *unstructured.Unstructured, id *Identity, r *resource) ([]Reference, error) {
	raw, found, err := unstructured.NestedMap(object.Object, r.spec...)
	if err != nil || !found {
		return nil, errors.New("Pod spec was not reported")
	}
	var spec corev1.PodSpec
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &spec); err != nil {
		return nil, err
	}
	return References(id, &spec), nil
}

func (c *collector) add(ref *Reference) bool {
	if len(validation.IsDNS1123Subdomain(ref.Name)) != 0 || ref.Consumer.UID == "" {
		c.partial("Reference declarations", "Invalid or identity-free declaration excluded")
		return false
	}
	if _, exists := c.seen[*ref]; exists {
		return false
	}
	if len(c.snapshot.References) >= MaxReferences {
		c.partial("Reference declarations", "Retention capped at 64 references; consumer coverage incomplete")
		return false
	}
	c.seen[*ref] = struct{}{}
	c.snapshot.References = append(c.snapshot.References, *ref)
	return true
}

func (c *collector) consumers(ctx context.Context) {
	if c.readers.Objects == nil {
		c.partial("Consumers", "Native resource reader unavailable")
		return
	}
	for i := range consumerResources {
		r := &consumerResources[i]
		list, err := c.readers.Objects.Resource(r.gvr).Namespace(c.snapshot.Identity.Namespace).List(ctx, metav1.ListOptions{Limit: MaxConsumersPerKind + 1})
		if err != nil || list == nil {
			state := Unknown
			if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
				state = Denied
			}
			c.snapshot.ConsumersComplete = false
			c.coverage(canonicalGVR(r.gvr)+" consumer LIST", state, "Read unavailable; unseen consumers remain unknown")
			continue
		}
		state, detail := inspect.ObservationComplete, "Same-namespace spec observations; no runtime inspection"
		if list.GetContinue() != "" || len(list.Items) > MaxConsumersPerKind {
			state, detail = inspect.ObservationIncomplete, "Bounded visible page; further consumers may exist"
			c.snapshot.ConsumersComplete = false
		}
		c.coverage(canonicalGVR(r.gvr)+" consumer LIST", state, detail)
		c.consumerPage(list, r)
	}
}

func (c *collector) consumerPage(list *unstructured.UnstructuredList, r *resource) {
	for i := range list.Items[:min(len(list.Items), MaxConsumersPerKind)] {
		object := &list.Items[i]
		id := Identity{Kind: r.kind, ResourceIdentity: inspect.ResourceIdentity{Context: c.snapshot.Identity.Context, GVR: canonicalGVR(r.gvr),
			Namespace: object.GetNamespace(), Name: object.GetName(), UID: string(object.GetUID())}}
		if id.Namespace != c.snapshot.Identity.Namespace || id.UID == "" || !matches(object, &id, r) {
			c.partial("Consumers", "Unverified consumer identity excluded")
			continue
		}
		refs, err := objectReferences(object, &id, r)
		if err != nil {
			c.partial("Consumers", "Unsupported or unavailable Pod template excluded")
			continue
		}
		if len(refs) > MaxReferences {
			c.partial("Consumers", "Consumer declarations exceed bounded projection; additional relationships unknown")
		}
		for j := range refs {
			ref := &refs[j]
			if _, topic := c.topics[objectKey(ref.Consumer.Namespace, ref.Kind, ref.Name)]; topic {
				c.add(ref)
			}
		}
	}
}

func (c *collector) configurations(ctx context.Context) {
	keys := make([]string, 0, len(c.topics))
	for key := range c.topics {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		parts := strings.SplitN(key, "/", 3)
		namespace, kind, name := parts[0], parts[1], parts[2]
		if c.haveObject(namespace, kind, name) {
			continue
		}
		id := Identity{Kind: kind, ResourceIdentity: inspect.ResourceIdentity{Context: c.snapshot.Identity.Context, Namespace: namespace, Name: name}}
		object := Object{Identity: id, State: Unknown, ObservedAt: time.Now().UTC()}
		var err error
		if kind == SecretKind {
			object, err = c.secret(ctx, &id)
		} else {
			id.GVR = "v1/configmaps"
			object.Identity = id
			object.Reason = "ConfigMap identity or key projection unavailable"
			if c.readers.Objects != nil {
				var raw *unstructured.Unstructured
				raw, err = c.readers.Objects.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
				if err == nil && raw != nil && raw.GetAPIVersion() == "v1" && raw.GetKind() == ConfigMapKind &&
					raw.GetNamespace() == namespace && raw.GetName() == name && raw.GetUID() != "" {
					object = configMapObject(&id, raw)
				}
			}
		}
		if err != nil {
			object.State, object.Reason = readState(err)
		}
		c.snapshot.Objects = append(c.snapshot.Objects, object)
		if object.State != Present {
			c.coverage(kind+" "+namespace+"/"+name+" named GET", object.State, object.Reason)
		}
	}
}

func (c *collector) secret(ctx context.Context, id *Identity) (Object, error) {
	id.GVR = "v1/secrets"
	object := Object{Identity: *id, State: Unknown, ObservedAt: time.Now().UTC(), Reason: "Strict Secret metadata reader unavailable; no fallback requested"}
	if c.readers.Secrets == nil {
		return object, nil
	}
	object.Reason = "Secret metadata identity unavailable; no full-object fallback requested"
	meta, err := c.readers.Secrets(ctx, id.Namespace, id.Name)
	if err == nil && meta != nil && meta.Namespace == id.Namespace && meta.Name == id.Name && meta.UID != "" {
		object = metadataObject(id, SecretKind, meta)
	}
	return object, err
}

func metadataObject(id *Identity, kind string, meta metav1.Object) Object {
	identity := *id
	identity.Kind, identity.UID = kind, string(meta.GetUID())
	keys := make([]string, 0, min(len(meta.GetAnnotations()), MaxKeys))
	for key := range meta.GetAnnotations() {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return Object{Identity: identity, ResourceVersion: meta.GetResourceVersion(), State: Present, AnnotationKeys: keys[:min(len(keys), MaxKeys)],
		ObservedAt: time.Now().UTC(), Reason: "Metadata projection only; annotation values discarded; runtime adoption unknown"}
}

func configMapObject(id *Identity, raw *unstructured.Unstructured) Object {
	object := metadataObject(id, ConfigMapKind, raw)
	keys := make(map[string]struct{})
	for _, field := range []string{"data", "binaryData"} {
		rawValues, found, err := unstructured.NestedFieldNoCopy(raw.Object, field)
		values, valid := rawValues.(map[string]any)
		if found && !valid {
			object.Reason = "Key-name projection unavailable"
			return object
		}
		if err != nil {
			object.Reason = "Key-name projection unavailable"
			return object
		}
		if found {
			for key := range values {
				keys[key] = struct{}{}
			}
		}
	}
	for key := range keys {
		object.KeyNames = append(object.KeyNames, key)
	}
	slices.Sort(object.KeyNames)
	object.KeysKnown, object.KeysTruncated = true, len(object.KeyNames) > MaxKeys
	object.KeyNames = object.KeyNames[:min(len(object.KeyNames), MaxKeys)]
	return object
}

func (c *collector) haveObject(namespace, kind, name string) bool {
	for i := range c.snapshot.Objects {
		id := &c.snapshot.Objects[i].Identity
		if id.Namespace == namespace && id.Kind == kind && id.Name == name {
			return true
		}
	}
	return false
}

func (c *collector) coverage(source, state, detail string) {
	c.snapshot.Coverage = append(c.snapshot.Coverage, Coverage{Source: source, State: state, Detail: detail, ObservedAt: time.Now().UTC()})
}
func (c *collector) partial(source, detail string) {
	c.snapshot.ConsumersComplete = false
	for _, existing := range c.snapshot.Coverage {
		if existing.Source == source && existing.Detail == detail {
			return
		}
	}
	c.coverage(source, inspect.ObservationIncomplete, detail)
}

func readState(err error) (state, reason string) {
	if apierrors.IsNotFound(err) {
		return Missing, "Named object not found at this observation"
	}
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return Denied, "Named read denied; key existence and runtime use remain unknown"
	}
	return Unknown, "Named read unavailable or canceled; no absence inferred"
}

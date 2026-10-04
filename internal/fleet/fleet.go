// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
// Package fleet compares facts from two explicitly named, independently read actors.
package fleet

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

const unknown = "unknown"

const (
	ReadTimeout       = 3 * time.Second
	ContextTimeout    = 8 * time.Second
	CollectionTimeout = 12 * time.Second
)

type Scope struct {
	Contexts        [2]string
	GVR             schema.GroupVersionResource
	Namespace, Name string
	PrimaryUID      types.UID
}
type Actor struct {
	Reader       dynamic.Interface
	NamespaceGet func(context.Context, string) (*corev1.Namespace, error)
	Authority    string
}
type Factory func(context.Context, string) (Actor, error)
type Fact struct{ Category, Name, Value string }
type Observation struct {
	Context, Authority, Namespace, Name string
	UID                                 types.UID
	ResourceVersion                     string
	NamespaceUID                        types.UID
	CapturedAt                          time.Time
	State                               string
	Facts                               []Fact
}
type Snapshot struct {
	Scope        Scope
	Observations [2]Observation
	CapturedAt   time.Time
}

func (s *Scope) Validate() error {
	if s.Contexts[0] == "" || s.Contexts[1] == "" || s.Contexts[0] == s.Contexts[1] {
		return fmt.Errorf("name exactly two distinct contexts")
	}
	if len(validation.IsDNS1123Label(s.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(s.Name)) != 0 {
		return fmt.Errorf("explicit namespace and name required")
	}
	switch s.GVR {
	case schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"},
		schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"},
		schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}:
		return nil
	}
	return fmt.Errorf("unsupported kind: use native Deployment, StatefulSet, DaemonSet or Job")
}

// Authority parses before any UI truncation, retaining no credentials or query.
func Authority(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return unknown
	}
	return u.Scheme + "://" + u.Host
}

//nolint:gocritic // Capture immutable scope values before concurrent actors start.
func Collect(parent context.Context, scope Scope, factory Factory) (*Snapshot, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, CollectionTimeout)
	defer cancel()
	s := &Snapshot{Scope: scope, CapturedAt: time.Now()}
	type result struct {
		index       int
		observation Observation
	}
	results := make(chan result, 2)
	for i, name := range scope.Contexts {
		go func() { results <- result{i, collectContext(ctx, &scope, i, name, factory)} }()
	}
	for range 2 {
		select {
		case r := <-results:
			s.Observations[r.index] = r.observation
		case <-ctx.Done():
			for i, name := range scope.Contexts {
				if s.Observations[i].Context == "" {
					s.Observations[i] = Observation{Context: name, Namespace: scope.Namespace, Name: scope.Name, State: failure(ctx.Err())}
				}
			}
			return s, nil
		}
	}
	return s, nil
}
func collectContext(parent context.Context, scope *Scope, index int, name string, factory Factory) Observation {
	ctx, cancel := context.WithTimeout(parent, ContextTimeout)
	defer cancel()
	o := Observation{Context: name, Namespace: scope.Namespace, Name: scope.Name, State: unknown, Authority: unknown, CapturedAt: time.Now()}
	// Setup can load credentials. A buffered handoff bounds the waiting caller.
	type setup struct {
		actor Actor
		err   error
	}
	ready := make(chan setup, 1)
	go func() { a, err := factory(ctx, name); ready <- setup{a, err} }()
	var actor Actor
	select {
	case r := <-ready:
		if r.err != nil {
			o.State = failure(r.err)
			return o
		}
		actor = r.actor
	case <-ctx.Done():
		o.State = failure(ctx.Err())
		return o
	}
	o.Authority = Authority(actor.Authority)
	if actor.Reader == nil {
		o.State = "actor unavailable"
		return o
	}
	read, c := context.WithTimeout(ctx, ReadTimeout)
	obj, err := actor.Reader.Resource(scope.GVR).Namespace(scope.Namespace).Get(read, scope.Name, metav1.GetOptions{})
	c()
	o.CapturedAt = time.Now()
	if err != nil {
		if namedNotFound(err, scope.GVR, scope.Name) {
			o.State = "named object absent"
		} else {
			o.State = failure(err)
		}
		return o
	}
	expectedKind := map[string]string{"deployments": "Deployment", "statefulsets": "StatefulSet", "daemonsets": "DaemonSet", "jobs": "Job"}[scope.GVR.Resource]
	if obj == nil || obj.GetAPIVersion() != scope.GVR.GroupVersion().String() || obj.GetKind() != expectedKind ||
		obj.GetName() != scope.Name || obj.GetNamespace() != scope.Namespace || obj.GetUID() == "" || len(obj.GetUID()) > 128 {
		o.State = "unexpected object identity"
		return o
	}
	o.UID, o.ResourceVersion = obj.GetUID(), clean(obj.GetResourceVersion())
	if index == 0 && scope.PrimaryUID != "" && o.UID != scope.PrimaryUID {
		o.State = "primary replaced; reopen selection"
		return o
	}
	o.State = "captured facts; status unknown unless reported"
	o.Facts = facts(obj)
	if len(o.Facts) >= 64 {
		o.State = "captured facts; partial bounded field coverage"
	}
	if ctx.Err() == nil {
		read, c = context.WithTimeout(ctx, ReadTimeout)
		if actor.NamespaceGet != nil {
			ns, e := actor.NamespaceGet(read, scope.Namespace)
			// Typed decoding establishes the schema and can clear TypeMeta.
			if e == nil && ns != nil && ns.Name == scope.Namespace && ns.Namespace == "" && ns.UID != "" && len(ns.UID) <= 128 {
				o.NamespaceUID = ns.UID
			}
		} else {
			ns, e := actor.Reader.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(read, scope.Namespace, metav1.GetOptions{})
			if e == nil && ns != nil && ns.GetAPIVersion() == "v1" && ns.GetKind() == "Namespace" &&
				ns.GetName() == scope.Namespace && ns.GetNamespace() == "" && ns.GetUID() != "" && len(ns.GetUID()) <= 128 {
				o.NamespaceUID = ns.GetUID()
			}
		}
		c()
	}
	return o
}
func namedNotFound(err error, gvr schema.GroupVersionResource, name string) bool {
	if apierrors.IsUnexpectedServerError(err) {
		return false
	}
	var status interface{ Status() metav1.Status }
	if !errors.As(err, &status) {
		return false
	}
	s := status.Status()
	return s.Code == 404 && s.Reason == metav1.StatusReasonNotFound && s.Details != nil &&
		s.Details.Group == gvr.Group && s.Details.Kind == gvr.Resource && s.Details.Name == name
}
func failure(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	var status interface{ Status() metav1.Status }
	if errors.As(err, &status) {
		s := status.Status()
		if s.Code == 403 {
			return "denied"
		}
		if s.Code == 401 {
			return "authentication unavailable"
		}
	}
	return "read/setup unavailable (unexpected API or transport response)"
}
func facts(obj *unstructured.Unstructured) []Fact {
	result := []Fact{}
	if generation, found, _ := unstructured.NestedInt64(obj.Object, "metadata", "generation"); found {
		result = append(result, Fact{Category: "generation", Name: "metadata", Value: fmt.Sprint(generation)})
	}
	for _, section := range []string{"containers", "initContainers"} {
		containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", section)
		for _, item := range containers {
			if len(result) >= 64 {
				break
			}
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			image, _ := m["image"].(string)
			if image != "" {
				result = append(result, Fact{Category: "declared image (template)", Name: clean(name), Value: clean(image)})
			}
		}
	}
	for _, field := range []string{
		"replicas", "readyReplicas", "availableReplicas", "updatedReplicas", "currentReplicas",
		"desiredNumberScheduled", "currentNumberScheduled", "numberReady", "numberAvailable",
		"updatedNumberScheduled", "active", "succeeded", "failed", "observedGeneration",
	} {
		if len(result) >= 64 {
			break
		}
		value, found, _ := unstructured.NestedInt64(obj.Object, "status", field)
		if found {
			result = append(result, Fact{Category: "reported status", Name: field, Value: fmt.Sprint(value)})
		}
	}
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, item := range conditions {
		if len(result) >= 64 {
			break
		}
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := m["type"].(string)
		state, _ := m["status"].(string)
		if state == "" {
			state = unknown
		}
		reason, _ := m["reason"].(string)
		result = append(result, Fact{Category: "reported condition", Name: clean(kind), Value: clean(strings.TrimSpace(state + " " + reason))})
	}
	return result
}
func (s *Snapshot) MayAlias() bool {
	a, b := s.Observations[0], s.Observations[1]
	return a.Authority != unknown && a.Authority != "" && a.Authority == b.Authority && a.NamespaceUID != "" && a.NamespaceUID == b.NamespaceUID
}

// Bound retained fields and remove terminal control sequences before rendering.
func clean(value string) string {
	runes := []rune(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value))
	if len(runes) > 512 {
		runes = runes[:512]
	}
	return string(runes)
}

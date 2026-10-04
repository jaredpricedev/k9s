// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package upgrade collects bounded, read-only facts for upgrade review. It
// deliberately does not infer compatibility from versions or inventory.
package upgrade

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
)

const maxItems = 100
const ReadTimeout = 3 * time.Second
const CollectionTimeout = 10 * time.Second

// ValidNamespace requires a single explicit Kubernetes namespace.
func ValidNamespace(namespace string) bool {
	return namespace != "" && namespace != "all" && len(validation.IsDNS1123Label(namespace)) == 0
}

// SafeField bounds retained text and flattens terminal control characters.
func SafeField(value string) string {
	var b strings.Builder
	count := 0
	for _, r := range value {
		if count == 512 {
			break
		}
		count++
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	return b.String()
}
func validIdentity(m metav1.Object, namespace string) bool {
	uid := string(m.GetUID())
	return m.GetNamespace() == namespace && m.GetName() != "" && len(validation.IsDNS1123Subdomain(m.GetName())) == 0 &&
		uid != "" && utf8.RuneCountInString(uid) <= 512 && SafeField(uid) == uid
}

const (
	stateUnknown         = "unknown"
	stateObserved        = "observed"
	stateDenied          = "denied"
	stateUnavailable     = "unavailable"
	stateCanceledTimeout = "canceled/timeout"
	stateUnsupported404  = "unsupported/404"
	stateCredentials     = "credentials"
)

// Fact contains the deliberately small identity and version-like fields kept by the collector.
type Fact struct {
	Name, UID, ResourceVersion, Version string
}

// Section records one bounded capability/read result.
type Section struct {
	State, Detail string
	Items         []Fact
	Truncated     bool
}

// Snapshot retains one explicit upgrade-readiness observation.
type Snapshot struct {
	Context, Namespace, NamespaceUID                  string
	NamespaceState, ServerVersionState, ServerVersion string
	ObservedAt                                        time.Time
	Nodes, Deployments, DaemonSets, StatefulSets      Section
}

// AllFactReadsFailed excludes the independent namespace identity read.
func (s *Snapshot) AllFactReadsFailed() bool {
	return s.ServerVersionState != stateObserved && s.Nodes.State != stateObserved &&
		s.Deployments.State != stateObserved && s.DaemonSets.State != stateObserved &&
		s.StatefulSets.State != stateObserved
}

// Collect reads the namespace identity, server version-independent node facts,
// and three explicit controller kinds in the captured namespace. Every list is
// capped at 100. A failed section is retained as denied/unknown evidence.
func Collect(ctx context.Context, client kubernetes.Interface, namespace, contextName, serverVersion string, observedAt time.Time) Snapshot {
	ctx, cancel := context.WithTimeout(ctx, CollectionTimeout)
	defer cancel()
	s := Snapshot{Context: SafeField(contextName), Namespace: SafeField(namespace), ServerVersion: SafeField(serverVersion), ObservedAt: observedAt}
	if !ValidNamespace(namespace) || client == nil {
		s.NamespaceState, s.NamespaceUID = stateUnavailable, stateUnknown
		s.Nodes = Section{State: stateUnavailable, Detail: "explicit namespace required"}
		s.Deployments, s.DaemonSets, s.StatefulSets = Section{State: stateUnavailable}, Section{State: stateUnavailable}, Section{State: stateUnavailable}
		return s
	}
	if ctx.Err() != nil {
		s.NamespaceUID, s.NamespaceState = stateUnknown, StateForError(ctx.Err())
		s.Nodes = Section{State: StateForError(ctx.Err())}
		s.Deployments = Section{State: StateForError(ctx.Err())}
		s.DaemonSets = Section{State: StateForError(ctx.Err())}
		s.StatefulSets = Section{State: StateForError(ctx.Err())}
		return s
	}
	readCtx, readCancel := context.WithTimeout(ctx, ReadTimeout)
	ns, err := client.CoreV1().Namespaces().Get(readCtx, namespace, metav1.GetOptions{})
	readCancel()
	if err != nil {
		s.NamespaceUID = stateUnknown
		s.NamespaceState = StateForError(err)
	} else if ns == nil || ns.Name != namespace || !validIdentity(ns, "") {
		s.NamespaceUID, s.NamespaceState = stateUnknown, stateUnavailable
	} else {
		s.NamespaceUID = string(ns.UID)
		s.NamespaceState = stateObserved
	}
	s.Nodes = collectNodes(ctx, client)
	s.Deployments = collectDeployments(ctx, client, s.Namespace)
	s.DaemonSets = collectDaemonSets(ctx, client, s.Namespace)
	s.StatefulSets = collectStatefulSets(ctx, client, s.Namespace)
	return s
}

func collectNodes(ctx context.Context, client kubernetes.Interface) Section {
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return Section{State: StateForError(ctx.Err()), Detail: safeError(ctx.Err())}
	}
	list, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: maxItems})
	if err != nil {
		return Section{State: StateForError(err), Detail: safeError(err)}
	}
	s := Section{State: stateObserved}
	limit := len(list.Items)
	if limit > maxItems {
		limit = maxItems
		s.Truncated = true
	}
	for index := range list.Items[:limit] {
		node := &list.Items[index]
		if !validIdentity(node, "") {
			s.Detail = "partial: invalid object identity"
			continue
		}
		version := node.Status.NodeInfo.KubeletVersion
		if version == "" {
			version = stateUnknown
		}
		s.Items = append(s.Items, Fact{Name: SafeField(node.Name), UID: string(node.UID), ResourceVersion: SafeField(node.ResourceVersion), Version: SafeField(version)})
	}
	s.Truncated = s.Truncated || list.Continue != ""
	if limit > 0 && len(s.Items) == 0 {
		s.State = stateUnavailable
	}
	return s
}

func collectDeployments(ctx context.Context, client kubernetes.Interface, namespace string) Section {
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return Section{State: StateForError(ctx.Err()), Detail: safeError(ctx.Err())}
	}
	list, err := client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{Limit: maxItems})
	if err != nil {
		return Section{State: StateForError(err), Detail: safeError(err)}
	}
	return controllers(list.Items, func(x appsv1.Deployment) (metav1.Object, []corev1.Container) {
		return &x, x.Spec.Template.Spec.Containers
	}, list.Continue, namespace)
}
func collectDaemonSets(ctx context.Context, client kubernetes.Interface, namespace string) Section {
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return Section{State: StateForError(ctx.Err()), Detail: safeError(ctx.Err())}
	}
	list, err := client.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{Limit: maxItems})
	if err != nil {
		return Section{State: StateForError(err), Detail: safeError(err)}
	}
	return controllers(list.Items, func(x appsv1.DaemonSet) (metav1.Object, []corev1.Container) {
		return &x, x.Spec.Template.Spec.Containers
	}, list.Continue, namespace)
}
func collectStatefulSets(ctx context.Context, client kubernetes.Interface, namespace string) Section {
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return Section{State: StateForError(ctx.Err()), Detail: safeError(ctx.Err())}
	}
	list, err := client.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{Limit: maxItems})
	if err != nil {
		return Section{State: StateForError(err), Detail: safeError(err)}
	}
	return controllers(list.Items, func(x appsv1.StatefulSet) (metav1.Object, []corev1.Container) {
		return &x, x.Spec.Template.Spec.Containers
	}, list.Continue, namespace)
}

func controllers[T any](items []T, fields func(T) (metav1.Object, []corev1.Container), continuation, namespace string) Section {
	s := Section{State: stateObserved, Truncated: continuation != "" || len(items) > maxItems}
	limit := min(len(items), maxItems)
	for _, item := range items[:limit] {
		object, containers := fields(item)
		if !validIdentity(object, namespace) {
			s.Detail = "partial: invalid object identity"
			continue
		}
		if len(containers) == 0 {
			containers = []corev1.Container{{Image: stateUnknown}}
		}
		for index := range containers {
			if len(s.Items) == maxItems {
				s.Truncated = true
				return s
			}
			container := &containers[index]
			image := container.Image
			if image == "" {
				image = stateUnknown
			}
			name := object.GetName()
			if container.Name != "" {
				name += "/" + container.Name
			}
			s.Items = append(s.Items, Fact{
				Name: SafeField(name), UID: string(object.GetUID()),
				ResourceVersion: SafeField(object.GetResourceVersion()), Version: SafeField(image),
			})
		}
	}
	if limit > 0 && len(s.Items) == 0 {
		s.State = stateUnavailable
	}
	return s
}

// StateForError maps Kubernetes/API failures to safe display categories.
func StateForError(err error) string {
	if err == nil {
		return stateObserved
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return stateCanceledTimeout
	}
	if apierrors.IsForbidden(err) {
		return stateDenied
	}
	if apierrors.IsUnauthorized(err) {
		return stateCredentials
	}
	if apierrors.IsNotFound(err) || apierrors.IsMethodNotSupported(err) {
		return stateUnsupported404
	}
	if apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) {
		return stateCanceledTimeout
	}
	if strings.Contains(strings.ToLower(err.Error()), "forbidden") {
		return stateDenied
	}
	return stateUnavailable
}
func safeError(err error) string {
	// Kubernetes API errors may include server and object details. Keep only a
	// bounded state label; do not retain response bodies or arbitrary messages.
	if err == nil {
		return ""
	}
	return fmt.Sprintf("request failed (%s)", StateForError(err))
}

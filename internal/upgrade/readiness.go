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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const maxItems = 100

const (
	stateObserved        = "observed"
	stateDenied          = "denied"
	stateUnavailable     = "unavailable"
	stateCanceledTimeout = "canceled/timeout"
	stateUnsupported404  = "unsupported/404"
	stateCredentials     = "credentials"
)

// Fact contains the deliberately small identity and version-like fields kept by the collector.
type Fact struct {
	Name, UID, Version string
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

// Collect reads the namespace identity, server version-independent node facts,
// and three explicit controller kinds in the captured namespace. Every list is
// capped at 100. A failed section is retained as denied/unknown evidence.
func Collect(ctx context.Context, client kubernetes.Interface, namespace, contextName, serverVersion string, observedAt time.Time) Snapshot {
	s := Snapshot{Context: contextName, Namespace: namespace, ServerVersion: serverVersion, ObservedAt: observedAt}
	if ctx.Err() != nil {
		s.NamespaceUID, s.NamespaceState = stateUnavailable, StateForError(ctx.Err())
		s.Nodes = Section{State: StateForError(ctx.Err())}
		s.Deployments = Section{State: StateForError(ctx.Err())}
		s.DaemonSets = Section{State: StateForError(ctx.Err())}
		s.StatefulSets = Section{State: StateForError(ctx.Err())}
		return s
	}
	ns, err := client.CoreV1().Namespaces().Get(ctx, s.Namespace, metav1.GetOptions{})
	if err != nil {
		s.NamespaceUID = stateUnavailable
		s.NamespaceState = StateForError(err)
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
		version := node.Status.NodeInfo.KubeletVersion
		if version == "" {
			version = "unknown"
		}
		s.Items = append(s.Items, Fact{Name: node.Name, UID: string(node.UID), Version: version})
	}
	s.Truncated = s.Truncated || list.Continue != ""
	return s
}

func collectDeployments(ctx context.Context, client kubernetes.Interface, namespace string) Section {
	if ctx.Err() != nil {
		return Section{State: StateForError(ctx.Err()), Detail: safeError(ctx.Err())}
	}
	list, err := client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{Limit: maxItems})
	if err != nil {
		return Section{State: StateForError(err), Detail: safeError(err)}
	}
	return controllers(list.Items, func(x appsv1.Deployment) (string, string, []corev1.Container) {
		return x.Name, string(x.UID), x.Spec.Template.Spec.Containers
	}, list.Continue)
}
func collectDaemonSets(ctx context.Context, client kubernetes.Interface, namespace string) Section {
	if ctx.Err() != nil {
		return Section{State: StateForError(ctx.Err()), Detail: safeError(ctx.Err())}
	}
	list, err := client.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{Limit: maxItems})
	if err != nil {
		return Section{State: StateForError(err), Detail: safeError(err)}
	}
	return controllers(list.Items, func(x appsv1.DaemonSet) (string, string, []corev1.Container) {
		return x.Name, string(x.UID), x.Spec.Template.Spec.Containers
	}, list.Continue)
}
func collectStatefulSets(ctx context.Context, client kubernetes.Interface, namespace string) Section {
	if ctx.Err() != nil {
		return Section{State: StateForError(ctx.Err()), Detail: safeError(ctx.Err())}
	}
	list, err := client.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{Limit: maxItems})
	if err != nil {
		return Section{State: StateForError(err), Detail: safeError(err)}
	}
	return controllers(list.Items, func(x appsv1.StatefulSet) (string, string, []corev1.Container) {
		return x.Name, string(x.UID), x.Spec.Template.Spec.Containers
	}, list.Continue)
}

func controllers[T any](items []T, fields func(T) (string, string, []corev1.Container), continuation string) Section {
	s := Section{State: stateObserved, Truncated: continuation != ""}
	for _, item := range items {
		name, uid, containers := fields(item)
		for index := range containers {
			container := &containers[index]
			if len(s.Items) == maxItems {
				s.Truncated = true
				return s
			}
			image := container.Image
			if image == "" {
				image = "unknown"
			}
			s.Items = append(s.Items, Fact{Name: name + "/" + container.Name, UID: uid, Version: image})
		}
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

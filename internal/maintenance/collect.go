// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package maintenance

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
)

// Collect reads a named Node, one bounded node-scoped Pod page and PDB pages
// only in observed Pod namespaces. It never requests Secrets or mutates data.
func Collect(parent context.Context, client kubernetes.Interface, identity *inspect.ResourceIdentity, now time.Time) (*Snapshot, error) {
	if client == nil || identity == nil || identity.Context == "" || identity.Name == "" || identity.Namespace != "" || identity.UID == "" || identity.GVR != "v1/nodes" {
		return nil, fmt.Errorf("maintenance requires a captured native Node UID")
	}
	captured := *identity
	identity = &captured
	ctx, cancel := context.WithTimeout(parent, CollectTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	readCtx, readCancel := context.WithTimeout(ctx, ReadTimeout)
	node, err := client.CoreV1().Nodes().Get(readCtx, identity.Name, metav1.GetOptions{})
	if readCtx.Err() != nil {
		err = readCtx.Err()
	}
	readCancel()
	if err != nil {
		return nil, err
	}
	if node.Name != identity.Name || node.Namespace != "" || string(node.UID) != identity.UID {
		return nil, fmt.Errorf("Node identity changed; reopen maintenance review")
	}
	snapshot := &Snapshot{Identity: *identity, CapturedAt: now, Node: projectNode(node)}
	snapshot.Coverage = append(snapshot.Coverage, Coverage{Source: "Node", Scope: identity.Name, State: Complete, Count: 1, Limit: 1, ObservedAt: time.Now()})
	pods := collectPods(ctx, client, snapshot)
	budgets := collectBudgets(ctx, client, snapshot, pods)
	snapshot.projectPods(ctx, pods, budgets)
	return snapshot, nil
}

func collectPods(ctx context.Context, client kubernetes.Interface, snapshot *Snapshot) []corev1.Pod {
	readCtx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	source := Coverage{Source: PodsSource, Scope: "spec.nodeName=" + snapshot.Identity.Name, State: Complete, Limit: MaxPods, ObservedAt: time.Now()}
	if err := readCtx.Err(); err != nil {
		source.State, source.Detail = readFailure(err)
		snapshot.Coverage = append(snapshot.Coverage, source)
		return nil
	}
	list, err := client.CoreV1().Pods("").List(readCtx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", snapshot.Identity.Name).String(), Limit: MaxPods,
	})
	if readCtx.Err() != nil {
		err = readCtx.Err()
	}
	if err != nil {
		source.State, source.Detail = readFailure(err)
		snapshot.Coverage = append(snapshot.Coverage, source)
		return nil
	}
	if list == nil {
		source.State, source.Detail = Unavailable, "No Pod observation returned"
		snapshot.Coverage = append(snapshot.Coverage, source)
		return nil
	}
	if list.Continue != "" || len(list.Items) > MaxPods {
		source.State, source.Detail = Partial, "Single page retained; remaining Pods were not collected"
	}
	pods := list.Items[:min(len(list.Items), MaxPods)]
	// Enforce scope even for custom clients whose List implementation ignores the
	// field selector. Never promote a different node's Pod into this review.
	filtered := make([]corev1.Pod, 0, len(pods))
	paths, uids := make(map[string]struct{}), make(map[string]struct{})
	for index := range pods {
		pod := &pods[index]
		path := pod.Namespace + "/" + pod.Name
		_, duplicatePath := paths[path]
		_, duplicateUID := uids[string(pod.UID)]
		if pod.Spec.NodeName != snapshot.Identity.Name || pod.Name == "" || pod.Namespace == "" || pod.UID == "" || duplicatePath || duplicateUID {
			source.State, source.Detail = Partial, "Wrong scope, missing or duplicate Pod identity; page cannot define an executable review"
			continue
		}
		paths[path], uids[string(pod.UID)] = struct{}{}, struct{}{}
		filtered = append(filtered, *pod)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Namespace+"/"+filtered[i].Name < filtered[j].Namespace+"/"+filtered[j].Name
	})
	source.Count = len(filtered)
	snapshot.Coverage = append(snapshot.Coverage, source)
	return filtered
}

func collectBudgets(ctx context.Context, client kubernetes.Interface, snapshot *Snapshot, pods []corev1.Pod) []policyv1.PodDisruptionBudget {
	namespaces := make(map[string]struct{})
	for index := range pods {
		namespaces[pods[index].Namespace] = struct{}{}
	}
	names := make([]string, 0, len(namespaces))
	for name := range namespaces {
		names = append(names, name)
	}
	sort.Strings(names)
	var budgets []policyv1.PodDisruptionBudget
	for index, namespace := range names {
		remaining := MaxBudgets - len(budgets)
		if index >= MaxNamespaces || remaining <= 0 {
			snapshot.Coverage = append(snapshot.Coverage, Coverage{Source: BudgetsSource, Scope: namespace, State: Partial,
				Detail: "Namespace or total PDB collection limit reached", ObservedAt: time.Now()})
			continue
		}
		observed, source := collectBudgetPage(ctx, client, namespace, remaining)
		budgets = append(budgets, observed...)
		snapshot.Coverage = append(snapshot.Coverage, source)
	}
	if len(names) == 0 {
		state, detail := Complete, "No observed Pod namespace to inspect"
		if !snapshot.PodsComplete() {
			state, detail = Unavailable, "Pod coverage unavailable; PDB scope is unknown"
		}
		snapshot.Coverage = append(snapshot.Coverage, Coverage{Source: BudgetsSource, State: state, Detail: detail, ObservedAt: time.Now(), Limit: MaxBudgets})
	}
	return budgets
}

func collectBudgetPage(ctx context.Context, client kubernetes.Interface, namespace string, limit int) ([]policyv1.PodDisruptionBudget, Coverage) {
	readCtx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	source := Coverage{Source: BudgetsSource, Scope: namespace, State: Complete, Limit: limit, ObservedAt: time.Now()}
	if err := readCtx.Err(); err != nil {
		source.State, source.Detail = readFailure(err)
		return nil, source
	}
	list, err := client.PolicyV1().PodDisruptionBudgets(namespace).List(readCtx, metav1.ListOptions{Limit: int64(limit)})
	if readCtx.Err() != nil {
		err = readCtx.Err()
	}
	if err != nil {
		source.State, source.Detail = readFailure(err)
		return nil, source
	}
	if list == nil {
		source.State, source.Detail = Unavailable, "No PDB observation returned"
		return nil, source
	}
	if list.Continue != "" || len(list.Items) > limit {
		source.State, source.Detail = Partial, "Single page retained; remaining PDBs were not collected"
	}
	var items []policyv1.PodDisruptionBudget
	for index := range min(len(list.Items), limit) {
		budget := &list.Items[index]
		if budget.Namespace != namespace {
			source.State, source.Detail = Partial, "Wrong-namespace PDB discarded; coverage is incomplete"
			continue
		}
		items = append(items, *budget)
	}
	source.Count = len(items)
	return items, source
}

func readFailure(err error) (state, detail string) {
	state = Unavailable
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		state = Denied
	}
	return state, logstream.SafeText(err.Error())
}

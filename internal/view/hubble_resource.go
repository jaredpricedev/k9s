// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/hubble"
	"github.com/derailed/tcell/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	hubblePodScopeLimit = 1000
	hubblePodPageSize   = 250
	hubbleDaemonSets    = "daemonsets"
	hubbleDeployments   = "deployments"
	hubblePods          = "pods"
	hubbleStatefulSets  = "statefulsets"
	hubbleReplicaSets   = "replicasets"
	hubbleJobs          = "jobs"
)

func hubbleResource(gvr *client.GVR) bool {
	switch gvr.R() {
	case hubblePods, hubbleDeployments, hubbleDaemonSets, hubbleStatefulSets, hubbleReplicaSets, hubbleJobs:
		return true
	}
	return false
}
func (b *Browser) hubbleCmd(*tcell.EventKey) *tcell.EventKey {
	target := b.SelectedResource()
	if err := target.Err(); err != nil {
		b.App().Flash().Err(err)
		return nil
	}
	if !hubbleResource(target.GVR) {
		b.App().Flash().Err(fmt.Errorf("Hubble requires a pod or supported workload"))
		return nil
	}
	connection, err := pinInspectionConnection(b.App().Conn())
	if err != nil {
		b.App().Flash().Err(err)
		return nil
	}
	w := newHubbleView(hubble.Scope{Title: target.Path()}, false)
	w.resolve = func(ctx context.Context) (hubble.Scope, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return resolveHubbleScope(ctx, connection, target)
	}
	if err := b.App().inject(w, false); err != nil {
		b.App().Flash().Err(err)
	}
	return nil
}

//nolint:gocritic // The resolver owns a copy of the selected identity across context changes.
func resolveHubbleScope(ctx context.Context, connection client.Connection, target SelectedResourceTarget) (hubble.Scope, error) {
	scope := hubble.Scope{Title: target.Path()}
	if err := target.Err(); err != nil {
		return scope, err
	}
	if target.Namespace == "" {
		return scope, fmt.Errorf("Hubble pod resolution requires a namespace")
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return scope, ctxErr
	}
	scope.Title = target.GVR.R() + " " + target.Path()
	d, err := connection.DynDial()
	if err != nil {
		return scope, err
	}
	object, err := d.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return scope, err
	}
	if identityErr := verifySelectedIdentity(target, object); identityErr != nil {
		return scope, identityErr
	}
	if target.GVR.R() == hubblePods {
		scope.Pods = []string{target.Path()}
		return scope, ctx.Err()
	}
	raw, ok, err := unstructured.NestedMap(object.Object, "spec", "selector")
	if err != nil || !ok {
		return scope, fmt.Errorf("workload has no reported pod selector")
	}
	var selector metav1.LabelSelector
	if decodeErr := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &selector); decodeErr != nil {
		return scope, decodeErr
	}
	sel, err := metav1.LabelSelectorAsSelector(&selector)
	if err != nil {
		return scope, err
	}
	if sel.Empty() {
		return scope, fmt.Errorf("refusing empty workload selector")
	}
	k, err := connection.Dial()
	if err != nil {
		return scope, err
	}
	var continuation string
	seen := map[string]bool{}
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return scope, ctxErr
		}
		// The final request asks only for the single extra match needed to
		// establish the cap, rather than decoding another full page.
		limit := int64(min(hubblePodPageSize, hubblePodScopeLimit+1-len(scope.Pods)))
		pods, err := k.CoreV1().Pods(target.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String(), Limit: limit, Continue: continuation})
		if err != nil {
			return scope, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return scope, ctxErr
		}
		for i := range pods.Items {
			if len(scope.Pods) == hubblePodScopeLimit {
				scope.Pods = nil
				return scope, fmt.Errorf("scope exceeds 1000 pods; select a smaller workload (resolution stopped at 1001 matches)")
			}
			scope.Pods = append(scope.Pods, client.FQN(target.Namespace, pods.Items[i].Name))
		}
		if pods.Continue == "" {
			break
		}
		if seen[pods.Continue] {
			return scope, fmt.Errorf("pod API repeated its continuation token; retry resolution")
		}
		seen[pods.Continue] = true
		continuation = pods.Continue
	}
	if len(scope.Pods) == 0 {
		return scope, fmt.Errorf("no current pods match workload selector; r retries")
	}
	scope.Title += fmt.Sprintf(" (%d current pods; r refreshes membership)", len(scope.Pods))
	return scope, nil
}

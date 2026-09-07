// SPDX-License-Identifier: Apache-2.0
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

func hubbleResource(gvr *client.GVR) bool {
	switch gvr.R() {
	case "pods", "deployments", "daemonsets", "statefulsets", "replicasets", "jobs":
		return true
	}
	return false
}
func (b *Browser) hubbleCmd(*tcell.EventKey) *tcell.EventKey {
	path := b.GetTable().GetSelectedItem()
	if path == "" {
		return nil
	}
	w := newHubbleView(hubble.Scope{Title: path}, false)
	gvr := b.GVR()
	connection := b.App().Conn()
	w.resolve = func(ctx context.Context) (hubble.Scope, error) {
		scope := hubble.Scope{Title: gvr.R() + " " + path}
		ns, name := client.Namespaced(path)
		if gvr.R() == "pods" {
			scope.Pods = []string{path}
			return scope, nil
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		d, err := connection.DynDial()
		if err != nil {
			return scope, err
		}
		object, err := d.Resource(gvr.GVR()).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return scope, err
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
		pods, err := k.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
		if err != nil {
			return scope, err
		}
		for i := range pods.Items {
			pod := &pods.Items[i]
			scope.Pods = append(scope.Pods, ns+"/"+pod.Name)
		}
		if len(scope.Pods) == 0 {
			return scope, fmt.Errorf("no current pods match workload selector; r retries")
		}
		if len(scope.Pods) > 1000 {
			return scope, fmt.Errorf("scope exceeds 1000 pods; select a smaller workload")
		}
		scope.Title += fmt.Sprintf(" (%d current pods; r refreshes membership)", len(scope.Pods))
		return scope, nil
	}
	if err := b.App().inject(w, false); err != nil {
		b.App().Flash().Err(err)
	}
	return nil
}

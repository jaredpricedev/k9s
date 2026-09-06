// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dao

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/flux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/dynamic"
)

const (
	fluxReconcile = "reconcile"
	fluxSuspend   = "suspend"
	fluxResume    = "resume"
)

// FluxDashboard combines Flux resources from the existing informer cache.
type FluxDashboard struct{ NonResource }

func (f *FluxDashboard) List(ctx context.Context, ns string) ([]runtime.Object, error) {
	return listFlux(ctx, f.getFactory(), MetaAccess, ns)
}

func listFlux(ctx context.Context, f Factory, metas *Meta, ns string) ([]runtime.Object, error) {
	sel := labels.Everything()
	if s, ok := ctx.Value(internal.KeyLabels).(labels.Selector); ok {
		sel = s
	}
	var result []runtime.Object
	for _, gvr := range fluxResourceVersions(metas) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Nonblocking cache reads let initial informer synchronization happen in
		// the background. Subsequent table refreshes reuse the same watches.
		objects, err := f.List(gvr, ns, false, sel)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			meta, _ := metas.MetaFor(gvr)
			o := &unstructured.Unstructured{}
			o.SetAPIVersion(gvr.GV().String())
			o.SetKind(meta.Kind)
			o.SetNamespace(client.CleanseNamespace(ns))
			o.SetName("<unavailable>")
			if apierrors.IsForbidden(err) || strings.Contains(err.Error(), "access denied") {
				o.SetName("<restricted>")
			}
			o.SetAnnotations(map[string]string{"k9scli.io/flux-list-error": err.Error()})
			result = append(result, o)
			continue
		}
		result = append(result, objects...)
	}
	return result, nil
}

// Prefer the newest stable served version and never list an object twice
// just because discovery advertises multiple versions of the same CRD.
func fluxResourceVersions(metas *Meta) client.GVRs {
	selected := make(map[string]*client.GVR)
	for _, gvr := range metas.AllGVRs() {
		if !flux.Supported(gvr) {
			continue
		}
		key := gvr.G() + "/" + gvr.R()
		old, ok := selected[key]
		if !ok || version.CompareKubeAwareVersionStrings(gvr.V(), old.V()) > 0 {
			selected[key] = gvr
		}
	}
	result := make(client.GVRs, 0, len(selected))
	for _, gvr := range selected {
		result = append(result, gvr)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result
}

// FluxNativeActions limits spec.suspend/requestedAt writes to controllers
// implementing these APIs. Other Flux kinds remain available for inspection.
func FluxNativeActions(gvr *client.GVR) bool {
	if !flux.Supported(gvr) {
		return false
	}
	switch gvr.G() {
	case "kustomize.toolkit.fluxcd.io", "helm.toolkit.fluxcd.io":
		return true
	case "source.toolkit.fluxcd.io":
		switch gvr.R() {
		case "gitrepositories", "ocirepositories", "helmrepositories", "helmcharts", "buckets":
			return true
		}
	}
	return false
}

// PrepareFluxAction captures the selected cluster client before a confirmation
// can be followed by a context switch. The returned operation has no UI state.
func PrepareFluxAction(conn client.Connection, gvr *client.GVR, fqn string, uid types.UID, action string) (func(context.Context) error, error) {
	if !FluxNativeActions(gvr) {
		return nil, fmt.Errorf("native Flux actions are unavailable for %s", gvr)
	}
	ns, name := client.Namespaced(fqn)
	if ns == "" || name == "" || uid == "" {
		return nil, fmt.Errorf("select a persisted namespaced Flux resource")
	}
	dial, err := conn.DynDial()
	if err != nil {
		return nil, err
	}
	resource := dial.Resource(gvr.GVR()).Namespace(ns)
	return func(ctx context.Context) error {
		// Kubernetes authorizes both requests using this captured client's
		// credentials. A failed GET or PATCH is returned without a retry.
		return patchFlux(ctx, resource, name, uid, action, time.Now())
	}, nil
}

func patchFlux(ctx context.Context, resource dynamic.ResourceInterface, name string, expectedUID types.UID, action string, now time.Time) error {
	if action != fluxReconcile && action != fluxSuspend && action != fluxResume {
		return fmt.Errorf("unsupported Flux action %q", action)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	o, err := resource.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if expectedUID == "" || o.GetUID() != expectedUID {
		return fmt.Errorf("resource %s was replaced; refresh and confirm again", name)
	}
	if o.GetResourceVersion() == "" {
		return fmt.Errorf("resource %s has no resource version", name)
	}
	meta := map[string]any{"resourceVersion": o.GetResourceVersion(), "uid": string(expectedUID)}
	patch := map[string]any{"metadata": meta}
	if action == fluxReconcile {
		if flux.Suspended(o) {
			return fmt.Errorf("resource %s is suspended; resume it before reconciling", name)
		}
		meta["annotations"] = map[string]string{"reconcile.fluxcd.io/requestedAt": now.Format(time.RFC3339Nano)}
	} else {
		patch["spec"] = map[string]bool{fluxSuspend: action == fluxSuspend}
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = resource.Patch(ctx, name, types.MergePatchType, data, metav1.PatchOptions{FieldManager: "k9s"})
	return err
}

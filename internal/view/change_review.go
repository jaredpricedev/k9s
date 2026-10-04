// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/workspace"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// captureReviewScope uses the visible workspace, not the saved store's active
// name. Returning to an older workspace must preserve that workspace's bounds.
func captureReviewScope(a *App) (review.Scope, error) {
	if a == nil || a.Config == nil || a.Config.ActiveContextName() == "" {
		return review.Scope{}, fmt.Errorf("select a configured context before reviewing a manifest")
	}
	scope := review.Scope{Context: a.Config.ActiveContextName(), CapturedUIDs: make(map[string]types.UID)}
	top := a.Content.Top()
	if current, ok := top.(*desiredReviewView); ok {
		if current.contextName != scope.Context || current.revision != a.Config.DestinationRevision() {
			return review.Scope{}, fmt.Errorf("destination changed; reopen review from the intended workspace")
		}
		return copyDesiredReviewScope(current.scope), nil
	}
	if daily, ok := top.(*dailyWorkspace); ok {
		if daily.scope.Name == "" {
			return review.Scope{}, fmt.Errorf("select a saved workspace before reviewing a manifest")
		}
		if daily.scope.Context != scope.Context {
			return review.Scope{}, fmt.Errorf("context changed; reopen the saved workspace in its original context")
		}
		scope.Namespaces = slices.Clone(daily.scope.Namespaces)
		scope.Kinds = slices.Clone(daily.scope.Kinds)
		if len(scope.Kinds) == 0 {
			scope.Kinds = workspace.DefaultKinds()
		}
		scope.LabelSelector = daily.scope.LabelSelector
		if len(scope.Namespaces) == 1 {
			scope.DefaultNamespace = scope.Namespaces[0]
		}
		for _, resource := range daily.snapshot.Resources {
			ref := resource.Ref
			if ref.UID != "" && ref.GVR != "" {
				gvr := client.NewGVR(ref.GVR).GVR()
				scope.CapturedUIDs[review.IdentityKey(gvr, ref.Namespace, ref.Name)] = types.UID(ref.UID)
			}
		}
		return scope, nil
	}
	namespace := a.Config.ActiveNamespace()
	if client.IsNamespaced(namespace) {
		scope.DefaultNamespace = namespace
		scope.Namespaces = []string{namespace}
	}
	if browser, ok := top.(*Browser); ok && browser.GetModel() != nil {
		if selector := browser.GetModel().GetLabelSelector(); selector != nil {
			scope.LabelSelector = selector.String()
		}
	}
	if owner, ok := top.(actionOwner); ok {
		target := actionTarget(owner, scope.Context)
		if target.Context != "" && target.Context != scope.Context {
			return review.Scope{}, fmt.Errorf("context changed; reopen review from the intended resource")
		}
		if _, browser := top.(ResourceViewer); !browser && target.Err() == nil && client.IsNamespaced(target.Namespace) {
			scope.DefaultNamespace = target.Namespace
			scope.Namespaces = []string{target.Namespace}
		}
		if target.Err() == nil && target.UID != "" && target.GVR != nil {
			scope.CapturedUIDs[review.IdentityKey(target.GVR.GVR(), target.Namespace, target.Name)] = target.UID
		}
	}
	return scope, nil
}

// reviewResolver captures a discovery REST client with the original transport.
// A single exact-version discovery GET respects the collector's context and
// deadline, instead of using mutable, unbounded cached discovery methods.
func reviewResolver(connection client.Connection) review.Resolver {
	if connection == nil {
		return unavailableReviewResolver
	}
	typed, err := connection.Dial()
	if err != nil || typed == nil || typed.Discovery() == nil || typed.Discovery().RESTClient() == nil {
		return unavailableReviewResolver
	}
	reader := typed.Discovery().RESTClient()
	return func(ctx context.Context, apiVersion, kind string) (review.Mapping, error) {
		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			return review.Mapping{}, fmt.Errorf("source API version is invalid")
		}
		if gv.Version == "" {
			return review.Mapping{}, fmt.Errorf("source API version is invalid")
		}
		path := "/api/" + gv.Version
		if gv.Group != "" {
			path = "/apis/" + gv.Group + "/" + gv.Version
		}
		resources := &metav1.APIResourceList{}
		if err := reader.Get().AbsPath(path).Do(ctx).Into(resources); err != nil {
			return review.Mapping{}, fmt.Errorf("exact-version API discovery unavailable")
		}
		if resources.GroupVersion != gv.String() {
			return review.Mapping{}, fmt.Errorf("API discovery returned a different version")
		}
		for i := range resources.APIResources {
			item := &resources.APIResources[i]
			if item.Kind == kind && item.Name != "" && !strings.Contains(item.Name, "/") {
				return review.Mapping{GVR: gv.WithResource(item.Name), Namespaced: item.Namespaced}, nil
			}
		}
		return review.Mapping{}, fmt.Errorf("source kind is not served by this API version")
	}
}

func unavailableReviewResolver(context.Context, string, string) (review.Mapping, error) {
	return review.Mapping{}, fmt.Errorf("exact-version API discovery unavailable")
}

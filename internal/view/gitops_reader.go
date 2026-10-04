// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"net/http"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/gitops"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

const maxGitOpsDiscoveryReads = 32

// gitopsReader captures existing native client handles. API discovery requests
// only the group/version needed by an explicit reference, with no global scan.
// Cache lifetime is one collection; refresh gets a new reader and observation.
type gitopsReader struct {
	dynamic   dynamic.Interface
	discovery rest.Interface
	groups    map[string]string
	resources map[string]*metav1.APIResourceList
	checks    int
}

func (r *gitopsReader) Get(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	if r.dynamic == nil || gvr.Resource == desiredReviewSecrets {
		return nil, errors.New("Resource reader unavailable or Secret body excluded")
	}
	return r.dynamic.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (r *gitopsReader) Resolve(ctx context.Context, group, version, kind string) (gitops.ResolvedResource, error) {
	var unavailable gitops.ResolvedResource
	if r.discovery == nil || kind == "Secret" || group != "" && !gitopsDNS(group) || version != "" && !gitopsDNS(version) {
		return unavailable, errors.New("Requested API descriptor unavailable, invalid or excluded")
	}
	if version == "" {
		observed, err := r.preferredVersion(ctx, group)
		if err != nil {
			return unavailable, err
		}
		version = observed
	}
	resources, err := r.apiResources(ctx, group, version)
	if err != nil {
		return unavailable, err
	}
	var found []gitops.ResolvedResource
	for index := range resources.APIResources {
		resource := &resources.APIResources[index]
		if resource.Kind != kind || !gitopsDNS(resource.Name) || resource.Name == desiredReviewSecrets {
			continue
		}
		readable := false
		for _, verb := range resource.Verbs {
			readable = readable || verb == client.GetVerb
		}
		if readable {
			found = append(found, gitops.ResolvedResource{GVR: schema.GroupVersionResource{Group: group, Version: version, Resource: resource.Name},
				Namespaced: resource.Namespaced})
		}
	}
	if len(found) == 0 {
		return unavailable, gitops.ErrAPIAbsent
	}
	if len(found) != 1 {
		return unavailable, errors.New("Requested kind has ambiguous advertised resources")
	}
	return found[0], nil
}

func (r *gitopsReader) preferredVersion(ctx context.Context, group string) (string, error) {
	if cached, exists := r.groups[group]; exists {
		return cached, nil
	}
	if group == "" {
		return corev1.SchemeGroupVersion.Version, nil
	}
	if err := r.checkBudget(ctx); err != nil {
		return "", err
	}
	var api metav1.APIGroup
	if err := r.discovery.Get().AbsPath("/apis", group).Do(ctx).Into(&api); err != nil {
		return "", gitopsDiscoveryError(err)
	}
	version := api.PreferredVersion.Version
	if api.Name != group || !gitopsDNS(version) || api.PreferredVersion.GroupVersion != group+"/"+version {
		return "", errors.New("Requested API preferred version could not be verified")
	}
	advertised := false
	for _, item := range api.Versions {
		advertised = advertised || item.Version == version && item.GroupVersion == group+"/"+version
	}
	if !advertised {
		return "", errors.New("Preferred API version is not advertised")
	}
	if r.groups == nil {
		r.groups = make(map[string]string)
	}
	r.groups[group] = version
	return version, nil
}

func (r *gitopsReader) apiResources(ctx context.Context, group, version string) (*metav1.APIResourceList, error) {
	groupVersion := schema.GroupVersion{Group: group, Version: version}.String()
	if cached, exists := r.resources[groupVersion]; exists {
		return cached, nil
	}
	if err := r.checkBudget(ctx); err != nil {
		return nil, err
	}
	path := []string{"/apis", group, version}
	if group == "" {
		path = []string{"/api", version}
	}
	var resources metav1.APIResourceList
	if err := r.discovery.Get().AbsPath(path...).Do(ctx).Into(&resources); err != nil {
		return nil, gitopsDiscoveryError(err)
	}
	if resources.GroupVersion != groupVersion {
		return nil, errors.New("Advertised API group/version mismatch")
	}
	if r.resources == nil {
		r.resources = make(map[string]*metav1.APIResourceList)
	}
	r.resources[groupVersion] = &resources
	return &resources, nil
}

func (r *gitopsReader) checkBudget(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.checks >= maxGitOpsDiscoveryReads {
		return errors.New("Requested API discovery budget reached")
	}
	r.checks++
	return nil
}
func gitopsDNS(value string) bool {
	return value != "" && len(validation.IsDNS1123Subdomain(value)) == 0
}
func gitopsDiscoveryError(err error) error {
	var status apierrors.APIStatus
	if apierrors.IsNotFound(err) && !apierrors.IsUnexpectedServerError(err) && errors.As(err, &status) {
		observation := status.Status()
		if observation.Code == http.StatusNotFound && observation.Reason == metav1.StatusReasonNotFound && observation.Details == nil {
			return gitops.ErrAPIAbsent
		}
	}
	return err
}

func gitopsNamespaceDNS(value string) bool {
	return value != "" && len(validation.IsDNS1123Label(value)) == 0
}

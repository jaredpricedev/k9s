// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	typedappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
)

// Native kubectl filters and discovery still have context.TODO calls. Bind
// those reads to the operation without replacing its ownership/filter policy.
func (c maintenanceClient) AppsV1() typedappsv1.AppsV1Interface {
	return maintenanceApps{AppsV1Interface: c.Interface.AppsV1(), ctx: c.ctx}
}

type maintenanceApps struct {
	typedappsv1.AppsV1Interface
	ctx context.Context
}

func (c maintenanceApps) DaemonSets(namespace string) typedappsv1.DaemonSetInterface {
	return maintenanceDaemonSets{DaemonSetInterface: c.AppsV1Interface.DaemonSets(namespace), ctx: c.ctx}
}

type maintenanceDaemonSets struct {
	typedappsv1.DaemonSetInterface
	ctx context.Context
}

func (c maintenanceDaemonSets) Get(_ context.Context, name string, opts metav1.GetOptions) (*appsv1.DaemonSet, error) {
	if c.ctx == nil {
		return nil, fmt.Errorf("native ownership read requires a captured operation context")
	}
	return c.DaemonSetInterface.Get(c.ctx, name, opts)
}

func (c maintenanceClient) Discovery() discovery.DiscoveryInterface {
	if c.discoveryClient != nil {
		return c.discoveryClient
	}
	return &maintenanceDiscovery{DiscoveryInterface: c.Interface.Discovery(), ctx: c.ctx}
}

// Alternative client adapters can provide a context-aware discovery read. A
// transport without either a REST client or this contract fails closed.
type contextualGroupVersionDiscovery interface {
	ServerResourcesForGroupVersionContext(context.Context, string) (*metav1.APIResourceList, error)
}

type maintenanceDiscovery struct {
	discovery.DiscoveryInterface
	ctx       context.Context
	mu        sync.Mutex
	resources map[string]*metav1.APIResourceList
}

func (c *maintenanceDiscovery) ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx == nil {
		return nil, fmt.Errorf("native eviction discovery requires a captured operation context")
	}
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	if cached := c.resources[groupVersion]; cached != nil {
		return cached.DeepCopy(), nil
	}
	resources, err := c.readResources(groupVersion)
	if err != nil {
		return nil, err
	}
	if resources == nil {
		return nil, fmt.Errorf("eviction discovery returned no observation")
	}
	if c.resources == nil {
		c.resources = make(map[string]*metav1.APIResourceList)
	}
	// The helper repeats discovery after cordon. Keep the preflight decision so
	// a changing discovery response cannot downgrade reviewed eviction to delete.
	c.resources[groupVersion] = resources.DeepCopy()
	return resources, nil
}

func (c *maintenanceDiscovery) readResources(groupVersion string) (*metav1.APIResourceList, error) {
	if contextual, ok := c.DiscoveryInterface.(contextualGroupVersionDiscovery); ok {
		return contextual.ServerResourcesForGroupVersionContext(c.ctx, groupVersion)
	}
	transport := c.DiscoveryInterface.RESTClient()
	if transport == nil {
		return nil, fmt.Errorf("bounded eviction discovery is unavailable; drain was not submitted")
	}
	if groupVersion == "" {
		return nil, fmt.Errorf("eviction discovery group version is unavailable")
	}
	path := "/apis/" + groupVersion
	if groupVersion == corev1.SchemeGroupVersion.String() {
		prefix := "/api"
		if native, ok := c.DiscoveryInterface.(*discovery.DiscoveryClient); ok {
			prefix = native.LegacyPrefix
		}
		if prefix != "" {
			path = prefix + "/" + groupVersion
		}
	}
	resources := &metav1.APIResourceList{GroupVersion: groupVersion}
	err := transport.Get().AbsPath(path).Do(c.ctx).Into(resources)
	// Preserve client-go's native core/v1 404 fallback. Other errors, including
	// denied discovery and cancellation, never imply unsupported eviction.
	if groupVersion == corev1.SchemeGroupVersion.String() && apierrors.IsNotFound(err) {
		return resources, nil
	}
	if err != nil {
		return nil, err
	}
	return resources, nil
}

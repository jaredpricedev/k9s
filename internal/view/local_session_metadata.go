// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/watch"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// The namespace is the originating list scope, which may contain Pods from all
// namespaces. Reading identity/ports never authorizes or waits for an informer.
func cachedLocalSessionPod(factory *watch.Factory, listNamespace, path string) (*v1.Pod, error) {
	object, err := factory.CachedGet(client.PodGVR, listNamespace, path)
	if err != nil {
		return nil, fmt.Errorf("Pod metadata is not retained; reopen the native Pod list")
	}
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		return nil, err
	}
	var pod v1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(data, &pod); err != nil {
		return nil, err
	}
	return &pod, nil
}

// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/maintenance"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The native helper collects all pages before it filters. A single capped page
// prevents unbounded preflight growth and retains the exact reviewed UID set.
type maintenancePodReview struct {
	ctx        context.Context
	nodeName   string
	reviewed   map[string]string
	verifyNode func(context.Context) error
}

//nolint:gocritic // The native PodInterface requires ListOptions by value.
func (p maintenancePods) List(ctx context.Context, opts metav1.ListOptions) (*corev1.PodList, error) {
	if p.review == nil {
		return p.PodInterface.List(ctx, opts)
	}
	ctx = p.review.ctx
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.Continue != "" {
		return nil, fmt.Errorf("drain Pod collection exceeded the bounded reviewed page; refresh maintenance preview")
	}
	opts.Limit = maintenance.MaxPods
	list, err := p.PodInterface.List(ctx, opts)
	if err != nil {
		return nil, err
	}
	if list == nil {
		return nil, fmt.Errorf("Pod collection returned no observation; drain was not submitted")
	}
	if len(list.Items) > maintenance.MaxPods || list.Continue != "" {
		return nil, fmt.Errorf("drain requires a complete Pod page (at most %d); review a smaller maintenance scope", maintenance.MaxPods)
	}
	paths, uids := make(map[string]bool), make(map[string]bool)
	for index := range list.Items {
		pod := &list.Items[index]
		path, uid := client.FQN(pod.Namespace, pod.Name), string(pod.UID)
		if pod.Name == "" || pod.Namespace == "" || uid == "" || pod.Spec.NodeName != p.review.nodeName || paths[path] || uids[uid] ||
			(p.review.reviewed != nil && p.review.reviewed[path] != uid) {
			return nil, fmt.Errorf("Pod identity or scope changed for %s; refresh maintenance preview before draining", path)
		}
		paths[path], uids[uid] = true, true
	}
	return list, nil
}

func (p *maintenancePodReview) checkNode(ctx context.Context) error {
	if p == nil || p.verifyNode == nil {
		return nil
	}
	return p.verifyNode(ctx)
}

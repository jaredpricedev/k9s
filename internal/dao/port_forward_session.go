// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package dao

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/port"
	authorizationv1 "k8s.io/api/authorization/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
)

var (
	// ErrForwardIdentityChanged means the captured Pod has been replaced.
	ErrForwardIdentityChanged = errors.New("selected Pod identity changed")
	// ErrForwardEndpointDenied means no permitted forwarding transport exists.
	ErrForwardEndpointDenied = errors.New("selected Pod endpoint access denied")
	// ErrForwardPodNotRunning means the live Pod is not ready for forwarding.
	ErrForwardPodNotRunning = errors.New("selected Pod is not running")
)

// Ready closes only after client-go has bound the requested local listeners.
func (p *PortForwarder) Ready() <-chan struct{} { return p.readyChan }

// StartCaptured verifies a captured Pod identity and endpoint access on a
// bounded worker. Endpoint authorization is independent of cluster-write mode.
// Setup remains active through negotiation; a successful stream then belongs
// only to lifetime. The caller must keep setup active until readiness or failure.
func (p *PortForwarder) StartCaptured(setup, lifetime context.Context, cfg *rest.Config, path string,
	tunnel port.PortTunnel, uid types.UID,
) (*portforward.PortForwarder, error) {
	if cfg == nil || uid == "" {
		return nil, fmt.Errorf("port-forward destination or Pod identity is unavailable; reopen the resource")
	}
	ctx, cancel := context.WithCancel(setup)
	stopLifetime := context.AfterFunc(lifetime, cancel)
	defer stopLifetime()
	defer cancel()
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	namespace, name := client.Namespaced(strings.Split(path, "|")[0])
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	allowed, err := forwardEndpointAllowed(ctx, clientset, namespace, name, "", client.GetVerb)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("%w: get Pod required", ErrForwardEndpointDenied)
	}
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if pod.UID != uid {
		return nil, ErrForwardIdentityChanged
	}
	if pod.Status.Phase != v1.PodRunning {
		return nil, ErrForwardPodNotRunning
	}
	getAllowed, getErr := forwardEndpointAllowed(ctx, clientset, namespace, name, "portforward", client.GetVerb)
	createAllowed, createErr := forwardEndpointAllowed(ctx, clientset, namespace, name, "portforward", client.CreateVerb)
	if !getAllowed && !createAllowed {
		if getErr != nil {
			return nil, getErr
		}
		if createErr != nil {
			return nil, createErr
		}
		return nil, fmt.Errorf("%w: get or create pods/portforward required", ErrForwardEndpointDenied)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	p.path, p.tunnel, p.age = path, tunnel, metav1.Now().Time
	transportConfig := rest.CopyConfig(cfg)
	if transportConfig.Timeout <= 0 || transportConfig.Timeout > defaultTimeout {
		transportConfig.Timeout = defaultTimeout
	}
	transportConfig.GroupVersion = &schema.GroupVersion{Group: "", Version: "v1"}
	transportConfig.APIPath = portForwardAPIPath
	serializer := codec()
	transportConfig.NegotiatedSerializer = serializer.WithoutConversion()
	resourceClient, err := rest.RESTClientFor(transportConfig)
	if err != nil {
		return nil, err
	}
	request := resourceClient.Post().Resource("pods").Namespace(namespace).Name(name).SubResource("portforward")
	return p.forwardCapturedTransport(setup, lifetime, transportConfig, request.URL(), tunnel, getAllowed, createAllowed)
}

func forwardEndpointAllowed(ctx context.Context, clientset kubernetes.Interface, namespace, name, subresource, verb string) (bool, error) {
	review, err := clientset.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authorizationv1.ResourceAttributes{
			Namespace: namespace, Version: "v1", Resource: "pods", Name: name, Subresource: subresource, Verb: verb,
		}},
	}, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return review.Status.Allowed, nil
}

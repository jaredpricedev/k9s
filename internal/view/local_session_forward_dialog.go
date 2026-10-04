// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"fmt"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/port"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type forwardDialogCapture struct {
	view        ResourceViewer
	destination forwardDestination
	selection   string
	generation  uint64
}

var errForwardNoCandidate = errors.New("no explicit running Pod candidate")

func prepareForwardDialog(view ResourceViewer, path string) error {
	app := view.App()
	if app.factory == nil || app.Conn() == nil {
		return fmt.Errorf("forwarding connection is unavailable")
	}
	origin := selectedResourceForPath(view, app.Config.ActiveContextName(), path)
	if err := checkOperationTarget(&origin); err != nil {
		return err
	}
	actor, err := app.Conn().RestConfig()
	if err != nil {
		return err
	}
	capture := &forwardDialogCapture{view: view, selection: view.GetTable().GetSelectedItem(),
		generation: view.GetTable().operationGeneration.Load(), destination: forwardDestination{
			app: app, factory: app.factory, owner: app.Content.Top(), revision: app.Config.DestinationRevision(),
			origin: origin, config: rest.CopyConfig(actor)}}
	if origin.GVR.GVR() == client.PodGVR.GVR() {
		pod, err := cachedLocalSessionPod(app.factory, view.GetTable().GetNamespace(), path)
		if err != nil {
			return err
		}
		return capture.show(pod)
	}
	// Controller candidate lookup is an explicit bounded read, with a captured
	// client. It cannot wait on authorization or informer sync on the UI thread.
	go capture.prepareControllerPod()
	app.Flash().Info("Checking selected controller's Pod endpoint; navigation cancels the pending dialog")
	return nil
}

func (c *forwardDialogCapture) current() bool {
	d, app := &c.destination, c.destination.app
	if !app.IsRunning() || app.factory != d.factory || app.Config.DestinationRevision() != d.revision ||
		app.Content.Top() != d.owner || c.view.GetTable().GetSelectedItem() != c.selection ||
		c.view.GetTable().operationGeneration.Load() != c.generation {
		return false
	}
	if d.origin.UID == "" {
		pod, err := cachedLocalSessionPod(d.factory, app.Config.CachedNamespace(), d.target.Path())
		return err == nil && pod.UID == d.target.UID
	}
	current := selectedResourceForPath(c.view, d.origin.Context, c.selection)
	return current.Err() == nil && current.UID == d.origin.UID
}

func (c *forwardDialogCapture) prepareControllerPod() {
	d := &c.destination
	ctx, cancel := context.WithTimeout(d.app.sessionContext(), localSessionSetupTimeout)
	defer cancel()
	pod, err := collectForwardControllerPod(ctx, d.config, &d.origin)
	if !d.app.IsRunning() {
		return
	}
	go d.app.QueueUpdateDraw(func() {
		if !c.current() {
			return
		}
		if err != nil {
			d.app.Flash().Warn(localForwardSetupNote(err))
			return
		}
		if err := c.show(pod); err != nil {
			d.app.Flash().Err(err)
		}
	})
}

func collectForwardControllerPod(ctx context.Context, cfg *rest.Config, target *SelectedResourceTarget) (*v1.Pod, error) {
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	operation := &operationSession{typed: typed, dynamic: dyn}
	if permissionErr := operation.authorize(ctx, target, "", client.GetVerb); permissionErr != nil {
		return nil, permissionErr
	}
	object, err := operation.resource(target).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if identityErr := verifySelectedIdentity(*target, object); identityErr != nil {
		return nil, errors.Join(identityErr, dao.ErrForwardIdentityChanged)
	}
	selector, err := forwardControllerSelector(object, target.GVR)
	if err != nil {
		return nil, err
	}
	podsTarget := SelectedResourceTarget{Context: target.Context, GVR: client.PodGVR, Namespace: target.Namespace}
	if permissionErr := operation.authorize(ctx, &podsTarget, "", client.ListVerb); permissionErr != nil {
		return nil, permissionErr
	}
	pods, err := typed.CoreV1().Pods(target.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String(), Limit: 200})
	if err != nil {
		return nil, err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.UID != "" && pod.Status.Phase == v1.PodRunning && pod.DeletionTimestamp == nil {
			return pod.DeepCopy(), nil
		}
	}
	return nil, fmt.Errorf("%w: bounded 200-Pod lookup completed; open Pods to select an endpoint", errForwardNoCandidate)
}

func forwardControllerSelector(object *unstructured.Unstructured, gvr *client.GVR) (labels.Selector, error) {
	selector, found, err := unstructured.NestedMap(object.Object, "spec", "selector")
	if err != nil || !found || len(selector) == 0 {
		return nil, fmt.Errorf("%w: selected controller has no Pod selector", errForwardNoCandidate)
	}
	if gvr.GVR() == client.SvcGVR.GVR() {
		values, _, selectorErr := unstructured.NestedStringMap(object.Object, "spec", "selector")
		if selectorErr != nil {
			return nil, selectorErr
		}
		return labels.SelectorFromSet(values), nil
	}
	var value metav1.LabelSelector
	if conversionErr := runtime.DefaultUnstructuredConverter.FromUnstructured(selector, &value); conversionErr != nil {
		return nil, conversionErr
	}
	parsed, err := metav1.LabelSelectorAsSelector(&value)
	if err != nil {
		return nil, err
	}
	if parsed.Empty() {
		return nil, fmt.Errorf("%w: selected controller has an empty Pod selector", errForwardNoCandidate)
	}
	return parsed, nil
}

func (c *forwardDialogCapture) show(pod *v1.Pod) error {
	if !c.current() {
		return fmt.Errorf("selection changed; reopen the forwarding action")
	}
	d := &c.destination
	d.target = resourceTargetForPath(client.PodGVR, d.origin.Context, client.FQN(pod.Namespace, pod.Name))
	d.target.UID, d.path = pod.UID, d.target.Path()
	if err := checkOperationTarget(&d.target); err != nil {
		return err
	}
	ports := make(port.ContainerPortSpecs, 0, len(pod.Spec.Containers))
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		for _, exposed := range container.Ports {
			if exposed.Protocol == v1.ProtocolTCP || exposed.Protocol == "" {
				ports = append(ports, port.NewPortSpec(container.Name, exposed.Name, exposed.ContainerPort))
			}
		}
	}
	return c.showPorts(ports, pod.Annotations)
}

func (c *forwardDialogCapture) showPorts(ports port.ContainerPortSpecs, annotations port.Annotations) error {
	d := &c.destination
	start := func(view ResourceViewer, _ string, tunnels port.PortTunnels) error {
		if !c.current() {
			return fmt.Errorf("destination or selected identity changed; reopen the forwarding action")
		}
		if err := d.start(tunnels); err != nil {
			return err
		}
		DismissPortForwards(view, view.App().Content.Pages)
		return nil
	}
	if spec, ok := annotations[port.K9sAutoPortForwardsKey]; ok {
		parsed, err := port.ParsePFs(spec)
		if err != nil {
			return err
		}
		tunnels, err := parsed.ToTunnels(d.app.Config.K9s.PortForwardAddress, ports, port.IsPortFree)
		if err != nil {
			return err
		}
		return start(c.view, d.path, tunnels)
	}
	ShowPortForwards(c.view, d.path, ports, annotations, start)
	return nil
}

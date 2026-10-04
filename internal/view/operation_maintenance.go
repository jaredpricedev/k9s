// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	typedpolicyv1 "k8s.io/client-go/kubernetes/typed/policy/v1"
	typedpolicyv1beta1 "k8s.io/client-go/kubernetes/typed/policy/v1beta1"
	"k8s.io/kubectl/pkg/drain"
)

//nolint:gocritic // The selected identity is immutable across worker boundaries.
func (s *operationSession) cordon(ctx context.Context, target SelectedResourceTarget, cordon bool) error {
	if err := checkOperationTarget(&target); err != nil {
		return err
	}
	if target.GVR.GVR() != client.NodeGVR.GVR() {
		return errors.New("cordon requires a native Node selection")
	}
	if err := s.authorize(ctx, &target, "", client.GetVerb, client.PatchVerb); err != nil {
		return err
	}
	return retryOperationConflict(ctx, func() error {
		obj, err := s.readTarget(ctx, &target)
		if err != nil {
			return err
		}
		patch, err := json.Marshal(map[string]any{
			operationMetadataField: map[string]any{operationUIDField: string(target.UID), operationResourceVersionField: obj.GetResourceVersion()},
			operationSpecField:     map[string]any{"unschedulable": cordon},
		})
		if err != nil {
			return err
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		operationBeginWrite(ctx)
		_, err = s.resource(&target).Patch(ctx, target.Name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: "k9plus-maintenance"})
		if err == nil {
			operationAcceptWrite(ctx, fmt.Sprintf("node unschedulable=%t", cordon))
		}
		return err
	})
}

//nolint:gocritic // Keep the selected node immutable across concurrent pod evictions.
func (s *operationSession) drain(ctx context.Context, target SelectedResourceTarget, opts dao.DrainOptions) error {
	if err := checkOperationTarget(&target); err != nil {
		return err
	}
	if target.GVR.GVR() != client.NodeGVR.GVR() {
		return errors.New("drain requires a native Node selection")
	}
	if opts.GracePeriodSeconds < -1 || opts.Timeout < 0 {
		return errors.New("invalid drain options; reopen the drain form")
	}
	if err := s.authorize(ctx, &target, "", client.GetVerb, client.PatchVerb); err != nil {
		return err
	}
	if _, err := s.readTarget(ctx, &target); err != nil {
		return err
	}
	podsTarget := SelectedResourceTarget{Context: target.Context, GVR: client.PodGVR}
	if err := s.authorize(ctx, &podsTarget, "", client.ListVerb); err != nil {
		return err
	}
	helper := drain.Helper{
		Ctx: ctx, Client: s.typed, GracePeriodSeconds: opts.GracePeriodSeconds, Timeout: opts.Timeout,
		DeleteEmptyDirData: opts.DeleteEmptyDirData, IgnoreAllDaemonSets: opts.IgnoreAllDaemonSets,
		DisableEviction: opts.DisableEviction, Force: opts.Force,
		Out: operationOutput(ctx), ErrOut: operationOutput(ctx), EvictErrorRetryDelay: 200 * time.Millisecond,
	}
	// Validate kubectl's ownership, DaemonSet and local-data filters before the
	// node is cordoned. Those native decisions must not be replaced by a guessed
	// "safe" delete. Explicit disable-eviction retains its delete semantics.
	list, errs := helper.GetPodsForDeletion(target.Name)
	if err := errors.Join(errs...); err != nil {
		return err
	}
	pods := list.Pods()
	if len(pods) > 500 {
		return errors.New("drain is limited to 500 eligible Pods per node; review a smaller maintenance scope")
	}
	evictionVersion, err := drain.CheckEvictionSupport(s.typed)
	if err != nil {
		return err
	}
	usingEviction := !opts.DisableEviction && !evictionVersion.Empty()
	expected := make(map[string]types.UID, len(pods))
	for _, pod := range pods {
		selectedPod := SelectedResourceTarget{Context: target.Context, GVR: client.PodGVR, Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID}
		if pod.UID == "" {
			return fmt.Errorf("Pod identity unavailable for %s; drain was not submitted", selectedPod.Path())
		}
		expected[selectedPod.Path()] = pod.UID
		if !usingEviction {
			if err := s.authorize(ctx, &selectedPod, "", client.GetVerb, client.DeleteVerb); err != nil {
				return err
			}
		} else {
			if err := s.authorize(ctx, &selectedPod, "", client.GetVerb); err != nil {
				return err
			}
			if err := s.authorize(ctx, &selectedPod, "eviction", client.CreateVerb); err != nil {
				return err
			}
		}
	}
	if err := s.cordon(ctx, target, true); err != nil {
		return err
	}
	helper.Client = maintenanceClient{Interface: s.typed, expected: expected}
	helper.OnPodDeletionOrEvictionFinished = func(pod *corev1.Pod, _ bool, err error) {
		if err == nil {
			fmt.Fprintf(operationOutput(ctx), "Observed Pod removal: %s/%s (UID %s)\n", pod.Namespace, pod.Name, pod.UID)
		}
	}
	if err := helper.DeleteOrEvictPods(pods); err != nil {
		return err
	}
	fmt.Fprintln(operationOutput(ctx), "Native drain completed; accepted changes were not rolled back.")
	return nil
}

// Keep kubectl's native filters, PDB handling and wait logic while pinning every
// selected Pod's UID on the actual eviction/delete request. Native drain can
// refresh a Pod after a rejected eviction; it must never follow a same-name
// replacement. UID preconditions also close the final GET-to-write race.
type maintenanceClient struct {
	kubernetes.Interface
	expected map[string]types.UID
}

func (c maintenanceClient) CoreV1() typedcorev1.CoreV1Interface {
	return maintenanceCore{CoreV1Interface: c.Interface.CoreV1(), expected: c.expected}
}
func (c maintenanceClient) PolicyV1() typedpolicyv1.PolicyV1Interface {
	return maintenancePolicy{PolicyV1Interface: c.Interface.PolicyV1(), expected: c.expected}
}
func (c maintenanceClient) PolicyV1beta1() typedpolicyv1beta1.PolicyV1beta1Interface {
	return maintenancePolicyBeta{PolicyV1beta1Interface: c.Interface.PolicyV1beta1(), expected: c.expected}
}

type maintenanceCore struct {
	typedcorev1.CoreV1Interface
	expected map[string]types.UID
}

func (c maintenanceCore) Pods(namespace string) typedcorev1.PodInterface {
	return maintenancePods{PodInterface: c.CoreV1Interface.Pods(namespace), namespace: namespace, expected: c.expected}
}

type maintenancePods struct {
	typedcorev1.PodInterface
	namespace string
	expected  map[string]types.UID
}

//nolint:gocritic // The native PodInterface requires DeleteOptions by value.
func (p maintenancePods) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	uid, err := maintenanceUID(p.expected, p.namespace, name)
	if err != nil {
		return err
	}
	opts.Preconditions = &metav1.Preconditions{UID: &uid}
	operationBeginWrite(ctx)
	err = p.PodInterface.Delete(ctx, name, opts)
	if err == nil {
		operationAcceptWrite(ctx, fmt.Sprintf("Pod delete %s/%s (UID %s)", p.namespace, name, uid))
	}
	return err
}

type maintenancePolicy struct {
	typedpolicyv1.PolicyV1Interface
	expected map[string]types.UID
}

func (p maintenancePolicy) Evictions(namespace string) typedpolicyv1.EvictionInterface {
	return maintenanceEvictions{EvictionInterface: p.PolicyV1Interface.Evictions(namespace), namespace: namespace, expected: p.expected}
}

type maintenanceEvictions struct {
	typedpolicyv1.EvictionInterface
	namespace string
	expected  map[string]types.UID
}

func (p maintenanceEvictions) Evict(ctx context.Context, eviction *policyv1.Eviction) error {
	uid, err := maintenanceUID(p.expected, p.namespace, eviction.Name)
	if err != nil {
		return err
	}
	eviction = eviction.DeepCopy()
	if eviction.DeleteOptions == nil {
		eviction.DeleteOptions = &metav1.DeleteOptions{}
	}
	eviction.DeleteOptions.Preconditions = &metav1.Preconditions{UID: &uid}
	operationBeginWrite(ctx)
	err = p.EvictionInterface.Evict(ctx, eviction)
	if err == nil {
		operationAcceptWrite(ctx, fmt.Sprintf("Pod eviction %s/%s (UID %s)", p.namespace, eviction.Name, uid))
	}
	return err
}

type maintenancePolicyBeta struct {
	typedpolicyv1beta1.PolicyV1beta1Interface
	expected map[string]types.UID
}

func (p maintenancePolicyBeta) Evictions(namespace string) typedpolicyv1beta1.EvictionInterface {
	return maintenanceEvictionsBeta{EvictionInterface: p.PolicyV1beta1Interface.Evictions(namespace), namespace: namespace, expected: p.expected}
}

type maintenanceEvictionsBeta struct {
	typedpolicyv1beta1.EvictionInterface
	namespace string
	expected  map[string]types.UID
}

func (p maintenanceEvictionsBeta) Evict(ctx context.Context, eviction *policyv1beta1.Eviction) error {
	uid, err := maintenanceUID(p.expected, p.namespace, eviction.Name)
	if err != nil {
		return err
	}
	eviction = eviction.DeepCopy()
	if eviction.DeleteOptions == nil {
		eviction.DeleteOptions = &metav1.DeleteOptions{}
	}
	eviction.DeleteOptions.Preconditions = &metav1.Preconditions{UID: &uid}
	operationBeginWrite(ctx)
	err = p.EvictionInterface.Evict(ctx, eviction)
	if err == nil {
		operationAcceptWrite(ctx, fmt.Sprintf("Pod eviction %s/%s (UID %s)", p.namespace, eviction.Name, uid))
	}
	return err
}
func maintenanceUID(expected map[string]types.UID, namespace, name string) (types.UID, error) {
	uid := expected[client.FQN(namespace, name)]
	if uid == "" {
		return "", fmt.Errorf("Pod identity unavailable for %s/%s; this request was not submitted", namespace, name)
	}
	return uid, nil
}

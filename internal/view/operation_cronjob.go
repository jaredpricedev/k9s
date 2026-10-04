// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/derailed/k9s/internal/client"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

//nolint:gocritic // Preserve the captured CronJob identity in the worker.
func (s *operationSession) triggerCronJob(ctx context.Context, target SelectedResourceTarget) error {
	if err := checkOperationTarget(&target); err != nil {
		return err
	}
	if target.GVR.GVR() != client.CjGVR.GVR() {
		return fmt.Errorf("manual Job requires a native CronJob selection")
	}
	if err := s.authorize(ctx, &target, "", client.GetVerb); err != nil {
		return err
	}
	jobs := SelectedResourceTarget{Context: target.Context, GVR: client.JobGVR, Namespace: target.Namespace}
	if err := s.authorize(ctx, &jobs, "", client.CreateVerb); err != nil {
		return err
	}
	obj, err := s.readTarget(ctx, &target)
	if err != nil {
		return err
	}
	var cron batchv1.CronJob
	if convertErr := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &cron); convertErr != nil {
		return convertErr
	}
	prefix := cron.Name
	if len(prefix) > 42 {
		prefix = prefix[:42]
	}
	controller := true
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		GenerateName: prefix + "-manual-", Namespace: target.Namespace,
		Labels: maps.Clone(cron.Spec.JobTemplate.Labels), Annotations: maps.Clone(cron.Spec.JobTemplate.Annotations),
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: target.GVR.GV().String(), Kind: "CronJob", Name: target.Name, UID: target.UID,
			Controller: &controller, BlockOwnerDeletion: &controller,
		}},
	}, Spec: *cron.Spec.JobTemplate.Spec.DeepCopy()}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	operationBeginWrite(ctx)
	created, err := s.typed.BatchV1().Jobs(target.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err == nil && created != nil {
		operationAcceptWrite(ctx, fmt.Sprintf("manual Job %s/%s (UID %s), owner CronJob UID %s", created.Namespace, created.Name, created.UID, target.UID))
	}
	return err
}

//nolint:gocritic // Never toggle a different or newly changed CronJob.
func (s *operationSession) suspendCronJob(ctx context.Context, target SelectedResourceTarget, expected, desired bool) error {
	if err := checkOperationTarget(&target); err != nil {
		return err
	}
	if target.GVR.GVR() != client.CjGVR.GVR() {
		return fmt.Errorf("scheduling operation requires a native CronJob selection")
	}
	if err := s.authorize(ctx, &target, "", client.GetVerb, client.PatchVerb); err != nil {
		return err
	}
	return retryOperationConflict(ctx, func() error {
		obj, err := s.readTarget(ctx, &target)
		if err != nil {
			return err
		}
		actual, _, err := unstructured.NestedBool(obj.Object, "spec", "suspend")
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("CronJob scheduling state changed; review again")
		}
		patch, err := json.Marshal(map[string]any{
			operationMetadataField: map[string]any{operationUIDField: target.UID, operationResourceVersionField: obj.GetResourceVersion()},
			operationSpecField:     map[string]any{"suspend": desired},
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
			operationAcceptWrite(ctx, fmt.Sprintf("CronJob suspend=%t; existing Jobs continue", desired))
		}
		return err
	})
}

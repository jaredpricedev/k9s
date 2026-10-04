// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	fake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func cronOperationFixture(t *testing.T) (*operationSession, SelectedResourceTarget) {
	t.Helper()
	s, target, _ := operationFixture(t)
	target.GVR, target.Name, target.UID = client.CjGVR, guardedTestNightly, guardedTestCronUID
	suspended := false
	cron := &batchv1.CronJob{TypeMeta: metav1.TypeMeta{APIVersion: batchv1.SchemeGroupVersion.String(), Kind: "CronJob"}, ObjectMeta: metav1.ObjectMeta{Namespace: guardedTestNamespace, Name: guardedTestNightly, UID: guardedTestCronUID, ResourceVersion: "12"}, Spec: batchv1.CronJobSpec{Suspend: &suspended, JobTemplate: batchv1.JobTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{investigationAppRole: guardedTestNightly}}, Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "main", Image: "example:1"}}}}}}}}
	encoded, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cron)
	if err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{Object: encoded}

	if _, createErr := s.resource(&target).Create(t.Context(), obj, metav1.CreateOptions{}); createErr != nil {
		t.Fatal(createErr)
	}
	return s, target
}

func TestGuardedCronJobTriggerRetainsCreatedIdentityAndProviderOwnership(t *testing.T) {
	s, target := cronOperationFixture(t)
	typed := s.typed.(*kubefake.Clientset)
	created := 0
	typed.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		created++
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		if job.GenerateName != "nightly-manual-" || job.Namespace != guardedTestNamespace || job.Labels[investigationAppRole] != guardedTestNightly || len(job.OwnerReferences) != 1 || job.OwnerReferences[0].UID != target.UID || job.Spec.Template.Spec.Containers[0].Image != "example:1" {
			t.Error("native template/ownership changed", job)
		}
		job = job.DeepCopy()
		job.Name = "nightly-manual-xyz"
		job.UID = "created-job-uid"
		return true, job, nil
	})
	task := startOperationBatch(time.Second, []SelectedResourceTarget{target}, s.triggerCronJob, nil, nil)
	receipt := waitOperationTask(t, task)
	text := operationReceiptText(receipt, 1, 1)
	if created != 1 || receipt.Outcomes[0].State != operationAccepted || !strings.Contains(text, "created-job-uid") || !strings.Contains(text, guardedTestCronUID) {
		t.Fatal(created, text)
	}
	target.UID = "old-cron-uid"
	if err := s.triggerCronJob(t.Context(), target); err == nil || created != 1 {
		t.Fatal("replacement CronJob triggered", err, created)
	}
}

func TestGuardedCronJobSuspendChecksReviewedStateAndConditionalPatch(t *testing.T) {
	s, target := cronOperationFixture(t)
	writes := 0
	s.dynamic.(*fake.FakeDynamicClient).PrependReactor("patch", "cronjobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		writes++
		var patch map[string]any
		if err := json.Unmarshal(action.(ktesting.PatchAction).GetPatch(), &patch); err != nil {
			t.Error(err)
		}
		metadata := patch["metadata"].(map[string]any)
		if metadata["uid"] != guardedTestCronUID || metadata["resourceVersion"] != "12" || patch["spec"].(map[string]any)["suspend"] != true {
			t.Error("unconditional scheduling patch", patch)
		}
		return true, &unstructured.Unstructured{}, nil
	})
	if err := s.suspendCronJob(t.Context(), target, false, true); err != nil || writes != 1 {
		t.Fatal(err, writes)
	}
	if err := s.suspendCronJob(t.Context(), target, true, false); err == nil || writes != 1 {
		t.Fatal("changed scheduling status was toggled", err, writes)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.suspendCronJob(ctx, target, false, true); err == nil || writes != 1 {
		t.Fatal("canceled scheduling wrote", err, writes)
	}
}

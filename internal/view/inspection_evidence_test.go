// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/dao"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const evidenceTestNamespace = "team"

func TestCrashLoopEvidencePrecedesOwnersAndRetainsFullMessages(t *testing.T) {
	metas := dao.MetaAccess
	dao.MetaAccess = dao.NewMeta()
	t.Cleanup(func() { dao.MetaAccess = metas })
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"namespace": evidenceTestNamespace, "name": "app", "uid": "pod-uid"},
		"status": map[string]any{
			"phase":             "Running",
			"conditions":        []any{map[string]any{"type": "PodScheduled", "status": "True"}, map[string]any{"type": "Ready", "status": "False", "reason": "ContainersNotReady", "message": "full readiness evidence [red] stays literal"}},
			"containerStatuses": []any{map[string]any{"name": "api", "ready": false, "restartCount": int64(7), "state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff", "message": "full crash-loop evidence"}}, "lastState": map[string]any{"terminated": map[string]any{"reason": "OOMKilled", "exitCode": int64(137)}}}},
		},
	}}
	typed := kubefake.NewSimpleClientset()
	typed.PrependReactor("list", "events", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() != evidenceTestNamespace || a.(ktesting.ListAction).GetListRestrictions().Fields.String() != "involvedObject.uid=pod-uid" {
			t.Fatal("event evidence changed scope", a)
		}
		return true, &corev1.EventList{Items: []corev1.Event{
			{ObjectMeta: metav1.ObjectMeta{Name: "failure"}, InvolvedObject: corev1.ObjectReference{UID: "pod-uid"},
				Reason: "BackOff", Message: "full retained event message", Type: "Warning"},
			{InvolvedObject: corev1.ObjectReference{UID: "other"}, Message: "wrong-object-must-not-appear"},
		}}, nil
	})
	target := resourceTargetForPath(client.PodGVR, "cluster", "team/app")
	snapshot, err := loadTargetInspectionSnapshot(t.Context(), inspectionConnection{dynamic: fake.NewSimpleDynamicClient(runtime.NewScheme(), obj), typed: typed}, target, troubleshootCommand)
	if err != nil || snapshot.UID != "pod-uid" || snapshot.CapturedAt.IsZero() {
		t.Fatal(snapshot, err)
	}
	text := snapshot.Text
	if strings.Contains(text, "wrong-object-must-not-appear") {
		t.Fatal("retained event snapshot broadened UID scope", text)
	}
	owners := strings.Index(text, "\nOWNERS\n")
	for _, evidence := range []string{"CrashLoopBackOff", "OOMKilled", "restarts: 7", "CONDITIONS", "EVENTS", "full readiness evidence [red] stays literal", "full retained event message"} {
		if index := strings.Index(text, evidence); index < 0 || index > owners {
			t.Fatal("critical evidence after owners or missing", evidence, text)
		}
	}
	if strings.Index(text, "Ready: False") > strings.Index(text, "PodScheduled: True") || !strings.Contains(text, "bounded observation") || !strings.Contains(text, "not a complete history") {
		t.Fatal("evidence priority or completeness missing", text)
	}
}

func TestInspectionRefreshRetainsIdentityQueryOffsetAndPreviousSnapshotOnFailure(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	d := &inspectionDetails{Details: NewDetails(app, troubleshootCommand, "team/app", contentInspection, true)}
	d.model.AddListener(d.Details)
	d.text.SetDynamicColors(true)
	d.cmdBuff.SetText("CrashLoop", "", true)
	d.BufferCompleted("CrashLoop", "")
	d.text.ScrollTo(4, 2)
	first := inspectionSnapshot{Text: strings.Repeat("CrashLoop evidence\n", 30), UID: retainedEvidenceUID, CapturedAt: time.Now()}
	d.acceptSnapshot(first, nil)
	row, col := d.text.GetScrollOffset()
	if d.target.UID != retainedEvidenceUID || d.cmdBuff.GetText() != "CrashLoop" || row != 4 || col != 2 || d.maxRegions == 0 {
		t.Fatal("refresh lost captured identity or navigation", d.target, d.cmdBuff.GetText(), row, col, d.maxRegions)
	}
	d.acceptSnapshot(inspectionSnapshot{}, fmt.Errorf("identity changed; replacement UID"))
	if d.snapshot != first || d.target.UID != retainedEvidenceUID || !strings.Contains(d.text.GetText(true), "RETAINED SNAPSHOT") || !strings.Contains(d.text.GetText(true), "identity changed") {
		t.Fatal("failed observation discarded evidence or silently replaced identity", d.snapshot, d.target, d.text.GetText(true))
	}
	d.Stop()
	d.Start()
	if d.cmdBuff.GetText() != "CrashLoop" || d.snapshot != first {
		t.Fatal("back navigation changed query or snapshot")
	}
}

func TestInspectionSearchRejectsInvalidRegexWithoutReplacingEvidence(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	d := NewDetails(app, troubleshootCommand, "team/app", contentInspection, true).Update("[red] reported evidence")
	d.model.AddListener(d)
	d.BufferCompleted("evidence", "")
	d.BufferCompleted("[", "")
	if text := strings.Join(d.model.Peek(), "\n"); text != "[red] reported evidence" || !d.validSearch("-f evidence") || d.inspectionQuery != "evidence" {
		t.Fatal("invalid inspection search changed the retained evidence", text)
	}
	if !strings.Contains(d.ExtraHints()["Search"], "regex") {
		t.Fatal("inspection query grammar unlabeled")
	}
}

func TestInspectionMessagesExpandFromRetainedEvidenceWithoutANewRead(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	d := &inspectionDetails{Details: NewDetails(app, troubleshootCommand, "team/app", contentInspection, true)}
	d.model.AddListener(d.Details)
	full := inspectionMessage(strings.Repeat("first message ", 40) + "\nsecond full evidence line")
	snapshot := inspectionSnapshot{Text: full, UID: "captured", CapturedAt: time.Now()}
	d.messagesCompact = true
	d.acceptSnapshot(snapshot, nil)
	if strings.Contains(d.text.GetText(true), "second full evidence line") || !strings.Contains(d.text.GetText(true), "m expands") || d.snapshot.Text != full {
		t.Fatal("compact messages lost full snapshot evidence", d.text.GetText(true), d.snapshot)
	}
	d.messagesCompact = false
	d.renderSnapshotText(d.displayEvidence)
	if !strings.Contains(d.text.GetText(true), "second full evidence line") || d.target.UID != "captured" {
		t.Fatal("expanded messages lost evidence or selected identity", d.text.GetText(true), d.target)
	}
}

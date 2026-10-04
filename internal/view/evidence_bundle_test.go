// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const (
	retainedEvidenceUID = "observed"
	evidenceBaselineUID = "old"
)

func TestEvidenceCaptureScopesEventsAndKeepsSources(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"namespace": "app", "name": "api", "uid": "pod-a"}, "spec": map[string]any{"containers": []any{map[string]any{"name": "api", "env": []any{map[string]any{"name": "API_TOKEN", "value": "private-value"}}}}}}}
	typed := kubefake.NewSimpleClientset()
	typed.PrependReactor(client.ListVerb, "events", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() != "app" || a.(ktesting.ListAction).GetListRestrictions().Fields.String() != "involvedObject.uid=pod-a" {
			t.Fatal("event scope widened", a)
		}
		return true, &corev1.EventList{Items: []corev1.Event{
			{ObjectMeta: metav1.ObjectMeta{Name: "matching"}, InvolvedObject: corev1.ObjectReference{UID: types.UID("pod-a")}, Reason: "BackOff", Message: "Bearer event-token"},
			{ObjectMeta: metav1.ObjectMeta{Name: "wrong"}, InvolvedObject: corev1.ObjectReference{UID: types.UID("other")}, Message: "must-not-include"},
		}}, nil
	})
	conn := inspectionConnection{dynamic: fake.NewSimpleDynamicClient(runtime.NewScheme(), obj), typed: typed}
	target := SelectedResourceTarget{Context: "fixture", GVR: client.PodGVR, Namespace: "app", Name: "api", UID: "pod-a"}
	bundle, err := loadEvidenceBundle(t.Context(), conn, target)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := inspect.EncodeBundle(bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"private-value", "event-token", "must-not-include"} {
		if strings.Contains(string(encoded), value) {
			t.Fatal("unsafe or wrongly scoped evidence", string(encoded))
		}
	}
	if len(bundle.Snippets) != 1 || !strings.Contains(bundle.Snippets[0].Source, "pod-a") || bundle.Observations[0].Identity.Context != "fixture" || bundle.Observations[0].ObservedAt.IsZero() {
		t.Fatal("lost source/time", bundle)
	}
	if _, err := json.Marshal(bundle); err != nil {
		t.Fatal(err)
	}
}

func TestEvidenceCaptureRejectsChangedIdentity(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"namespace": "app", "name": "api", "uid": "replacement"}}}
	conn := inspectionConnection{dynamic: fake.NewSimpleDynamicClient(runtime.NewScheme(), obj), typed: kubefake.NewSimpleClientset()}
	_, err := loadEvidenceBundle(t.Context(), conn, SelectedResourceTarget{Context: "fixture", GVR: client.PodGVR, Namespace: "app", Name: "api", UID: "selected"})
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatal("captured replacement silently", err)
	}
}

func TestEvidenceSecretPreviewDoesNotFetchSecretBody(t *testing.T) {
	dyn := fake.NewSimpleDynamicClient(runtime.NewScheme())
	bundle, err := loadEvidenceBundle(t.Context(), inspectionConnection{dynamic: dyn}, SelectedResourceTarget{Context: "fixture", GVR: client.NewGVR("v1/secrets"), Namespace: "app", Name: "tls", UID: "known"})
	if err != nil || len(dyn.Actions()) != 0 || len(bundle.Observations) != 1 || bundle.Observations[0].State != inspect.ObservationIncomplete {
		t.Fatal("default export fetched Secret or lost exclusion", bundle, err, dyn.Actions())
	}
}

func TestEvidenceCaptureRedactsCredentialAcrossEventMessages(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"namespace": "app", "name": "api", "uid": "pod-a"},
	}}
	typed := kubefake.NewSimpleClientset()
	typed.PrependReactor(client.ListVerb, "events", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.EventList{Items: []corev1.Event{
			{InvolvedObject: corev1.ObjectReference{UID: "pod-a"}, Message: "-----BEGIN PRIVATE KEY-----"},
			{InvolvedObject: corev1.ObjectReference{UID: "pod-a"}, Message: "private-key-continuation"},
			{InvolvedObject: corev1.ObjectReference{UID: "pod-a"}, Message: "-----END PRIVATE KEY-----"},
		}}, nil
	})
	conn := inspectionConnection{dynamic: fake.NewSimpleDynamicClient(runtime.NewScheme(), obj), typed: typed}
	bundle, err := loadEvidenceBundle(t.Context(), conn, SelectedResourceTarget{
		Context: "fixture", GVR: client.PodGVR, Namespace: "app", Name: "api", UID: "pod-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := inspect.EncodeBundle(bundle, false)
	if err != nil || strings.Contains(string(encoded), "private-key-continuation") {
		t.Fatal("event aggregation lost credential boundary", string(encoded), err)
	}
	for _, action := range append(conn.dynamic.(*fake.FakeDynamicClient).Actions(), typed.Actions()...) {
		if action.GetVerb() != "get" && action.GetVerb() != client.ListVerb {
			t.Fatal("capture issued an API write", action)
		}
	}
}

func evidenceFormForTest(t *testing.T, state *evidenceForm) *tview.Form {
	t.Helper()
	var form *tview.Form
	state.modal.Focus(func(p tview.Primitive) { form, _ = p.(*tview.Form) })
	if form == nil {
		t.Fatal("modal has no form")
	}
	return form
}

func evidenceKeyForTest(v *evidenceView, primitive tview.Primitive, key tcell.Key) {
	primitive.InputHandler()(tcell.NewEventKey(key, 0, 0), func(p tview.Primitive) { v.app.SetFocus(p) })
}

func evidenceViewForTest(t *testing.T) *evidenceView {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	observation := inspect.NewObservation(inspect.ResourceIdentity{Context: "offline", GVR: "v1/pods", Name: "api"},
		"offline fixture", time.Now(), map[string]any{"kind": "Pod"})
	v := &evidenceView{Details: NewDetails(app, "Offline evidence", "import", contentInspection, true),
		bundle: inspect.NewBundle([]inspect.Observation{observation}), ready: true}
	app.Content.Push(v)
	return v
}

func TestEvidenceStopRemovesOwnedFormsAndRejectsAbandonedCallbacks(t *testing.T) {
	for _, save := range []bool{false, true} {
		t.Run(fmt.Sprintf("save=%v", save), func(t *testing.T) {
			v := evidenceViewForTest(t)
			page := "evidence-note"
			if save {
				page = "evidence-save"
				v.saveForm()
			} else {
				v.noteForm(false)
			}
			state := v.forms[page]
			form := evidenceFormForTest(t, state)
			destination := filepath.Join(t.TempDir(), "abandoned.json")
			form.GetFormItem(0).(*tview.InputField).SetText(destination)
			next := NewDetails(v.app, "next", "", contentTXT, false)
			v.app.Content.Push(next)
			v.app.SetFocus(next)
			focus := v.app.GetFocus()
			if v.app.Content.GetPrimitive(page) != nil || len(v.forms) != 0 {
				t.Fatal("abandoned evidence modal survived navigation")
			}
			evidenceKeyForTest(v, form.GetButton(1), tcell.KeyEnter)
			evidenceKeyForTest(v, form.GetButton(0), tcell.KeyEnter)
			if len(v.bundle.Notes) != 0 || v.app.GetFocus() != focus {
				t.Fatal("abandoned callback changed evidence or focus")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("abandoned callback saved a file", err)
			}
		})
	}
}

func TestEvidenceReplacedFormAndEscapeRespectPageOwnership(t *testing.T) {
	v := evidenceViewForTest(t)
	v.noteForm(false)
	old := evidenceFormForTest(t, v.forms["evidence-note"])
	v.noteForm(false)
	current := v.forms["evidence-note"]
	old.GetFormItem(0).(*tview.InputField).SetText("abandoned note")
	evidenceKeyForTest(v, old.GetButton(1), tcell.KeyEnter)
	evidenceKeyForTest(v, old.GetButton(0), tcell.KeyEnter)
	if v.app.Content.GetPrimitive("evidence-note") != current.modal || len(v.bundle.Notes) != 0 {
		t.Fatal("old callback acted on replacement form")
	}
	input := v.app.GetFocus()
	evidenceKeyForTest(v, input, tcell.KeyEscape)
	if v.app.Content.GetPrimitive("evidence-note") != nil || len(v.forms) != 0 || v.app.Content.Top() != v {
		t.Fatal("Escape left modal or changed offline evidence owner")
	}
}

func TestEvidenceRetainsComparisonBaselineAndCapturedBWithoutReload(t *testing.T) {
	v := &comparisonView{}
	if _, err := retainedEvidenceBundle(v); err == nil {
		t.Fatal("uncaptured baseline became exportable evidence")
	}
	at := time.Date(2026, 10, 4, 1, 2, 3, 0, time.UTC)
	baseline := inspect.NewObservation(inspect.ResourceIdentity{Context: "captured", GVR: "v1/pods", Name: "api", UID: evidenceBaselineUID},
		"original baseline GET", at, map[string]any{"kind": "Pod", "spec": map[string]any{"replicas": int64(1)}})
	v.baseline = &baseline
	bundle, err := retainedEvidenceBundle(v)
	if err != nil || len(bundle.Observations) != 1 || bundle.Observations[0].ObservedAt != at {
		t.Fatal("baseline-only evidence lost original capture", bundle, err)
	}
	v.other = inspect.NewObservation(inspect.ResourceIdentity{Context: "captured", GVR: "v1/pods", Name: "api", UID: "new"},
		"original comparison GET", at.Add(time.Minute), map[string]any{"kind": "Pod", "spec": map[string]any{"replicas": int64(2)}})
	bundle, err = retainedEvidenceBundle(v)
	if err != nil || len(bundle.Observations) != 2 || bundle.Observations[0].Identity.UID != evidenceBaselineUID || bundle.Observations[1].Identity.UID != "new" {
		t.Fatal("comparison export lost distinct retained identities", bundle, err)
	}
	bundle.Observations[0].Object["spec"].(map[string]any)["replicas"] = int64(99)
	if baseline.Object["spec"].(map[string]any)["replicas"] != int64(1) {
		t.Fatal("export aliases immutable comparison baseline")
	}
}

func TestEvidenceRetainsLastSuccessfulInspectionWithCaptureTimeAndLimits(t *testing.T) {
	v := &inspectionDetails{Details: NewDetails(nil, troubleshootCommand, "app/api", contentInspection, true),
		target: SelectedResourceTarget{Context: "captured", GVR: client.PodGVR, Namespace: "app", Name: "api", UID: "selected"}}
	if _, err := retainedEvidenceBundle(v); err == nil {
		t.Fatal("uncaptured inspector became exportable evidence")
	}
	at := time.Date(2026, 10, 4, 1, 2, 3, 0, time.UTC)
	v.snapshot = inspectionSnapshot{Text: "retained evidence Bearer credential-token\n" + strings.Repeat("é", 5000), UID: retainedEvidenceUID, CapturedAt: at}
	v.displayEvidence = "new refresh failed and must not replace retained capture"
	bundle, err := retainedEvidenceBundle(v)
	if err != nil {
		t.Fatal(err)
	}
	observation := bundle.Observations[0]
	if observation.Identity.UID != retainedEvidenceUID || observation.ObservedAt != at || observation.State != inspect.ObservationIncomplete {
		t.Fatal("inspector export recaptured selection or claimed complete object", observation)
	}
	if len(bundle.Snippets) != 1 || bundle.Snippets[0].ObservedAt != at || len(bundle.Snippets[0].Text) > 8<<10 ||
		!strings.Contains(bundle.Snippets[0].Limits, "truncated") || !strings.Contains(bundle.Snippets[0].Text, "retained evidence") ||
		strings.Contains(bundle.Snippets[0].Text, "credential-token") || strings.Contains(bundle.Snippets[0].Text, "new refresh failed") {
		t.Fatal("inspector export lost retained evidence boundary", bundle.Snippets)
	}
}

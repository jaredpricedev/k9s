// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const (
	desiredReviewFixtureUID       = "review-original-uid"
	desiredReviewFixtureContext   = "lab"
	desiredReviewFixtureNamespace = "team"
	desiredReviewFixturePath      = "/tmp/authored.yaml"
	desiredReviewFixtureHash      = "source-hash"
	desiredReviewFixtureOldUID    = "old-review-uid"
)

func desiredReviewFixture() *desiredReviewView {
	app := &App{App: &ui.App{Application: tview.NewApplication(), Configurator: ui.Configurator{Styles: config.NewStyles()}}}
	w := newDesiredReviewView(app, review.Scope{Context: desiredReviewFixtureContext, Namespaces: []string{desiredReviewFixtureNamespace}, DefaultNamespace: desiredReviewFixtureNamespace}, desiredReviewFixturePath)
	w.source = review.Source{Identity: review.SourceIdentity{Path: desiredReviewFixturePath, SHA256: desiredReviewFixtureHash, LoadedAt: time.Date(2026, 10, 4, 1, 0, 0, 0, time.UTC), Documents: 2}}
	return w
}
func desiredReviewFixtureEntry(name, state string, at time.Time) review.Entry {
	return review.Entry{Identity: review.Identity{Context: desiredReviewFixtureContext, APIVersion: "apps/v1", Kind: "Deployment", Namespace: desiredReviewFixtureNamespace, Name: name, GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, UID: types.UID(desiredReviewFixtureUID)}, State: state, ObservedAt: at,
		Intent: review.IntentResult{DeclaredFields: 3, MatchedFields: 2, Changes: []review.Change{{Path: "/spec/template/spec/containers/0/image", Kind: "changed", Before: "example/api:v1", After: "example/api:v2"}}, Unreviewed: []string{"live-only and redacted fields excluded"}}}
}
func desiredReviewFixtureSnapshot(w *desiredReviewView, at time.Time, entries ...review.Entry) review.Snapshot {
	return review.Snapshot{Source: w.source.Identity, Scope: w.scope, ObservedAt: at, Entries: entries}
}

func TestDesiredReviewMixedReadRetainsFailedEntryEvidenceAndOriginalTime(t *testing.T) {
	w := desiredReviewFixture()
	first := time.Date(2026, 10, 4, 2, 0, 0, 0, time.UTC)
	a, b := desiredReviewFixtureEntry(testWorkspaceAPIName, review.StateChanged, first), desiredReviewFixtureEntry(testWorkspaceWorkerName, review.StateMatch, first)
	w.acceptSnapshot(desiredReviewFixtureSnapshot(w, first, a, b), nil)
	later := first.Add(time.Minute)
	a.State = review.StateMatch
	a.ObservedAt = later
	a.Intent.Changes = nil
	denied := b
	denied.State = review.StateDenied
	denied.Reason = "forbidden"
	denied.ObservedAt = later
	denied.Intent = review.IntentResult{}
	w.acceptSnapshot(desiredReviewFixtureSnapshot(w, later, a, denied), nil)
	w.render()
	if w.snapshot.Entries[0].ObservedAt != later || w.snapshot.Entries[0].State != review.StateMatch {
		t.Fatal("successful read stayed stale")
	}
	retained := w.snapshot.Entries[1]
	if retained.ObservedAt != first || retained.State != review.StateMatch || len(retained.Intent.Changes) != 1 {
		t.Fatal("failed row lost prior values or original timestamp")
	}
	if w.latest[1].State != review.StateDenied || !strings.Contains(w.retainedReasons[desiredReviewEntryKey(&retained)], review.StateDenied) {
		t.Fatal("latest denial was hidden")
	}
	w.table.Select(2, 0)
	w.detailOpen = true
	w.detailKey = desiredReviewEntryKey(&retained)
	w.render()
	if !strings.Contains(w.detail.GetText(true), "RETAINED EVIDENCE") || !strings.Contains(w.detail.GetText(true), first.UTC().Format(time.RFC3339)) {
		t.Fatal("retained detail relabeled old evidence")
	}
}
func TestDesiredReviewFailedRefreshAndDifferentSourcePreserveLastGood(t *testing.T) {
	w := desiredReviewFixture()
	at := time.Now().UTC()
	good := desiredReviewFixtureEntry(testWorkspaceAPIName, review.StateChanged, at)
	w.acceptSnapshot(desiredReviewFixtureSnapshot(w, at, good), nil)
	failed := desiredReviewFixtureSnapshot(w, at.Add(time.Minute))
	w.acceptSnapshot(failed, context.DeadlineExceeded)
	if !w.snapshot.ObservedAt.Equal(at) || len(w.snapshot.Entries) != 1 || !strings.Contains(w.notice, "retained") {
		t.Fatal("deadline replaced accepted evidence")
	}
	mismatch := desiredReviewFixtureSnapshot(w, at.Add(time.Minute))
	mismatch.Source.SHA256 = "other-source"
	w.acceptSnapshot(mismatch, nil)
	if !w.snapshot.ObservedAt.Equal(at) || w.source.Identity.SHA256 != desiredReviewFixtureHash {
		t.Fatal("live refresh changed authored source")
	}
}
func TestDesiredReviewSelectionUsesCapturedUIDAndRejectsRecreatedOrSecretIdentity(t *testing.T) {
	w := desiredReviewFixture()
	at := time.Now()
	entry := desiredReviewFixtureEntry(testWorkspaceAPIName, review.StateChanged, at)
	w.acceptSnapshot(desiredReviewFixtureSnapshot(w, at, entry), nil)
	w.render()
	key := review.IdentityKey(entry.Identity.GVR, entry.Identity.Namespace, entry.Identity.Name)
	if w.scope.CapturedUIDs[key] != desiredReviewFixtureUID || w.SelectedResource().UID != desiredReviewFixtureUID {
		t.Fatal("first accepted UID was not captured")
	}
	replacement := entry
	replacement.Identity.UID = "replacement-uid"
	replacement.State = review.StateStale
	w.acceptSnapshot(desiredReviewFixtureSnapshot(w, at.Add(time.Minute), replacement), nil)
	w.render()
	if w.scope.CapturedUIDs[key] != desiredReviewFixtureUID || w.SelectedResource().Err() == nil {
		t.Fatal("review followed replacement identity")
	}
	secret := entry
	secret.Identity.Kind = desiredReviewSecretKind
	secret.Identity.GVR = schema.GroupVersionResource{Version: "v1", Resource: desiredReviewSecrets}
	secret.Intent.Changes = []review.Change{{Before: "DO-NOT-DISPLAY", After: "DO-NOT-DISPLAY"}}
	w.snapshot.Entries = []review.Entry{secret}
	w.latest = nil
	w.render()
	if w.SelectedResource().Err() == nil || strings.Contains(renderDesiredReviewEntry(&secret), "DO-NOT-DISPLAY") {
		t.Fatal("Secret values or identity exposed by review")
	}
}
func TestDesiredReviewLifecycleBlocksQueuesAfterExitAndRejectsChangedDestination(t *testing.T) {
	w := desiredReviewFixture()
	w.app.SetRunning(true)
	w.active = true
	queued := 0
	w.enqueue = func(func()) { queued++ }
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.generation = 3
	if !w.submit(ctx, func() {}) || queued != 1 {
		t.Fatal("active result was not queued")
	}
	w.Stop()
	if ctx.Err() != context.Canceled || w.generation != 4 || w.submit(context.Background(), func() {}) || queued != 1 {
		t.Fatal("exit accepted late result or did not cancel")
	}
	for _, test := range []struct {
		context, current          string
		revision, currentRevision uint64
		top, want                 bool
	}{{desiredReviewFixtureContext, desiredReviewFixtureContext, 1, 1, true, true}, {desiredReviewFixtureContext, "prod", 1, 1, true, false}, {desiredReviewFixtureContext, desiredReviewFixtureContext, 1, 2, true, false}, {desiredReviewFixtureContext, desiredReviewFixtureContext, 1, 1, false, false}} {
		if got := desiredReviewAccepts(test.context, test.current, test.revision, test.currentRevision, test.top); got != test.want {
			t.Fatal("changed destination accepted")
		}
	}
}
func TestDesiredReviewCopiesCapturedScopeAndRetainsLocalSearchSelection(t *testing.T) {
	original := review.Scope{Context: desiredReviewFixtureContext, Namespaces: []string{desiredReviewFixtureNamespace}, CapturedUIDs: map[string]types.UID{"resource": desiredReviewFixtureOldUID}}
	copied := copyDesiredReviewScope(original)
	original.Namespaces[0] = guardedTestOtherContext
	original.CapturedUIDs["resource"] = "new"
	if copied.Namespaces[0] != desiredReviewFixtureNamespace || copied.CapturedUIDs["resource"] != desiredReviewFixtureOldUID {
		t.Fatal("scope changed under async reader")
	}
	w := desiredReviewFixture()
	at := time.Now()
	a, b := desiredReviewFixtureEntry(testWorkspaceAPIName, review.StateChanged, at), desiredReviewFixtureEntry(testWorkspaceWorkerName, review.StateChanged, at)
	w.acceptSnapshot(desiredReviewFixtureSnapshot(w, at, a, b), nil)
	w.SetFilter(testWorkspaceWorkerName, false)
	if len(w.rows) != 1 || w.SelectedResource().Name != testWorkspaceWorkerName {
		t.Fatal("local query lost selection")
	}
	w.acceptSnapshot(desiredReviewFixtureSnapshot(w, at.Add(time.Minute), b, a), nil)
	w.render()
	if w.query != testWorkspaceWorkerName || w.SelectedResource().Name != testWorkspaceWorkerName {
		t.Fatal("refresh lost accepted query or selected identity")
	}
}
func TestDesiredReviewPaintAt80ColumnsShowsContextCoverageAndSafeDetail(t *testing.T) {
	w := desiredReviewFixture()
	at := time.Date(2026, 10, 4, 2, 0, 0, 0, time.UTC)
	entry := desiredReviewFixtureEntry(testWorkspaceAPIName, review.StateChanged, at)
	entry.Reason = "[red]reported"
	entry.Intent.Unreviewed = []string{"[blue]unreviewed"}
	w.acceptSnapshot(desiredReviewFixtureSnapshot(w, at, entry), nil)
	w.render()
	paint := drawnText(t, w, 80, 24)
	for _, expected := range []string{desiredReviewFixtureContext, desiredReviewFixtureNamespace, "SHA256", desiredReviewFixtureHash, "1 resources", "STATUS", "reviewed matches", "Loaded 2026-10-04T01:00:00Z", "Read 2026-10-04T02:00:00Z"} {
		if !strings.Contains(paint, expected) {
			t.Fatalf("missing %q:\n%s", expected, paint)
		}
	}
	detail := renderDesiredReviewEntry(&entry)
	w.detail.SetText(detail)
	text := drawnText(t, w.detail, 80, 24)
	if !strings.Contains(text, "[red]reported") || !strings.Contains(text, "[blue]unreviewed") {
		t.Fatal("resource markup interpreted")
	}
	// Presentation and search never inspect or serialize raw authored objects.
	w.source.Objects = []review.Manifest{{Object: map[string]any{"private": "DO-NOT-DISPLAY"}}}
	w.notice = string(capabilityUnavailable)
	w.render()
	if strings.Contains(drawnText(t, w, 80, 24), "DO-NOT-DISPLAY") {
		t.Fatal("raw authored object exposed")
	}
}
func TestDesiredReviewCompactHeaderKeepsSourceFingerprintAndReadTimes(t *testing.T) {
	w := desiredReviewFixture()
	w.source.Identity.Path = "/tmp/release-bundles/a-long-directory-which-does-not-fit-in-the-header/authored.yaml"
	w.source.Identity.SHA256 = strings.Repeat("abcdef12", 8)
	at := w.source.Identity.LoadedAt.Add(time.Hour)
	entry := desiredReviewFixtureEntry(testWorkspaceAPIName, review.StateMatch, at)
	w.acceptSnapshot(desiredReviewFixtureSnapshot(w, at, entry), nil)
	w.render()
	paint := drawnText(t, w, 80, 24)
	for _, expected := range []string{"authored.yaml", "SHA256 abcdef12abcd", "reviewed matches", "Loaded 2026-10-04T01:00:00Z", "Read 2026-10-04T02:00:00Z"} {
		if !strings.Contains(paint, expected) {
			t.Fatalf("header clipped %q:\n%s", expected, paint)
		}
	}
	w.detailOpen = true
	w.detailKey = desiredReviewEntryKey(&entry)
	w.evidenceOpen = true
	w.render()
	if detail := w.detail.GetText(true); !strings.Contains(detail, w.source.Identity.Path) || !strings.Contains(detail, w.source.Identity.SHA256) {
		t.Fatal("full retained source identity unavailable in detail")
	}
}
func TestDesiredReviewSourceFormPaintAndEscapeAt80Columns(t *testing.T) {
	w := desiredReviewFixture()
	w.app.Content = NewPageStack()
	w.sourceForm()
	_, modal := w.app.Content.Pages.GetFrontPage()
	paint := drawnText(t, modal, 80, 24)
	for _, expected := range []string{"Local manifest file", "Cancel", "Load source and review"} {
		if !strings.Contains(paint, expected) {
			t.Fatalf("source form clipped %q:\n%s", expected, paint)
		}
	}
	modal.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(tview.Primitive) {})
	if w.formOpen || w.app.Content.Pages.HasPage(desiredReviewSourcePage) {
		t.Fatal("Escape did not dismiss source form")
	}
}

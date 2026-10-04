// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/provider"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
)

func desiredProviderViewFixture(t *testing.T) (view *desiredReviewView, updates chan func()) {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	w := newDesiredReviewView(app, review.Scope{Context: app.Config.ActiveContextName(), Namespaces: []string{desiredReviewFixtureNamespace}, DefaultNamespace: desiredReviewFixtureNamespace}, desiredReviewFixturePath)
	w.source = review.Source{Identity: review.SourceIdentity{Path: desiredReviewFixturePath, SHA256: desiredReviewFixtureHash, LoadedAt: time.Now(), Documents: 1}}
	w.reader = fake.NewSimpleDynamicClient(runtime.NewScheme())
	entry := desiredReviewFixtureEntry("checkout", review.StateChanged, time.Now())
	entry.Identity.Context = w.contextName
	w.acceptSnapshot(desiredReviewFixtureSnapshot(w, entry.ObservedAt, entry), nil)
	app.Content.Push(w)
	app.SetRunning(true)
	w.active = true
	queued := make(chan func(), 8)
	w.enqueue = func(update func()) { queued <- update }
	w.render()
	t.Cleanup(func() { w.Stop(); app.SetRunning(false) })
	return w, queued
}
func nextDesiredUpdate(t *testing.T, queued chan func()) {
	t.Helper()
	select {
	case update := <-queued:
		update()
	case <-time.After(time.Second):
		t.Fatal("bounded worker did not submit result")
	}
}
func TestDesiredServerPreviewRequiresExplicitConfirmationAndKeepsLocalReport(t *testing.T) {
	w, queued := desiredProviderViewFixture(t)
	calls := 0
	w.preview = func(context.Context, dynamic.Interface, review.Resolver, review.Source, review.Scope, time.Time) review.ServerPreview {
		calls++
		return review.ServerPreview{Source: w.source.Identity, Scope: w.scope, ObservedAt: time.Now(), Entries: []review.ServerPreviewEntry{{State: review.StateDenied, Reason: "create/patch denied"}}}
	}
	w.render()
	w.SetFilter("checkout", false)
	for _, width := range []int{120, 80, 60} {
		_ = drawnText(t, w, width, 20)
	}
	require.Zero(t, calls)
	w.key(tcell.NewEventKey(tcell.KeyRune, 'p', tcell.ModNone))
	require.True(t, w.formOpen)
	require.Zero(t, calls)
	_, modal := w.app.Content.Pages.GetFrontPage()
	frame := drawnText(t, modal, 80, 24)
	require.Contains(t, frame, "dryRun=All")
	require.Contains(t, frame, "create/patch")
	require.Contains(t, frame, "Cancel")
	modal.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(tview.Primitive) {})
	require.False(t, w.formOpen)
	require.Zero(t, calls)
	prior := w.snapshot
	w.runServerPreview()
	nextDesiredUpdate(t, queued)
	require.Equal(t, 1, calls)
	require.True(t, w.previewOpen)
	require.Equal(t, prior, w.snapshot)
	require.Equal(t, "checkout", w.SelectedResource().Name)
	require.Contains(t, drawnText(t, w, 60, 20), "NOT PERSISTED")
	require.Contains(t, w.detail.GetText(true), "create/patch denied")
	w.key(tcell.NewEventKey(tcell.KeyRune, 'e', tcell.ModNone))
	require.Contains(t, w.detail.GetText(true), desiredReviewFixtureHash)
	w.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	require.False(t, w.previewOpen)
	require.Equal(t, "checkout", w.SelectedResource().Name)
	require.Equal(t, "checkout", w.query)
}
func TestDesiredServerPreviewLateResultCannotFollowChangedDestination(t *testing.T) {
	w, queued := desiredProviderViewFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	w.preview = func(context.Context, dynamic.Interface, review.Resolver, review.Source, review.Scope, time.Time) review.ServerPreview {
		close(entered)
		<-release
		return review.ServerPreview{ObservedAt: time.Now()}
	}
	w.runServerPreview()
	<-entered
	require.NoError(t, w.app.Config.SetActiveNamespace("another-namespace"))
	close(release)
	nextDesiredUpdate(t, queued)
	require.Nil(t, w.serverPreview)
	require.False(t, w.previewOpen)
	require.Equal(t, desiredReviewFixtureHash, w.source.Identity.SHA256)
}
func TestDesiredProfileFailureRetainsExactSourceAndInvokesOnlyChosenProvider(t *testing.T) {
	w, queued := desiredProviderViewFixture(t)
	original := w.source
	calls := 0
	w.loadProfile = func(_ context.Context, path string, scope provider.Scope, _ review.ProviderRun) (review.Source, error) {
		calls++
		require.Equal(t, "/tmp/profile.yaml", path)
		require.Equal(t, w.contextName, scope.Context)
		require.Equal(t, w.revision, scope.Revision)
		return review.Source{}, fmt.Errorf("renderer unavailable")
	}
	w.loadSource("@/tmp/profile.yaml")
	nextDesiredUpdate(t, queued)
	require.Equal(t, 1, calls)
	require.Equal(t, original, w.source)
	require.Contains(t, w.notice, "prior source and evidence retained")
	require.NotEmpty(t, w.snapshot.Entries)
	w.detailOpen = true
	w.detailKey = desiredReviewEntryKey(&w.snapshot.Entries[0])
	w.evidenceOpen = true
	w.render()
	require.Contains(t, w.detail.GetText(true), "renderer unavailable")
}

func TestDesiredRefreshUsesRetainedRenderedSourceWithoutInvokingProviderAgain(t *testing.T) {
	w, queued := desiredProviderViewFixture(t)
	calls := 0
	configured := w.source
	configured.Identity.Provider = "kustomize"
	configured.Identity.Name = "release"
	configured.Identity.Path = "/tmp/source-profile.yaml"
	configured.Identity.InputSHA256 = "render-input-hash"
	configured.Identity.RendererVersion = "fixture-version"
	w.loadProfile = func(context.Context, string, provider.Scope, review.ProviderRun) (review.Source, error) {
		calls++
		return configured, nil
	}
	w.collect = func(_ context.Context, _ dynamic.Interface, _ review.Resolver, source review.Source, scope review.Scope, at time.Time) review.Snapshot {
		entry := desiredReviewFixtureEntry("checkout", review.StateChanged, at)
		entry.Identity.Context = scope.Context
		return review.Snapshot{Source: source.Identity, Scope: scope, ObservedAt: at, Entries: []review.Entry{entry}}
	}
	w.loadSource("@/tmp/source-profile.yaml")
	nextDesiredUpdate(t, queued) // accepted source schedules one named live collection
	nextDesiredUpdate(t, queued)
	require.Equal(t, 1, calls)
	require.Equal(t, "@/tmp/source-profile.yaml", w.path)
	prior := w.source.Identity
	w.refresh()
	nextDesiredUpdate(t, queued)
	require.Equal(t, 1, calls)
	require.Equal(t, prior, w.source.Identity)
	w.render()
	w.SetFilter("checkout", false)
	require.Equal(t, 1, calls)
	require.Contains(t, w.renderSourceIdentity(), "render-input-hash")
}

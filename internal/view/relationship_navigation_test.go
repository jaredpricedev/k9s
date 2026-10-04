// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/watch"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
)

const navigationContextChange = "context"

func retainedNavigationFixture(t *testing.T) (*App, *inspectionDetails) {
	t.Helper()
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	app.factory = watch.NewFactory(inspectionConnection{dynamic: fake.NewSimpleDynamicClient(runtime.NewScheme())})
	require.NoError(t, app.switchNS("source"))
	ctx := context.WithValue(t.Context(), internal.KeyApp, app)
	require.NoError(t, app.Content.Init(ctx))
	d := &inspectionDetails{Details: NewDetails(app, troubleshootCommand, "source/app", contentInspection, true)}
	require.NoError(t, d.Init(ctx))
	app.Content.Push(d)
	d.acceptSnapshot(inspectionSnapshot{Text: strings.Repeat("CrashLoop evidence\n", 30), UID: "source-uid", CapturedAt: time.Now()}, nil)
	d.cmdBuff.SetText("CrashLoop", "", true)
	d.BufferCompleted("CrashLoop", "")
	d.currentRegion = 3
	d.text.Highlight("search_3")
	d.text.ScrollTo(4, 2)
	t.Cleanup(d.Stop)
	return app, d
}

func assertRetainedNavigation(t *testing.T, d *inspectionDetails, snapshot inspectionSnapshot) {
	t.Helper()
	require.Equal(t, snapshot, d.snapshot)
	require.Equal(t, snapshot.UID, d.target.UID)
	require.Equal(t, "CrashLoop", d.cmdBuff.GetText())
	require.Equal(t, "CrashLoop", d.inspectionQuery)
	require.Equal(t, 3, d.currentRegion)
	require.Equal(t, []string{"search_3"}, d.text.GetHighlights())
	row, col := d.text.GetScrollOffset()
	require.Equal(t, 4, row)
	require.Equal(t, 2, col)
	require.Contains(t, d.text.GetText(false), "search_3")
}

func TestRelatedCrossNamespaceBackRestoresDestinationAndRetainedNavigation(t *testing.T) {
	app, d := retainedNavigationFixture(t)
	snapshot := d.snapshot
	require.NoError(t, app.switchNS("destination"))
	d.rememberRelatedDestination("source", nil)
	child := NewDetails(app, "Pod", "destination/child", contentInspection, true)
	app.Content.Push(child)
	require.Contains(t, app.statusIndicator().FullDestination(), "Namespace: destination")
	// Changing skins while the source is hidden must catch up on Back.
	require.NoError(t, app.Styles.Load("../../skins/monochrome.yaml", false))
	app.PrevCmd(nil)
	require.Same(t, d, app.Content.Top())
	require.Equal(t, "source", app.Config.ActiveNamespace())
	require.Contains(t, app.statusIndicator().FullDestination(), "Namespace: source")
	assertRetainedNavigation(t, d, snapshot)
	// The inspector, rather than its embedded Details, owns live style events.
	require.NoError(t, app.Styles.Load("../../skins/dracula.yaml", false))
	assertRetainedNavigation(t, d, snapshot)
}

func TestRelatedBackPreservesLaterNamespaceOrContextChoice(t *testing.T) {
	for _, changed := range []string{"namespace", navigationContextChange, "namespace-round-trip", "context-round-trip"} {
		t.Run(changed, func(t *testing.T) {
			app, d := retainedNavigationFixture(t)
			require.NoError(t, app.switchNS("destination"))
			d.rememberRelatedDestination("source", nil)
			app.Content.Push(NewDetails(app, "Pod", "destination/child", contentInspection, true))
			if strings.HasPrefix(changed, navigationContextChange) {
				_, err := app.Config.ActivateContext("ct-1-2")
				require.NoError(t, err)
			}
			require.NoError(t, app.switchNS("user-choice"))
			if changed == "context-round-trip" {
				_, err := app.Config.ActivateContext(d.contextName)
				require.NoError(t, err)
			}
			if strings.HasSuffix(changed, "round-trip") {
				require.NoError(t, app.switchNS("destination"))
			}
			namespace := app.Config.ActiveNamespace()
			app.PrevCmd(nil)
			require.Equal(t, namespace, app.Config.ActiveNamespace())
			require.Nil(t, d.returnDestination, "a stale return ticket must be consumed")
			// A later jump returns to the scope chosen just before that jump.
			require.NoError(t, app.switchNS("next-destination"))
			d.rememberRelatedDestination(namespace, nil)
			app.Content.Push(NewDetails(app, "Pod", "next-destination/child", contentInspection, true))
			app.PrevCmd(nil)
			if changed != navigationContextChange {
				require.Equal(t, namespace, app.Config.ActiveNamespace())
			}
		})
	}
}

func TestRelatedLateJumpCannotLeaveClosedPickerOrExpiredDestination(t *testing.T) {
	for _, stale := range []string{"closed", "expired", navigationContextChange, "namespace", "namespace-round-trip", "context-round-trip"} {
		t.Run(stale, func(t *testing.T) {
			app, d := retainedNavigationFixture(t)
			p := &relatedPicker{Picker: NewPicker(), generation: 4, namespace: app.Config.ActiveNamespace(), revision: app.Config.DestinationRevision()}
			app.Content.Push(p)
			ctx := t.Context()
			target := SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.PodGVR,
				Namespace: "destination", Name: "child", UID: "child-uid"}
			switch stale {
			case "closed":
				app.PrevCmd(nil)
			case "expired":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				defer cancel()
			case navigationContextChange:
				_, err := app.Config.ActivateContext("ct-1-2")
				require.NoError(t, err)
			case "namespace", "namespace-round-trip":
				require.NoError(t, app.switchNS("user-choice"))
				if stale == "namespace-round-trip" {
					require.NoError(t, app.switchNS(p.namespace))
				}
			case "context-round-trip":
				_, err := app.Config.ActivateContext("ct-1-2")
				require.NoError(t, err)
				_, err = app.Config.ActivateContext(target.Context)
				require.NoError(t, err)
			}
			before := app.Content.Top()
			namespace := app.Config.ActiveNamespace()
			d.applyRelatedJump(ctx, p, 4, target, nil, "v1/pods destination", "destination/child")
			require.Same(t, before, app.Content.Top())
			require.Equal(t, namespace, app.Config.ActiveNamespace())
			require.Nil(t, d.returnDestination)
		})
	}
}

func TestRelatedAnchorContextMismatchDoesNotSubmitCachedReplacement(t *testing.T) {
	old := dao.MetaAccess
	dao.MetaAccess = dao.NewMeta()
	t.Cleanup(func() { dao.MetaAccess = old })
	app, _ := retainedNavigationFixture(t)
	b := NewBrowser(client.PodGVR).(*Browser)
	b.Table.app = app
	b.GetModel().SetNamespace("source")
	expected := SelectedResourceTarget{Context: "prior-context", GVR: client.PodGVR,
		Namespace: "source", Name: "app", UID: "original"}
	b.expectedTarget = &expected
	replacement := relationshipObject(t, `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":"source","name":"app","uid":"replacement"}}`)
	factory := app.factory.FactoryFor("source")
	require.NoError(t, factory.ForResource(client.PodGVR.GVR()).Informer().GetStore().Add(replacement))
	targets, err := captureOperationTargets(b, app.Config.ActiveContextName(), []string{"source/app"})
	require.NoError(t, err)
	require.Len(t, targets, 1)
	require.Empty(t, targets[0].UID)
	require.ErrorContains(t, targets[0].Err(), "Context changed")
	finished := make(chan []operationOutcome, 1)
	published := make(chan operationOutcome, 1)
	startOperationBatch(time.Second, targets, func(context.Context, SelectedResourceTarget) error {
		t.Error("a stale relationship submitted an operation")
		return nil
	}, func(outcome operationOutcome) { published <- outcome }, func(outcomes []operationOutcome) { finished <- outcomes })
	select {
	case outcomes := <-finished:
		require.True(t, outcomes[0].NotSubmitted)
		require.ErrorContains(t, outcomes[0].Err, "Context changed")
		require.True(t, (<-published).NotSubmitted, "the published result must also reject the stale anchor")
	case <-time.After(time.Second):
		t.Fatal("operation cancellation did not complete")
	}
}

func TestRelatedNestedBackRestoresEachOwnedSourceDestination(t *testing.T) {
	app, a := retainedNavigationFixture(t)
	first := a.snapshot
	require.NoError(t, app.switchNS("nested-b"))
	a.rememberRelatedDestination("source", nil)
	app.Content.Push(NewDetails(app, "Pod", "nested-b/child", contentInspection, true))
	b := &inspectionDetails{Details: NewDetails(app, troubleshootCommand, "nested-b/child", contentInspection, true)}
	require.NoError(t, b.Init(context.WithValue(t.Context(), internal.KeyApp, app)))
	app.Content.Push(b)
	b.acceptSnapshot(inspectionSnapshot{Text: "Retained child evidence", UID: "child-uid", CapturedAt: time.Now()}, nil)
	second := b.snapshot
	ownership := b.ownedReturnDestinations("nested-b")
	require.NoError(t, app.switchNS("nested-c"))
	b.rememberRelatedDestination("nested-b", ownership)
	app.Content.Push(NewDetails(app, "Pod", "nested-c/child", contentInspection, true))
	app.PrevCmd(nil)
	require.Same(t, b, app.Content.Top())
	require.Equal(t, "nested-b", app.Config.ActiveNamespace())
	require.Equal(t, second, b.snapshot)
	app.PrevCmd(nil)
	app.PrevCmd(nil)
	require.Same(t, a, app.Content.Top())
	require.Equal(t, "source", app.Config.ActiveNamespace())
	require.Contains(t, app.statusIndicator().FullDestination(), "Namespace: source")
	assertRetainedNavigation(t, a, first)
}

func TestRelatedDiskReloadRoundTripInvalidatesDestinationOwnership(t *testing.T) {
	app, d := retainedNavigationFixture(t)
	require.NoError(t, app.switchNS("destination"))
	d.rememberRelatedDestination("source", nil)
	app.Content.Push(NewDetails(app, "Pod", "destination/child", contentInspection, true))
	oldRevision := app.Config.DestinationRevision()
	for _, namespace := range []string{"disk-choice", "destination"} {
		reloadDiskNamespace(t, app, namespace)
	}
	require.Greater(t, app.Config.DestinationRevision(), oldRevision)
	app.PrevCmd(nil)
	require.Equal(t, "destination", app.Config.ActiveNamespace(), "a reactive disk edit must invalidate the original return ticket")
}

func reloadDiskNamespace(t *testing.T, app *App, namespace string) {
	t.Helper()
	contextName := app.Config.ActiveContextName()
	clusterName, err := app.Config.ActiveClusterName(contextName)
	require.NoError(t, err)
	path := config.AppContextConfig(clusterName, contextName)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, yaml.Unmarshal(content, &document))
	ctx := document["k9s"].(map[string]any)
	ctx["namespace"].(map[string]any)["active"] = namespace
	content, err = yaml.Marshal(document)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, content, 0o600))
	require.NoError(t, app.Config.Reload())
	require.Equal(t, namespace, app.Config.ActiveNamespace())
}

func TestRelatedNestedReturnNeverRenewsAfterUserDestinationRoundTrip(t *testing.T) {
	for _, stage := range []string{"before-child-jump", "at-child", "after-child-return"} {
		for _, change := range []string{"namespace", navigationContextChange, "reload"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				app, a := retainedNavigationFixture(t)
				first := a.snapshot
				require.NoError(t, app.switchNS("nested-b"))
				a.rememberRelatedDestination("source", nil)
				app.Content.Push(NewDetails(app, "Pod", "nested-b/child", contentInspection, true))
				b := pushNestedInspection(t, app, "nested-b/child", "child-uid")
				if stage == "before-child-jump" {
					navigationDestinationRoundTrip(t, app, change, "nested-b")
				}
				ownership := b.ownedReturnDestinations("nested-b")
				require.NoError(t, app.switchNS("nested-c"))
				b.rememberRelatedDestination("nested-b", ownership)
				app.Content.Push(NewDetails(app, "Pod", "nested-c/child", contentInspection, true))
				if stage == "at-child" {
					navigationDestinationRoundTrip(t, app, change, "nested-c")
				}
				app.PrevCmd(nil)
				if stage == "after-child-return" {
					require.Equal(t, "nested-b", app.Config.ActiveNamespace())
					navigationDestinationRoundTrip(t, app, change, "nested-b")
				}
				namespace := app.Config.ActiveNamespace()
				app.PrevCmd(nil)
				app.PrevCmd(nil)
				require.Same(t, a, app.Content.Top())
				require.Equal(t, namespace, app.Config.ActiveNamespace())
				require.NotEqual(t, "source", namespace, "a nested unwind revived the stale ancestor ticket")
				assertRetainedNavigation(t, a, first)
			})
		}
	}
}

func TestRelatedThreeNestedReturnsRestoreOnlyTheirOwnedChain(t *testing.T) {
	app, a := retainedNavigationFixture(t)
	first := a.snapshot
	require.NoError(t, app.switchNS("nested-b"))
	a.rememberRelatedDestination("source", nil)
	app.Content.Push(NewDetails(app, "Pod", "nested-b/child", contentInspection, true))
	b := pushNestedInspection(t, app, "nested-b/child", "child-b")
	bOwnership := b.ownedReturnDestinations("nested-b")
	require.NoError(t, app.switchNS("nested-c"))
	b.rememberRelatedDestination("nested-b", bOwnership)
	app.Content.Push(NewDetails(app, "Pod", "nested-c/child", contentInspection, true))
	c := pushNestedInspection(t, app, "nested-c/child", "child-c")
	cOwnership := c.ownedReturnDestinations("nested-c")
	require.NoError(t, app.switchNS("nested-d"))
	c.rememberRelatedDestination("nested-c", cOwnership)
	app.Content.Push(NewDetails(app, "Pod", "nested-d/child", contentInspection, true))
	for _, expected := range []string{"nested-c", "nested-c", "nested-b", "nested-b", "source"} {
		app.PrevCmd(nil)
		require.Equal(t, expected, app.Config.ActiveNamespace())
	}
	require.Same(t, a, app.Content.Top())
	assertRetainedNavigation(t, a, first)
}

func TestRelatedDiskReloadAlsoRejectsPendingPickerRoundTrip(t *testing.T) {
	app, d := retainedNavigationFixture(t)
	p := &relatedPicker{Picker: NewPicker(), generation: 4, namespace: app.Config.ActiveNamespace(), revision: app.Config.DestinationRevision()}
	app.Content.Push(p)
	for _, namespace := range []string{"disk-choice", "source"} {
		reloadDiskNamespace(t, app, namespace)
	}
	target := SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.PodGVR,
		Namespace: "destination", Name: "child", UID: "child-uid"}
	d.applyRelatedJump(t.Context(), p, 4, target, nil, "v1/pods destination", "destination/child")
	require.Same(t, p, app.Content.Top())
	require.Equal(t, "source", app.Config.ActiveNamespace())
	require.Nil(t, d.returnDestination)
}

func pushNestedInspection(t *testing.T, app *App, path, uid string) *inspectionDetails {
	t.Helper()
	d := &inspectionDetails{Details: NewDetails(app, troubleshootCommand, path, contentInspection, true)}
	require.NoError(t, d.Init(context.WithValue(t.Context(), internal.KeyApp, app)))
	app.Content.Push(d)
	d.acceptSnapshot(inspectionSnapshot{Text: "Retained child evidence", UID: types.UID(uid), CapturedAt: time.Now()}, nil)
	return d
}

func navigationDestinationRoundTrip(t *testing.T, app *App, change, returnNamespace string) {
	t.Helper()
	switch change {
	case "namespace":
		require.NoError(t, app.switchNS("manual-choice"))
		require.NoError(t, app.switchNS(returnNamespace))
	case navigationContextChange:
		contextName := app.Config.ActiveContextName()
		_, err := app.Config.ActivateContext("ct-1-2")
		require.NoError(t, err)
		_, err = app.Config.ActivateContext(contextName)
		require.NoError(t, err)
		require.NoError(t, app.switchNS(returnNamespace))
	case "reload":
		reloadDiskNamespace(t, app, "disk-choice")
		reloadDiskNamespace(t, app, returnNamespace)
	}
}

func TestRelatedUnchangedOrFailedReloadKeepsCurrentReturnOwnership(t *testing.T) {
	app, d := retainedNavigationFixture(t)
	require.NoError(t, app.switchNS("destination"))
	d.rememberRelatedDestination("source", nil)
	app.Content.Push(NewDetails(app, "Pod", "destination/child", contentInspection, true))
	revision := app.Config.DestinationRevision()
	reloadDiskNamespace(t, app, "destination")
	require.Equal(t, revision, app.Config.DestinationRevision())
	contextName := app.Config.ActiveContextName()
	clusterName, err := app.Config.ActiveClusterName(contextName)
	require.NoError(t, err)
	path := config.AppContextConfig(clusterName, contextName)
	require.NoError(t, os.WriteFile(path, []byte("k9s: [broken: ["), 0o600))
	require.Error(t, app.Config.Reload())
	require.Equal(t, revision, app.Config.DestinationRevision())
	require.Equal(t, "destination", app.Config.ActiveNamespace())
	app.PrevCmd(nil)
	require.Equal(t, "source", app.Config.ActiveNamespace())
}

// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDesiredDetailPutsBothChangesAboveProvenanceAndRetainsEvidence(t *testing.T) {
	for _, width := range []int{120, 80, 60} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			w := desiredReviewFixture()
			entry := desiredReviewFixtureEntry("checkout", review.StateChanged, time.Date(2026, 10, 4, 2, 0, 0, 0, time.UTC))
			entry.Intent.Changes = append(entry.Intent.Changes, review.Change{Path: "/spec/template/spec/containers/0/resources/limits/memory", Kind: "changed", Before: "128Mi", After: "256Mi"})
			entry.Intent.DeclaredFields = 4
			w.acceptSnapshot(desiredReviewFixtureSnapshot(w, entry.ObservedAt, entry), nil)
			w.render()
			w.key(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
			paint := drawnText(t, w, width, 20)
			for _, value := range []string{"example/api:v1", "example/api:v2", "128Mi", "256Mi", "READ ONLY", "e evidence"} {
				require.Contains(t, paint, value)
			}
			require.NotContains(t, paint, "RETAINED SOURCE")
			w.key(tcell.NewEventKey(tcell.KeyRune, 'e', tcell.ModNone))
			evidence := w.detail.GetText(true)
			for _, value := range []string{w.source.Identity.Path, w.source.Identity.SHA256, identityTime(w.source.Identity.LoadedAt), entry.Intent.Changes[1].Path, "CAPTURED SCOPE", "admission"} {
				require.Contains(t, evidence, value)
			}
			w.key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
			require.False(t, w.detailOpen)
			require.Equal(t, "checkout", w.SelectedResource().Name)
			require.Equal(t, desiredReviewFixtureHash, w.source.Identity.SHA256)
		})
	}
}

func TestDesiredResponsiveFloorPreservesLocalSearchAndRetainedDetail(t *testing.T) {
	w := desiredReviewFixture()
	entry := desiredReviewFixtureEntry("長い-resource-e\u0301", review.StateChanged, time.Now())
	w.acceptSnapshot(desiredReviewFixtureSnapshot(w, entry.ObservedAt, entry), nil)
	w.SetFilter("resource", false)
	for _, size := range []struct{ width, height int }{{120, 34}, {80, 24}, {60, 24}, {40, 12}} {
		paint := drawnText(t, w, size.width, size.height)
		require.Contains(t, paint, "Esc back")
		require.Contains(t, paint, "changed")
		require.NotContains(t, paint, "View too small")
		require.Equal(t, "resource", w.query)
		require.Equal(t, entry.Identity.Name, w.SelectedResource().Name)
	}
	paint := drawnText(t, w, 39, 12)
	require.Contains(t, paint, "40x12")
	require.Equal(t, "resource", w.query)
	require.Equal(t, entry.Identity.Name, w.SelectedResource().Name)
	require.NotContains(t, drawnText(t, w, 60, 24), "View too small")
}

func rolloutManyRevisions(t *testing.T) *rolloutReviewView {
	t.Helper()
	v := rolloutViewFixture(t)
	var objects []*unstructured.Unstructured
	for index := range 25 {
		objects = append(objects, rolloutTestReplicaSet(fmt.Sprintf("revision-%02d", index), fmt.Sprintf("uid-%02d", index), rolloutTestUID, rolloutTestImage, true))
	}
	v.acceptSnapshot(review.NewRolloutSnapshot(rolloutTestDeployment(), objects, nil, []review.RolloutCoverage{{Source: "Pods", State: inspect.ObservationDenied}}, v.target.Context, time.Now()), nil)
	// Prior selection was removed by this new retained set: require a new choice.
	v.selectTab(1)
	v.moveRevision(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone), 1)
	return v
}

func assertSelectedRevisionVisible(t *testing.T, v *rolloutReviewView, width, height int) {
	t.Helper()
	paint := drawnText(t, v, width, height)
	name := v.snapshot.Revisions[v.revisionIndex()].Identity.Name
	visible := false
	for _, line := range strings.Split(paint, "\n") {
		if strings.HasPrefix(strings.TrimSpace(strings.Trim(line, " │")), "> ") && strings.Contains(line, name) {
			visible = true
		}
	}
	require.True(t, visible, "chosen row %s absent at %dx%d:\n%s", name, width, height, paint)
	require.Contains(t, paint, "Enter review")
}

func TestRolloutRevisionNavigationKeepsChosenRowVisibleAtEverySize(t *testing.T) {
	v := rolloutManyRevisions(t)
	for _, size := range []struct{ width, height int }{{120, 34}, {80, 24}, {60, 24}, {40, 12}} {
		for _, step := range []int{18, 1, 8, -1, -18, -10} {
			v.moveRevision(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone), step)
			assertSelectedRevisionVisible(t, v, size.width, size.height)
		}
		for _, key := range []tcell.Key{tcell.KeyPgDn, tcell.KeyPgUp, tcell.KeyDown, tcell.KeyUp} {
			action, ok := v.actions.Get(key)
			require.True(t, ok)
			action.Action(tcell.NewEventKey(key, 0, tcell.ModNone))
			assertSelectedRevisionVisible(t, v, size.width, size.height)
		}
	}
	chosen := v.selectedRevisionUID
	v.reviewSelectedRevision(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	require.Equal(t, chosen, v.recoveryRevisionUID)
	require.Contains(t, v.text.GetText(true), chosen)
	require.Contains(t, v.text.GetText(true), "NOT EXECUTED")
	require.Contains(t, drawnText(t, v, 39, 12), "40x12")
	require.Equal(t, chosen, v.selectedRevisionUID)
	v.selectTab(1)
	assertSelectedRevisionVisible(t, v, 60, 24)
}

func TestRolloutRemovedSelectionCannotSilentlyPreviewAnotherUID(t *testing.T) {
	v := rolloutViewFixture(t)
	v.selectTab(1)
	v.moveRevision(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone), 1)
	require.Equal(t, rolloutTestHistorical, v.selectedRevisionUID)
	current := rolloutTestReplicaSet("a-current", rolloutTestRSUID, rolloutTestUID, rolloutTestImage, true)
	v.acceptSnapshot(review.NewRolloutSnapshot(rolloutTestDeployment(), []*unstructured.Unstructured{current}, nil, nil, v.target.Context, time.Now()), nil)
	require.Equal(t, -1, v.revisionIndex())
	require.True(t, v.selectionInvalidated)
	v.reviewSelectedRevision(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	require.Equal(t, 1, v.activeTab)
	require.Empty(t, v.recoveryRevisionUID)
	require.Contains(t, drawnText(t, v, 60, 24), "Selection removed")
	v.moveRevision(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone), 1)
	require.False(t, v.selectionInvalidated)
	require.Equal(t, rolloutTestRSUID, v.selectedRevisionUID)
	v.reviewSelectedRevision(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	require.Equal(t, rolloutTestRSUID, v.recoveryRevisionUID)
}

func TestRolloutSearchTabsAndRefreshPreserveExactSelectionWithoutReading(t *testing.T) {
	v := rolloutManyRevisions(t)
	v.moveRevision(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone), 18)
	chosen := v.selectedRevisionUID
	v.BufferCompleted("revision-01", "")
	v.selectTab(rolloutEvidenceTab)
	v.selectTab(1)
	require.Equal(t, chosen, v.selectedRevisionUID)
	require.Equal(t, "revision-01", v.inspectionQuery)
	assertSelectedRevisionVisible(t, v, 60, 24)
	v.moveRevision(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone), 1)
	chosen = v.selectedRevisionUID
	assertSelectedRevisionVisible(t, v, 60, 24)
	replacement := *v.snapshot
	replacement.Revisions = append([]review.RolloutRevision(nil), v.snapshot.Revisions...)
	replacement.Revisions[0], replacement.Revisions[19] = replacement.Revisions[19], replacement.Revisions[0]
	v.acceptSnapshot(&replacement, nil)
	require.Equal(t, chosen, v.selectedRevisionUID)
	assertSelectedRevisionVisible(t, v, 60, 24)
	require.False(t, v.selectionInvalidated)
	require.Contains(t, v.footer.GetText(true), "READ ONLY")
	// Template matching is not selection: all 25 fixtures match the current spec.
	require.Equal(t, chosen, v.snapshot.Revisions[v.revisionIndex()].Identity.UID)
	require.NotEqual(t, v.snapshot.Revisions[1].Identity.UID, chosen)
	action, ok := v.actions.Get(ui.KeyJ)
	require.True(t, ok)
	v.cmdBuff.SetActive(true)
	event := tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone)
	require.Same(t, event, action.Action(event))
	require.Equal(t, chosen, v.selectedRevisionUID)
}

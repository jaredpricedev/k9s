// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/ui"
)

const (
	relatedNativeReadReason = "Use Inspector for this related object, or reopen the resource list for live YAML and Describe"
	relatedNativeEditReason = "Reopen the resource list and select this object again before editing"
)

// Native views poll by name and kubectl edit has no UID precondition. Retain the
// relationship's identity by requiring a fresh list before those actions, even
// when the cache still contains the observed UID. This is a cache-only UI guard.
func nativeRelationshipReason(source *Table, gvr *client.GVR, path string, edit bool) string {
	matched, reason := relatedSelectionReason(source, gvr, path)
	if !matched || reason != "" {
		return reason
	}
	if edit {
		return relatedNativeEditReason
	}
	return relatedNativeReadReason
}

func relatedSelectionReason(source *Table, gvr *client.GVR, path string) (matched bool, reason string) {
	if source == nil || source.app == nil || source.expectedTarget == nil || gvr == nil || source.GVR() != gvr {
		return false, ""
	}
	expected := source.expectedTarget
	target := resourceTargetForPath(gvr, source.app.Config.ActiveContextName(), path)
	if expected.GVR != target.GVR || expected.Path() != target.Path() {
		return false, ""
	}
	if err := tableResourceForPath(source, target.Context, path).Err(); err != nil {
		return true, err.Error()
	}
	return true, ""
}

func (b *Browser) nativeRelationshipAction(action ui.KeyAction, edit bool) ui.KeyAction {
	action.Availability = func() string {
		return nativeRelationshipReason(b.GetTable(), b.GVR(), b.GetSelectedItem(), edit)
	}
	return action
}

func (b *Browser) nativeEnterReason() string {
	if _, reason := relatedSelectionReason(b.GetTable(), b.GVR(), b.GetSelectedItem()); reason != "" {
		return reason
	}
	if b.enterFn != nil {
		return ""
	}
	if _, custom := b.App().CustomJumps().GetRule(b.GVR()); custom {
		return ""
	}
	return nativeRelationshipReason(b.GetTable(), b.GVR(), b.GetSelectedItem(), false)
}

func (v *LiveView) nativeRelationshipReason(edit bool) string {
	if v.model == nil {
		return ""
	}
	return nativeRelationshipReason(v.nativeSource, v.model.GVR(), v.model.GetPath(), edit)
}

// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/k9s/internal/ui/dialog"
)

func (w *desiredReviewView) confirmServerPreview() {
	if w.source.Identity.SHA256 == "" {
		w.notice = "Choose a source before requesting server preview"
		w.render()
		return
	}
	if !w.current() {
		w.notice = "Destination changed; reopen review before server preview"
		w.render()
		return
	}
	if w.reader == nil {
		w.notice = "Server preview reader unavailable; local comparison retained"
		w.render()
		return
	}
	w.formOpen = true
	styles := w.app.Styles.Dialog()
	message := fmt.Sprintf("SERVER DRY RUN · context %s · namespaces %s\nSource %s · SHA256 %s\n"+
		"Invokes API admission/defaulting with dryRun=All. Requires create/patch permission. No persisted write or prune request. "+
		"Secrets and cluster-scoped objects stay excluded.", w.contextName, strings.Join(w.scope.Namespaces, ", "),
		w.source.Identity.Path, w.source.Identity.SHA256[:min(12, len(w.source.Identity.SHA256))])
	dialog.ShowConfirm(&styles, w.app.Content.Pages, "Server admission preview", desiredReviewSafe(message), w.runServerPreview,
		func() { w.formOpen = false; w.focusContent() })
	_, modal := w.app.Content.Pages.GetFrontPage()
	w.app.SetFocus(modal)
}
func (w *desiredReviewView) runServerPreview() {
	if !w.current() || w.reader == nil || w.preview == nil {
		w.notice = "Server preview unavailable or destination changed"
		w.render()
		return
	}
	ctx := w.beginRead()
	generation := w.generation
	cancel := w.cancel
	source, scope, reader, resolve, preview := w.source, copyDesiredReviewScope(w.scope), w.reader, w.resolve, w.preview
	detailKey := w.detailKey
	if entry, ok := w.selectedEntry(); ok {
		detailKey = desiredReviewEntryKey(&entry)
	}
	w.notice = "Explicit server dry-run in progress; local comparison retained"
	w.render()
	go func() {
		defer cancel()
		result := preview(ctx, reader, resolve, source, scope, time.Now().UTC())
		evidence := renderServerPreview(&result, true, 0)
		w.submit(ctx, func() {
			if generation != w.generation || !w.current() || source.Identity.SHA256 != w.source.Identity.SHA256 {
				return
			}
			w.cancel = nil
			w.serverPreview = &result
			w.detailKey = detailKey
			w.serverPreviewEvidence = evidence
			w.previewOpen = true
			w.detailOpen = true
			w.evidenceOpen = false
			w.notice = "Server preview retained; no resource persisted. r only refreshes local comparison"
			w.pages.SwitchToPage(desiredReviewDetailPage)
			w.detail.ScrollToBeginning()
			w.render()
			w.focusContent()
		})
	}()
}
func renderSourceProvenance(source *review.SourceIdentity) string {
	if source.Provider == "" {
		return ""
	}
	return desiredReviewSafe(fmt.Sprintf("\nProvider: %s · named source: %s\nProfile SHA256: %s\nInput path: %s\n"+
		"Input SHA256 / revision: %s\nGit ref: %s\nGit commit: %s\nRenderer: %s · version: %s\nExact argv: %q\n"+
		"Pruning scope: unavailable; no authoritative inventory supplied", source.Provider, source.Name, source.ProfileSHA256,
		source.InputPath, source.InputSHA256, source.RequestedRevision, source.Revision, source.Renderer, source.RendererVersion, source.Options))
}
func renderServerPreview(result *review.ServerPreview, evidence bool, width int) string {
	if evidence {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return desiredReviewSafe("Server preview evidence unavailable")
		}
		text := "SERVER DRY RUN EVIDENCE · NOT PERSISTED\n" + string(data)
		if len(text) > inspect.MaxComparisonText {
			text = text[:inspect.MaxComparisonText] + "\nEvidence display capped at 256KiB; JSON projection incomplete."
		}
		return desiredReviewSafe(text)
	}
	var out strings.Builder
	out.WriteString("SERVER ADMISSION PREVIEW · NOT PERSISTED\n")
	fmt.Fprintf(&out, "Context %s · %s\n", result.Scope.Context, result.ObservedAt.UTC().Format(time.RFC3339))
	out.WriteString("Acceptance applies to this request only; rollout outcome unknown.\n\n")
	for index := range result.Entries {
		entry := &result.Entries[index]
		fmt.Fprintf(&out, "%s %s/%s · %s\n", entry.Identity.Kind, entry.Identity.Namespace, entry.Identity.Name, entry.State)
		fmt.Fprintln(&out, entry.Request+" · "+entry.Reason)
		if entry.State == review.PreviewAccepted {
			fmt.Fprintf(&out, "%d projected field changes · %d authored/admitted differences\n", len(entry.Projection.Changes), len(entry.Admission.Changes))
			for _, change := range entry.Projection.Changes[:min(8, len(entry.Projection.Changes))] {
				path := change.Path
				if width < 90 {
					path = strings.TrimPrefix(path, "/spec/template/spec/")
				}
				before, after := comparisonValues(change.Before, change.After)
				fmt.Fprintf(&out, "%s %s\n  live: %s\n  preview: %s\n", change.Kind, path, before, after)
			}
			if len(entry.Projection.Changes) > 8 {
				out.WriteString("Additional projected fields retained in Evidence/source.\n")
			}
			if entry.Projection.Truncated || entry.Admission.Truncated {
				out.WriteString("Incomplete projection; inspect Evidence/source.\n")
			}
		}
		out.WriteByte('\n')
	}
	out.WriteString("Projection omits server bookkeeping and controller status.\n" +
		"No authoritative resource deletion/pruning inventory. Secret values excluded.\ne evidence/source · Esc retained resource table")
	return desiredReviewSafe(out.String())
}

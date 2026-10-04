// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/provider"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const historyCommand = "history-review"

type historyView struct {
	chrome *tview.TextView
	*Details
	request         observability.Request
	snapshot        *observability.Snapshot
	loader          func(context.Context, observability.Request) (*observability.Snapshot, error)
	cancel          context.CancelFunc
	generation      uint64
	active, loading bool
	failure         string
}

func (c *Command) historyReviewCommand() {
	owner, ok := c.app.Content.Top().(actionOwner)
	if !ok {
		c.app.Flash().Warn("Select a native Pod for history review")
		return
	}
	target := actionTarget(owner, c.app.Config.ActiveContextName())
	if target.Err() != nil || target.GVR.String() != client.PodGVR.String() || target.UID == "" {
		c.app.Flash().Warn("Select a native Pod with captured UID for history review")
		return
	}
	now := time.Now().UTC().Truncate(time.Second)
	v := &historyView{Details: NewDetails(c.app, "History review", target.Path(), contentInspection, true), loader: observability.Collect,
		request: observability.Request{Scope: provider.Scope{
			Context: target.Context, Namespace: c.app.Config.CachedNamespace(), TargetNamespace: target.Namespace,
			GVR: target.GVR.String(), Name: target.Name, UID: string(target.UID), Revision: c.app.Config.DestinationRevision(),
		}, Start: now.Add(-time.Hour), End: now}}
	if err := c.app.inject(v, false); err != nil {
		c.app.Flash().Err(err)
		return
	}
	v.form()
}
func (*historyView) CompactWorkspace() bool { return true }
func (v *historyView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Choose history source/window", func(e *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return e
		}
		v.form()
		return nil
	}, true))
	v.SetBorder(false).SetBorderPadding(0, 0, 0, 0)
	v.chrome = tview.NewTextView().SetWrap(false)
	v.Flex.Clear().SetDirection(tview.FlexRow).AddItem(v.chrome, 4, 0, false).AddItem(v.text, 0, 1, true)
	v.render()
	return nil
}
func (v *historyView) Start() {
	v.active = true
	v.Details.Start()
	v.app.Prompt().SetModel(v.cmdBuff)
	v.render()
}
func (v *historyView) Stop() {
	v.active = false
	v.generation++
	if v.cancel != nil {
		v.cancel()
	}
	v.Details.Stop()
}
func (v *historyView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Flex.Box) {
		return
	}
	_, _, width, _ := v.GetInnerRect()
	if v.chrome != nil {
		styles := v.app.Styles.Semantic()
		v.chrome.SetBackgroundColor(styles.Panel.Color())
		v.chrome.SetTextColor(styles.Text.Color())
		r := v.request
		source := "Source pending / mapping unverified"
		if v.snapshot != nil {
			source = v.snapshot.ObservedAt.UTC().Format("15:04:05Z") + " " + v.snapshot.Request.URL
		}
		if v.loading {
			source = "Requesting / prior evidence retained"
		}
		if v.failure != "" {
			source = "Retained / " + v.failure
		}
		lines := []string{"History / " + r.Scope.TargetNamespace + "/" + r.Scope.Name, "UID " + r.Scope.UID + " (history unverified)",
			source, "r source/window  / search  Esc back"}
		for i, line := range lines {
			lines[i] = tview.Escape(ui.Truncate(line, width))
		}
		v.chrome.SetText(strings.Join(lines, "\n"))
	}
	v.Flex.Draw(screen)
}
func (v *historyView) current() bool {
	return v.request.Scope.Context == v.app.Config.ActiveContextName() && v.request.Scope.Namespace == v.app.Config.CachedNamespace() &&
		v.request.Scope.Revision == v.app.Config.DestinationRevision()
}
func (v *historyView) form() {
	r := v.request
	address, start, end := r.URL, r.Start.UTC().Format(time.RFC3339), r.End.UTC().Format(time.RFC3339)
	form := tview.NewForm().SetItemPadding(0)
	form.AddInputField("URL", address, 24, nil, func(s string) { address = s })
	form.AddInputField("Start UTC", start, 24, nil, func(s string) { start = s })
	form.AddInputField("End UTC", end, 24, nil, func(s string) { end = s })
	const page = "history-source-form"
	dismiss := func() { v.app.Content.Pages.RemovePage(page); v.app.SetFocus(v) }
	form.AddButton("Cancel", dismiss).AddButton("Request", func() {
		if !v.current() {
			dismiss()
			v.failure = "Destination changed; reopen history review"
			v.render()
			return
		}
		var err error
		r.URL = address
		r.Start, err = time.Parse(time.RFC3339, start)
		if err == nil {
			r.End, err = time.Parse(time.RFC3339, end)
		}
		if err != nil || !strings.HasSuffix(start, "Z") || !strings.HasSuffix(end, "Z") {
			v.app.Flash().Warn("Use UTC RFC3339 times ending in Z")
			return
		}
		if err = observability.Validate(&r); err != nil {
			v.app.Flash().Warn(err.Error())
			return
		}
		v.request = r
		dismiss()
		v.refresh()
	})
	styles := v.app.Styles.Dialog()
	form.SetButtonBackgroundColor(styles.ButtonBgColor.Color()).SetButtonTextColor(styles.ButtonFgColor.Color()).
		SetLabelColor(styles.LabelFgColor.Color()).SetFieldTextColor(styles.FieldFgColor.Color()).SetFieldBackgroundColor(styles.BgColor.Color())
	modal := ui.NewModalForm("Pod history", form)
	modal.SetText("Two scoped CPU/memory queries. Provider cluster mapping and historical Pod UID are unverified. Max 24h. Request contacts this URL.")
	modal.SetTextColor(styles.FgColor.Color()).SetDoneFunc(func(int, string) { dismiss() })
	v.app.Content.Pages.AddPage(page, modal, false, true)
	v.app.SetFocus(modal)
}
func (v *historyView) refresh() {
	if !v.current() {
		v.failure = "Destination changed; reopen history review"
		v.render()
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation, r := v.generation, v.request
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	v.cancel = cancel
	v.loading = true
	v.render()
	go func() {
		defer cancel()
		snapshot, err := v.loader(ctx, r)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if ctx.Err() == context.Canceled || !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if v.active && generation == v.generation && v.current() && v.app.Content.Top() == v {
				v.accept(snapshot, err)
			}
		})
	}()
}
func (v *historyView) accept(s *observability.Snapshot, err error) {
	v.loading = false
	if err != nil {
		v.failure = "Provider unavailable"
		if errors.Is(err, context.DeadlineExceeded) {
			v.failure = "Provider timed out"
		}
	} else if s == nil {
		v.failure = "Provider unavailable"
	} else {
		v.failure = ""
		failed := false
		for _, e := range s.Evidence {
			if e.State == "denied" || e.State == "absent" || e.State == string(capabilityUnavailable) {
				failed = true
				v.failure = "Provider " + e.State
			}
		}
		if !failed || v.snapshot == nil {
			v.snapshot = s
		}
	}
	v.render()
}
func (v *historyView) render() {
	var b strings.Builder
	r := &v.request
	fmt.Fprintf(&b, "History review · READ ONLY\n%s / %s/%s\nGVR: %s · selected UID: %s\n",
		safeProviderText(r.Scope.Context), safeProviderText(r.Scope.TargetNamespace), safeProviderText(r.Scope.Name), r.Scope.GVR, safeProviderText(r.Scope.UID))
	b.WriteString("Provider cluster mapping unverified.\nPod-name history cannot verify historical UID.\n")
	if v.loading {
		b.WriteString("Requesting; prior evidence retained.\n")
	}
	if v.failure != "" {
		b.WriteString(v.failure + "; prior evidence retained.\n")
	}
	if s := v.snapshot; s != nil {
		fmt.Fprintf(&b, "\nSource: %s\nObserved: %s\nWindow: %s to %s\nStep: %s\n", s.Request.URL, s.ObservedAt.UTC().Format(time.RFC3339),
			s.Request.Start.UTC().Format(time.RFC3339), s.Request.End.UTC().Format(time.RFC3339), s.Request.Step)
		for _, e := range s.Evidence {
			fmt.Fprintf(&b, "\n%s: %s · gaps/rejected %d\nWindow mismatch: %t · target mismatch: %t\nQuery: %s\n",
				e.Unit, e.State, e.Gaps, e.WindowMismatch, e.TargetMismatch, e.Query)
			for _, series := range e.Series {
				fmt.Fprintf(&b, "Container %s: %d samples", safeProviderText(series.Container), len(series.Samples))
				if len(series.Samples) > 0 {
					first, last := series.Samples[0], series.Samples[len(series.Samples)-1]
					fmt.Fprintf(&b, " · %s to %s · last %g", first.Timestamp.Format(time.RFC3339), last.Timestamp.Format(time.RFC3339), last.Value)
				}
				b.WriteString("\n")
			}
		}
		b.WriteString("\nCoverage remains partial/unknown; success is not complete history.\n")
	} else {
		b.WriteString("\nNo provider requested. r chooses source/window.\n")
	}
	b.WriteString("\nr source/window · / search · Esc back")
	row, col := v.text.GetScrollOffset()
	v.Update(b.String())
	v.text.ScrollTo(row, col)
}

func historyActions(owner actionOwner, app *App) []ui.ActionDescriptor {
	target := actionTarget(owner, app.Config.ActiveContextName())
	reason := ""
	if target.Err() != nil || target.GVR.String() != client.PodGVR.String() || target.UID == "" {
		reason = "Select a native Pod with captured UID"
	}
	return []ui.ActionDescriptor{{ID: "resource.history-review", Label: "Pod CPU/memory history", Category: ui.ActionInspect,
		Shortcut: ":" + historyCommand, Discoverable: true, RequiresSelection: true, UnavailableReason: reason,
		Handler: func(*tcell.EventKey) *tcell.EventKey { (&Command{app: app}).historyReviewCommand(); return nil }}}
}

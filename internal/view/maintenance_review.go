// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/maintenance"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/ui/dialog"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const maintenanceCommandToken = "maintenance"
const maintenanceTitle = "Maintenance"

type maintenanceView struct {
	*Details
	origin                       ResourceViewer
	target                       SelectedResourceTarget
	session                      *operationSession
	snapshot                     *maintenance.Snapshot
	loader                       func(context.Context, *SelectedResourceTarget) (*maintenance.Snapshot, error)
	task                         *operationTask
	reviewedPods                 map[string]string
	cancel                       context.CancelFunc
	generation, revision         uint64
	active, loading              bool
	activeTab                    int
	tabStates                    [6]investigationTabState
	identityBar, tabsBar, footer *tview.TextView
	width, height                int
	failure, receiptState        string
}

func (c *Command) maintenanceCommand() {
	if existing, ok := c.app.Content.Top().(*maintenanceView); ok {
		existing.selectTab(0)
		return
	}
	owner, ok := c.app.Content.Top().(ResourceViewer)
	if !ok {
		c.app.Flash().Warn("Select a native Node in a resource list for maintenance review")
		return
	}
	c.app.openNodeMaintenance(owner, resolveSelectedResource(owner, c.app.Config.ActiveContextName()))
}

//nolint:gocritic // The review owns the selected identity independently of navigation.
func (a *App) openNodeMaintenance(origin ResourceViewer, target SelectedResourceTarget) {
	if err := maintenanceTargetError(&target); err != nil {
		a.Flash().Warn(err.Error())
		return
	}
	if target.Context != a.Config.ActiveContextName() {
		a.Flash().Warn("Context changed; reopen maintenance review")
		return
	}
	pinned, err := pinInspectionConnection(a.Conn())
	if err != nil {
		a.Flash().Err(err)
		return
	}
	typed, err := pinned.Dial()
	if err != nil {
		a.Flash().Err(err)
		return
	}
	dynamic, err := pinned.DynDial()
	if err != nil {
		a.Flash().Err(err)
		return
	}
	v := &maintenanceView{Details: NewDetails(a, maintenanceTitle, target.Path(), contentInspection, true),
		origin: origin, target: target, revision: a.Config.DestinationRevision()}
	v.session = &operationSession{app: a, context: target.Context, revision: v.revision, timeout: maxOperationDeadline, typed: typed, dynamic: dynamic,
		stillCurrent: func() bool { return v.active && a.Content.Top() == v }}
	v.loader = func(ctx context.Context, selected *SelectedResourceTarget) (*maintenance.Snapshot, error) {
		identity := inspect.ResourceIdentity{Context: selected.Context, GVR: selected.GVR.String(), Name: selected.Name, UID: string(selected.UID)}
		return maintenance.Collect(ctx, typed, &identity, time.Now())
	}
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}

func maintenanceTargetError(target *SelectedResourceTarget) error {
	if err := target.Err(); err != nil {
		return err
	}
	if target.GVR.GVR() != client.NodeGVR.GVR() {
		return fmt.Errorf("Select a native Node to review maintenance")
	}
	if target.UID == "" {
		return fmt.Errorf("Node UID unavailable; refresh the source list and select it again")
	}
	return nil
}

func (v *maintenanceView) SelectedResource() SelectedResourceTarget { return v.target }
func (*maintenanceView) CompactWorkspace() bool                     { return true }

func (v *maintenanceView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.SetBorder(false).SetBorderPadding(0, 0, 0, 0)
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v)
	v.identityBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.tabsBar = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.footer = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.Flex.Clear().SetDirection(tview.FlexRow).AddItem(v.identityBar, 3, 0, false).AddItem(v.tabsBar, 1, 0, false).AddItem(v.text, 0, 1, true).AddItem(v.footer, 1, 0, false)
	v.bindMaintenanceKeys()
	v.render()
	return nil
}

func (v *maintenanceView) bindMaintenanceKeys() {
	for index, key := range []tcell.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6} {
		tab := index
		v.actions.Add(key, ui.NewKeyAction(maintenance.Tabs[index], v.keyAction(func() { v.selectTab(tab) }), true))
	}
	for _, action := range []struct {
		key   tcell.Key
		label string
		run   func()
	}{
		{tcell.KeyTab, "Next maintenance tab", func() { v.selectTab((v.activeTab + 1) % len(maintenance.Tabs)) }},
		{tcell.KeyBacktab, "Previous maintenance tab", func() { v.selectTab((v.activeTab + len(maintenance.Tabs) - 1) % len(maintenance.Tabs)) }},
		{ui.KeyR, "Refresh maintenance evidence", v.refresh},
		{ui.KeyX, "Cancel remaining maintenance", v.cancelOperation},
	} {
		v.actions.Add(action.key, ui.NewKeyAction(action.label, v.keyAction(action.run), true))
	}
	for _, action := range []struct {
		key   tcell.Key
		label string
		run   func()
	}{
		{ui.KeyD, "Review drain options", v.drainOptions},
		{ui.KeyC, "Cordon", func() { v.confirmCordon(true) }},
		{ui.KeyU, "Uncordon", func() { v.confirmCordon(false) }},
	} {
		v.actions.Add(action.key, ui.NewKeyActionWithOpts(action.label, v.keyAction(action.run), ui.ActionOpts{Visible: true, Dangerous: true}))
	}
}

func (v *maintenanceView) keyAction(run func()) func(*tcell.EventKey) *tcell.EventKey {
	return func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		run()
		return nil
	}
}

func (v *maintenanceView) Start() {
	v.active = true
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.RemoveListener(v)
	v.app.Styles.AddListener(v)
	v.app.Prompt().SetModel(v.cmdBuff)
	v.StylesChanged(v.app.Styles)
	if v.snapshot == nil && !v.loading && v.loader != nil {
		v.refresh()
	}
}
func (v *maintenanceView) Stop() {
	v.active, v.loading = false, false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}
func (v *maintenanceView) StylesChanged(styles *config.Styles) { v.applyStyles(styles); v.render() }
func (v *maintenanceView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Flex.Box) {
		return
	}
	_, _, width, height := v.GetInnerRect()
	changed := width != v.width || height != v.height
	v.width, v.height = width, height
	if v.task != nil {
		receipt := v.task.receipt()
		state := maintenanceReceiptState(&receipt)
		if state != v.receiptState {
			v.receiptState, changed = state, true
		}
	}
	if changed {
		v.render()
	}
	v.renderChrome()
	v.Flex.Draw(screen)
}

func maintenanceReceiptState(receipt *operationReceipt) string {
	var state strings.Builder
	fmt.Fprintf(&state, "%t:%v", receipt.CancelRequested, receipt.Ended)
	for index := range receipt.Outcomes {
		outcome := &receipt.Outcomes[index]
		fmt.Fprintf(&state, ":%s:%d:%d", outcome.State, len(outcome.AcceptedSteps), len(outcome.Output))
	}
	return state.String()
}

func (v *maintenanceView) selectTab(tab int) {
	if tab < 0 || tab >= len(maintenance.Tabs) || tab == v.activeTab {
		return
	}
	row, col := v.text.GetScrollOffset()
	v.tabStates[v.activeTab] = investigationTabState{query: v.inspectionQuery, region: v.currentRegion, row: row, col: col}
	v.activeTab = tab
	state := v.tabStates[tab]
	v.inspectionQuery, v.currentRegion = state.query, state.region
	v.cmdBuff.SetText(state.query, "", true)
	v.text.ScrollTo(state.row, state.col)
	v.render()
}
func (v *maintenanceView) destinationCurrent() bool {
	return v.target.Context == v.app.Config.ActiveContextName() && v.revision == v.app.Config.DestinationRevision()
}
func (v *maintenanceView) refresh() {
	if !v.destinationCurrent() {
		v.failure = "Destination changed; reopen maintenance review"
		v.render()
		return
	}
	if v.loader == nil {
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation, target := v.generation, v.target
	ctx, cancel := context.WithTimeout(v.app.sessionContext(), maintenance.CollectTimeout)
	v.cancel, v.loading = cancel, true
	v.renderChrome()
	go func() {
		defer cancel()
		snapshot, err := v.loader(ctx, &target)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if ctx.Err() == context.Canceled || !v.app.IsRunning() {
			return
		}
		go v.app.QueueUpdateDraw(func() {
			if v.active && v.generation == generation && v.destinationCurrent() && v.app.Content.Top() == v {
				v.acceptSnapshot(snapshot, err)
			}
		})
	}()
}
func (v *maintenanceView) acceptSnapshot(snapshot *maintenance.Snapshot, err error) {
	v.loading = false
	if err != nil {
		v.failure = err.Error()
	} else if snapshot == nil {
		v.failure = "No maintenance observation returned"
	} else if !v.matchesSnapshot(snapshot) {
		v.failure = "Node identity changed; reopen maintenance review"
	} else {
		v.failure, v.snapshot = "", snapshot
	}
	v.render()
}

func (v *maintenanceView) matchesSnapshot(snapshot *maintenance.Snapshot) bool {
	identity := &snapshot.Identity
	return identity.UID == string(v.target.UID) && identity.Context == v.target.Context && identity.GVR == v.target.GVR.String() &&
		identity.Name == v.target.Name && identity.Namespace == ""
}
func (v *maintenanceView) render() {
	if v.identityBar == nil {
		return
	}
	v.renderChrome()
	text := "Loading read-only maintenance evidence..."
	if v.snapshot != nil {
		text = v.snapshot.Render(v.activeTab)
	} else if v.failure != "" {
		text = "Maintenance evidence unavailable: " + v.failure
	}
	if v.activeTab == 4 && v.task != nil {
		text = v.renderOutcomes()
	}
	if v.snapshot != nil && v.failure != "" {
		text = "Refresh failed: " + v.failure + "\nPrevious captured evidence retained.\n\n" + text
	}
	query, region := v.inspectionQuery, v.currentRegion
	row, col := v.text.GetScrollOffset()
	// Details' inspection renderer owns the single terminal-markup escape.
	v.Update(text)
	if query != "" {
		v.model.Filter(query)
		if region < v.maxRegions {
			v.currentRegion = region
			v.text.Highlight(fmt.Sprintf("search_%d", region))
		}
	}
	v.text.ScrollTo(row, col)
}
func (v *maintenanceView) renderChrome() {
	if v.identityBar == nil {
		return
	}
	width := v.width
	if width <= 0 {
		width = 80
	}
	styles := v.app.Styles.Semantic()
	for _, item := range []*tview.TextView{v.identityBar, v.tabsBar, v.footer} {
		item.SetBackgroundColor(styles.Panel.Color())
		item.SetTextColor(styles.Text.Color())
	}
	identity := maintenanceTitle + " / " + v.target.Name
	source := "Context: " + v.target.Context + " | capture pending"
	state := "Read-only preview | waiting for evidence"
	if v.snapshot != nil {
		source = "Context: " + v.target.Context + " | " + v.snapshot.CapturedAt.UTC().Format("15:04:05Z")
		state = "Preview | complete Pod page"
		if v.snapshot.Partial() {
			state = "Preview | partial evidence | 6 sources"
		}
	}
	if v.app.Config.IsReadOnly() {
		state = "Read only | maintenance disabled"
	}
	if v.loading {
		state = "Refreshing | prior evidence retained"
	} else if v.failure != "" {
		state = "Unavailable | r refresh"
		if v.snapshot != nil {
			state = "Retained | failed refresh | r refresh"
		}
	}
	if !v.destinationCurrent() {
		state = retainedDestinationChanged
	}
	lines := []string{identity, source, state}
	for index, line := range lines {
		lines[index] = tview.Escape(ui.Truncate(logstream.SafeText(line), width))
	}
	v.identityBar.SetText(strings.Join(lines, "\n"))
	v.tabsBar.SetText(ui.TaskTabs(maintenance.Tabs, v.activeTab, width))
	footer := "d drain  c/u cordon  r refresh  Esc back"
	if width >= 60 {
		footer = "d drain  c cordon  u uncordon  r refresh  Esc back"
	}
	if v.task != nil && v.task.receipt().Ended.IsZero() {
		footer = "x cancel remaining  r refresh  Esc back"
	}
	v.footer.SetText(tview.Escape(ui.Truncate(footer, width)))
}

func (v *maintenanceView) canMaintain(requirePods bool) bool {
	if v.session == nil || v.snapshot == nil || v.loading || v.failure != "" || !v.destinationCurrent() {
		v.app.Flash().Warn("Refresh the captured Node evidence before maintenance")
		return false
	}
	if !v.session.confirm() {
		return false
	}
	if v.task != nil {
		receipt := v.task.receipt()
		if receipt.Ended.IsZero() {
			v.app.Flash().Warn("Maintenance is already running; 5 Outcomes or x cancel")
			return false
		}
		if !v.snapshot.CapturedAt.After(receipt.Ended) {
			v.app.Flash().Warn("Refresh the captured Node after the previous outcome before another maintenance action")
			return false
		}
	}
	if requirePods && !v.snapshot.PodsComplete() {
		v.app.Flash().Warn("Drain needs a complete bounded Pod page; review Evidence")
		return false
	}
	return true
}
func (v *maintenanceView) drainOptions() {
	if !v.canMaintain(true) {
		return
	}
	reviewed := v.snapshot.ReviewedPods()
	defaults := dao.DrainOptions{GracePeriodSeconds: -1, Timeout: 5 * time.Minute}
	ShowDrain(v.origin, []string{v.target.Name}, defaults, func(_ ResourceViewer, _ []string, opts dao.DrainOptions) {
		if !v.canMaintain(true) {
			return
		}
		v.session.timeout = maxOperationDeadline
		if opts.Timeout > 0 {
			v.session.timeout = boundedOperationTimeout(opts.Timeout)
		}
		task := v.session.submit("Drain", []SelectedResourceTarget{v.target}, func(ctx context.Context, target SelectedResourceTarget) error {
			return v.session.drainReviewed(ctx, target, opts, reviewed)
		}, nil)
		if task != nil {
			v.task, v.reviewedPods = task, reviewed
			v.selectTab(4)
			v.render()
		}
	})
}
func (v *maintenanceView) confirmCordon(cordon bool) {
	if !v.canMaintain(false) {
		return
	}
	action := "Cordon"
	if !cordon {
		action = "Uncordon"
	}
	message := action + "?\nContext: " + v.target.Context + "\n" + operationDestination([]SelectedResourceTarget{v.target}) +
		"\nAccepted API change does not prove workload recovery."
	styles := v.app.Styles.Dialog()
	dialog.ShowConfirm(&styles, v.app.Content.Pages, action, message, func() {
		if !v.canMaintain(false) {
			return
		}
		task := v.session.submit(action, []SelectedResourceTarget{v.target}, func(ctx context.Context, target SelectedResourceTarget) error {
			return v.session.cordon(ctx, target, cordon)
		}, nil)
		if task != nil {
			v.task, v.reviewedPods = task, nil
			v.selectTab(4)
			v.render()
		}
	}, func() {})
}
func (v *maintenanceView) cancelOperation() {
	if v.task == nil {
		v.app.Flash().Warn("No maintenance operation to cancel")
		return
	}
	v.task.cancelRemaining()
	v.selectTab(4)
	v.render()
}
func (v *maintenanceView) renderOutcomes() string {
	receipt := v.task.receipt()
	var out strings.Builder
	fmt.Fprintf(&out, "OPERATION #%d %s\nContext: %s | Node UID: %s\nStarted: %s\n",
		receipt.ID, receipt.Action, receipt.Context, v.target.UID, receipt.Started.UTC().Format("15:04:05Z"))
	if receipt.CancelRequested {
		out.WriteString("Cancellation requested; accepted changes remain.\n")
	}
	for index := range receipt.Outcomes {
		outcome := &receipt.Outcomes[index]
		fmt.Fprintf(&out, "%s %s\n", outcome.State, outcome.Target.Path())
		if outcome.State == operationRunning {
			fmt.Fprintf(&out, "%d acknowledged steps so far; preflight, requests or Pod-removal waits may still be running.\n", len(outcome.AcceptedSteps))
		}
		for _, step := range outcome.AcceptedSteps {
			fmt.Fprintf(&out, "ACCEPTED: %s\n", step)
		}
		if outcome.Err != nil {
			fmt.Fprintf(&out, "%s\n", operationOutcomeError(outcome))
		}
		if outcome.Output != "" {
			out.WriteString(outcome.Output)
			out.WriteByte('\n')
		}
	}
	out.WriteString("\nINDEPENDENT RECOVERY EVIDENCE\n")
	if v.snapshot == nil || !v.snapshot.CapturedAt.After(receipt.Started) || v.failure != "" {
		out.WriteString("No fresh post-start observation. r refresh to inspect the captured Node.\n")
	} else {
		fmt.Fprintf(&out, "Captured %s: Ready=%s, unschedulable=%t\n", v.snapshot.CapturedAt.UTC().Format("15:04:05Z"), v.snapshot.Node.Ready, v.snapshot.Node.Unschedulable)
		if !v.snapshot.PodsComplete() {
			out.WriteString("Pod coverage partial; remaining reviewed Pods cannot be counted.\n")
		} else if v.reviewedPods != nil {
			remaining := 0
			for path, uid := range v.snapshot.ReviewedPods() {
				if v.reviewedPods[path] == uid {
					remaining++
				}
			}
			fmt.Fprintf(&out, "%d original reviewed Pod UIDs still observed on this Node. Absence here is not proof of deletion or replacement readiness.\n", remaining)
		}
	}
	out.WriteString("API acceptance is not controller completion. Native wait reports original Pod removal separately. " +
		"Workload replacement readiness remains unverified.\nUNKNOWN: inspect state before retrying; no automatic retry.\n" +
		"x cancel remaining | :operations retains all receipts | u reviews uncordon.\n")
	return logstream.SafeText(out.String())
}

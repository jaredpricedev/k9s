// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/backup"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/util/validation"
)

const backupCommandToken = "backup-review"
const backupTitle = "Backup review"

type backupView struct {
	*Details
	target                          SelectedResourceTarget
	snapshot                        *backup.Snapshot
	loader                          func(context.Context, *SelectedResourceTarget) (*backup.Snapshot, error)
	cancel                          context.CancelFunc
	generation, destinationRevision uint64
	active, loading                 bool
	activeTab                       int
	tabStates                       [5]investigationTabState
	identityBar, tabsBar, footer    *tview.TextView
	width, height                   int
	refreshFailure                  string
}

func (c *Command) backupCommand(line string) {
	args := strings.Fields(line)
	namespace := client.CleanseNamespace(c.app.Config.ActiveNamespace())
	if len(args) != 2 || namespace == "" || namespace == AllScopes || namespace == "*" {
		c.app.Flash().Warn("Use :backup-review <controller-namespace> in an explicit application namespace")
		return
	}
	if errors := validation.IsDNS1123Label(args[1]); len(errors) > 0 {
		c.app.Flash().Warn("Enter a valid explicit Velero controller namespace")
		return
	}
	conn, err := pinInspectionConnection(c.app.Conn())
	if err != nil {
		c.app.Flash().Err(err)
		return
	}
	target := SelectedResourceTarget{Context: c.app.Config.ActiveContextName(), Namespace: namespace, Name: args[1]}
	v := &backupView{Details: NewDetails(c.app, backupTitle, namespace, contentInspection, true), target: target, destinationRevision: c.app.Config.DestinationRevision()}
	v.loader = func(ctx context.Context, _ *SelectedResourceTarget) (*backup.Snapshot, error) {
		reader, err := conn.DynDial()
		if err != nil {
			return nil, fmt.Errorf("Velero reader unavailable")
		}
		snapshot := backup.Collect(ctx, reader, &backup.Scope{Context: target.Context, Namespace: namespace, ControllerNamespace: args[1]}, time.Now())
		return snapshot, nil
	}
	if err := c.app.inject(v, false); err != nil {
		c.app.Flash().Err(err)
	}
}

func (*backupView) CompactWorkspace() bool { return true }

func (v *backupView) Init(ctx context.Context) error {
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
	for index, key := range []tcell.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5} {
		tab := index
		v.actions.Add(key, ui.NewKeyAction(backup.Tabs[index], func(event *tcell.EventKey) *tcell.EventKey {
			if v.cmdBuff.IsActive() {
				return event
			}
			v.selectTab(tab)
			return nil
		}, true))
	}
	v.actions.Add(tcell.KeyTab, ui.NewKeyAction("Next backup tab", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.selectTab((v.activeTab + 1) % len(backup.Tabs))
		return nil
	}, true))
	v.actions.Add(tcell.KeyBacktab, ui.NewKeyAction("Previous backup tab", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.selectTab((v.activeTab + len(backup.Tabs) - 1) % len(backup.Tabs))
		return nil
	}, true))
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Refresh backup evidence", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.refresh()
		return nil
	}, true))
	v.actions.Add(ui.KeyC, ui.NewKeyAction("Cancel collection", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		if v.cancel != nil {
			v.cancel()
		}
		v.generation++
		v.loading = false
		v.refreshFailure = "Canceled; prior evidence retained"
		v.render()
		return nil
	}, true))
	v.render()
	return nil
}

func (v *backupView) Start() {
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
func (v *backupView) Stop() {
	v.active, v.loading = false, false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.app.Styles.RemoveListener(v)
	v.Details.Stop()
}
func (v *backupView) StylesChanged(styles *config.Styles) { v.applyStyles(styles); v.render() }
func (v *backupView) Draw(screen tcell.Screen) {
	if ui.DrawTaskSizeNotice(screen, v.Flex.Box) {
		return
	}
	_, _, width, height := v.GetInnerRect()
	if width != v.width || height != v.height {
		v.width, v.height = width, height
		v.render()
	}

	v.renderChrome()
	v.Flex.Draw(screen)
}
func (v *backupView) selectTab(tab int) {
	if tab < 0 || tab >= len(backup.Tabs) || tab == v.activeTab {
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
func (v *backupView) destinationCurrent() bool {
	return v.target.Context == v.app.Config.ActiveContextName() && v.destinationRevision == v.app.Config.DestinationRevision()
}
func (v *backupView) refresh() {
	if !v.destinationCurrent() {
		v.refreshFailure = "Destination changed; reopen backup review"
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
		v.app.QueueUpdateDraw(func() {
			v.applyRefreshResult(generation, snapshot, err)
		})
	}()
}
func (v *backupView) applyRefreshResult(generation uint64, snapshot *backup.Snapshot, err error) bool {
	if !v.active || v.generation != generation || !v.destinationCurrent() || v.app.Content.Top() != v {
		return false
	}
	v.acceptSnapshot(snapshot, err)
	return true
}
func (v *backupView) acceptSnapshot(snapshot *backup.Snapshot, err error) {
	v.loading = false
	if err != nil {
		v.refreshFailure = "Collection failed or timed out"
	} else if snapshot == nil {
		v.refreshFailure = "No backup observation returned"
	} else if v.snapshot != nil && snapshot.CollectionFailed() {
		v.refreshFailure = "All refresh sources denied or unavailable; previous evidence retained"
	} else {
		v.refreshFailure = ""
		v.snapshot = snapshot
	}
	v.render()
}
func (v *backupView) render() {
	if v.identityBar == nil {
		return
	}
	v.renderChrome()
	text := "Loading read-only backup evidence..."
	if v.snapshot != nil {
		text = v.snapshot.Render(v.activeTab)
	} else if v.refreshFailure != "" {
		text = "Backup evidence unavailable: " + v.refreshFailure
	}
	if v.snapshot != nil && v.refreshFailure != "" {
		text = "Refresh failed: " + v.refreshFailure + "\nPrevious captured evidence retained.\n\n" + text
	}
	query, region := v.inspectionQuery, v.currentRegion
	row, col := v.text.GetScrollOffset()
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
func (v *backupView) renderChrome() {
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
	identity := backupTitle + " / " + v.target.Path()
	source := retainedObservationPending
	state := retainedWaitingForReads
	if v.snapshot != nil {
		source = v.snapshot.CapturedAt.UTC().Format("15:04:05Z")
		state = "RO | observed page"
		if v.snapshot.Partial() {
			state = "RO | partial | 5 sources"
		}
	}
	if v.loading {
		state = "RO | refreshing; retained"
	} else if v.refreshFailure != "" {
		state = "Unavailable | r retry | " + v.refreshFailure
		if v.snapshot != nil {
			state = "Retained / failed refresh"
		}
	}
	if !v.destinationCurrent() {
		state = retainedDestinationChanged
	}
	lines := []string{identity + " | " + v.target.Context, "Backup success != recoverability", source + " | " + state}
	for index, line := range lines {
		lines[index] = tview.Escape(ui.Truncate(line, width))
	}
	v.identityBar.SetText(strings.Join(lines, "\n"))
	v.tabsBar.SetText(ui.TaskTabs(backup.Tabs, v.activeTab, width))
	v.footer.SetText(tview.Escape(ui.Truncate("Tab next r refresh c cancel / Esc back", width)))
}

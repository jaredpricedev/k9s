// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/ui/dialog"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/k9s/internal/vul"
	"github.com/derailed/k9s/internal/watch"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

// ExitStatus indicates UI exit conditions.
var ExitStatus = ""

const (
	splashDelay      = 1 * time.Second
	clusterRefresh   = 15 * time.Second
	clusterInfoWidth = 24
)

// App represents an application view.
type App struct {
	version   string
	lifecycle appLifecycle
	sessionMu sync.RWMutex
	*ui.App
	Content            *PageStack
	command            *Command
	commandSuggestions *commandSuggestions
	factory            *watch.Factory
	cancelFn           context.CancelFunc
	clusterModel       *model.ClusterInfo
	cmdHistory         *model.History
	filterHistory      *model.History
	fluxActions        map[fluxActionKey]struct{}
	logRecordings      logRecordingRegistry
	operations         operationRegistry
	conRetry           int32
	showHeader         bool
	showLogo           bool
	showCrumbs         bool
	headerOverride     *bool
	headerFlex         *tview.Flex
}

// NewApp returns a K9s app instance.
func NewApp(cfg *config.Config) *App {
	a := App{
		App:           ui.NewApp(cfg, cfg.K9s.ActiveContextName()),
		cmdHistory:    model.NewHistory(model.MaxHistory),
		filterHistory: model.NewHistory(model.MaxHistory),
		Content:       NewPageStack(),
	}
	a.ReloadStyles()

	a.Views()["statusIndicator"] = ui.NewStatusIndicator(a.App, a.Styles)
	a.Views()["clusterInfo"] = NewClusterInfo(&a)

	return &a
}

// ReloadStyles reloads skin file.
func (a *App) ReloadStyles() {
	a.RefreshStyles(a)
}

// UpdateClusterInfo updates clusterInfo panel
func (a *App) UpdateClusterInfo() {
	if a.factory != nil {
		a.clusterModel.Reset(a.factory)
	}
}

// ConOK checks the connection is cool, returns false otherwise.
func (a *App) ConOK() bool {
	return atomic.LoadInt32(&a.conRetry) == 0
}

// Init initializes the application.
func (a *App) Init(version string, _ int) error {
	a.version = model.NormalizeVersion(version)

	ctx := context.WithValue(a.sessionContext(), internal.KeyApp, a)
	if err := a.Content.Init(ctx); err != nil {
		return err
	}
	a.Content.AddListener(a.Crumbs())
	a.Content.AddListener(a.Menu())

	a.App.Init()
	a.SetInputCapture(a.keyboard)
	a.bindKeys()

	// Allow initialization even without a valid connection
	// We'll fall back to context view in defaultCmd
	if a.Conn() != nil {
		ns := a.Config.ActiveNamespace()
		a.factory = watch.NewFactory(a.Conn())
		a.initFactory(ns)

		a.clusterModel = model.NewClusterInfo(a.factory, a.version, a.Config.K9s)
		a.clusterModel.AddListener(a.clusterInfo())
		a.clusterModel.AddListener(a.statusIndicator())
		if a.Conn().ConnectionOK() {
			go func() {
				a.clusterModel.Refresh()
				a.QueueUpdateDraw(func() {
					a.clusterInfo().Init()
				})
			}()
		}
	}

	a.command = NewCommand(a)
	if err := a.command.Init(a.Config.ContextAliasesPath()); err != nil {
		return err
	}
	a.CmdBuff().SetSuggestionFn(a.suggestCommand())

	a.layout(ctx)

	if a.Config.K9s.ImageScans.Enable {
		a.initImgScanner(version)
	}
	a.ReloadStyles()

	return nil
}

func (*App) stopImgScanner() {
	if vul.ImgScanner != nil {
		vul.ImgScanner.Stop()
	}
}

func (a *App) clearHistory() {
	a.cmdHistory.Clear()
	a.filterHistory.Clear()
}

func (a *App) initImgScanner(version string) {
	defer func(t time.Time) {
		slog.Debug("Scanner init time", slogs.Elapsed, time.Since(t))
	}(time.Now())

	vul.ImgScanner = vul.NewImageScanner(a.Config.K9s.ImageScans, slog.Default())
	go vul.ImgScanner.Init(config.AppName, version)
}

func (a *App) layout(ctx context.Context) {
	flash := ui.NewFlash(a.App)
	go flash.Watch(ctx, a.Flash().Channel())

	main := tview.NewFlex().SetDirection(tview.FlexRow)
	a.headerFlex = tview.NewFlex().SetDirection(tview.FlexRow)
	main.AddItem(a.headerFlex, 2, 1, false)
	main.AddItem(a.Content, 0, 10, true)
	if !a.Config.K9s.IsCrumbsless() {
		main.AddItem(a.Crumbs(), 1, 1, false)
	}
	main.AddItem(flash, 1, 1, false)

	a.Main.AddPage("main", main, true, false)
	a.showLogo = !a.Config.K9s.IsLogoless()
	a.updateChrome(0, 0)
	main.SetDrawFunc(func(_ tcell.Screen, x, y, width, height int) (int, int, int, int) {
		a.updateChrome(width, height)
		return x, y, width, height
	})
	if !a.Config.K9s.IsSplashless() {
		a.Main.AddPage("splash", ui.NewSplash(a.Styles, a.version), true, true)
	}
}

func (a *App) keyboard(evt *tcell.EventKey) *tcell.EventKey {
	if a.lifecycle.requested.Load() {
		a.Application.Stop()
		return nil
	}
	if k, ok := a.HasAction(ui.AsKey(evt)); ok && !a.Content.IsTopDialog() {
		return k.Action(evt)
	}

	return evt
}

func (a *App) bindKeys() {
	a.AddActions(ui.NewKeyActionsFromMap(ui.KeyMap{
		tcell.KeyCtrlO:     ui.NewSharedKeyAction("Actions", a.actionsCmd, true),
		tcell.KeyF2:        ui.NewSharedKeyAction("Destination", a.destinationCmd, true),
		tcell.KeyCtrlE:     ui.NewSharedKeyAction("ToggleHeader", a.toggleHeaderCmd, false),
		tcell.KeyCtrlG:     ui.NewSharedKeyAction("ToggleCrumbs", a.toggleCrumbsCmd, false),
		ui.KeyHelp:         ui.NewSharedKeyAction("Help", a.helpCmd, false),
		ui.KeyLeftBracket:  ui.NewSharedKeyAction("Go Back", a.previousCommand, false),
		ui.KeyRightBracket: ui.NewSharedKeyAction("Go Forward", a.nextCommand, false),
		ui.KeyDash:         ui.NewSharedKeyAction("Last View", a.lastCommand, false),
		tcell.KeyCtrlA:     ui.NewSharedKeyAction("Aliases", a.aliasCmd, false),
		tcell.KeyEnter:     ui.NewKeyAction("Goto", a.gotoCmd, false),
		tcell.KeyCtrlC:     ui.NewKeyAction("Quit", a.quitCmd, false),
	}))
}

// ActiveView returns the currently active view.
func (a *App) ActiveView() model.Component {
	return a.Content.GetPrimitive("main").(model.Component)
}

// fullHeader respects an explicit preference; auto protects short terminals.
func (a *App) fullHeader(width, height int) bool {
	if a.headerOverride != nil {
		return *a.headerOverride
	}
	if a.Config.K9s.IsHeadless() {
		return false
	}
	switch a.Config.K9s.UI.HeaderMode {
	case "full":
		return true
	case "compact":
		return false
	default:
		if a.Content != nil {
			if workspace, ok := a.Content.Top().(interface{ CompactWorkspace() bool }); ok && workspace.CompactWorkspace() {
				return false
			}
		}
		return width >= 120 && height >= 34
	}
}

func (a *App) updateChrome(width, height int) {
	full := a.fullHeader(width, height)
	if a.headerFlex == nil {
		return
	}
	if a.headerFlex.ItemAt(0) != nil && full == a.showHeader {
		return
	}
	a.showHeader = full
	a.headerFlex.Clear()
	a.headerFlex.SetBackgroundColor(a.Styles.Semantic().Canvas.Color())
	a.headerFlex.AddItem(a.statusIndicator(), 1, 1, false)
	a.Menu().SetCompact(!full)
	size := 2
	if full {
		a.headerFlex.AddItem(a.buildHeader(), 7, 1, false)
		size = 8
	} else {
		a.headerFlex.AddItem(a.Menu(), 1, 1, false)
	}
	if main, ok := a.Main.GetPrimitive("main").(*tview.Flex); ok {
		main.ResizeItem(a.headerFlex, size, 1)
	}
}

func (a *App) toggleHeader(header, logo bool) {
	a.showLogo = logo
	a.headerOverride = &header
	// Force reconstruction when only the logo preference changes.
	if a.headerFlex != nil {
		a.headerFlex.Clear()
	}
	a.updateChrome(0, 0)
}

func (a *App) toggleCrumbs(flag bool) {
	a.showCrumbs = flag
	flex, ok := a.Main.GetPrimitive("main").(*tview.Flex)
	if !ok {
		slog.Error("Expecting valid flex view main panel. Exiting!")
		a.BailOut(1)
		return
	}
	if a.showCrumbs {
		if _, ok := flex.ItemAt(2).(*ui.Crumbs); !ok {
			flex.AddItemAtIndex(2, a.Crumbs(), 1, 1, false)
		}
	} else {
		flex.RemoveItemAtIndex(2)
	}
}

func (a *App) buildHeader() tview.Primitive {
	a.Menu().SetCompact(false)
	header := tview.NewFlex()
	header.SetBackgroundColor(a.Styles.BgColor())
	header.SetDirection(tview.FlexColumn)
	if !a.showHeader {
		return header
	}

	info := a.clusterInfo()
	header.AddItem(info, clusterInfoWidth, 1, false)
	// Measure the rendered context, user and version values on every draw so
	// short names release their unused space and context changes resize safely.
	header.SetDrawFunc(func(_ tcell.Screen, x, y, width, height int) (int, int, int, int) {
		var labelWidth, valueWidth int
		rowCount := info.GetRowCount()
		for row := range rowCount {
			label, value := info.GetCell(row, 0), info.GetCell(row, 1)
			if label != nil && value != nil {
				labelWidth = max(labelWidth, tview.TaggedStringWidth(label.Text))
				valueWidth = max(valueWidth, tview.TaggedStringWidth(value.Text))
			}
		}
		natural := max(clusterInfoWidth, labelWidth+valueWidth+4)
		infoWidth := min(natural, max(1, width/3))
		header.ResizeItem(info, infoWidth, 1)
		if a.showLogo {
			compact := width-infoWidth-ui.LogoWidth < a.Menu().NaturalWidth()
			logoWidth := ui.LogoWidth
			if compact {
				logoWidth = 7
			}
			a.Logo().SetCompact(compact)
			header.ResizeItem(a.Logo(), logoWidth, 1)
		}
		return x, y, width, height
	})
	header.AddItem(a.Menu(), 0, 1, false)

	if a.showLogo {
		header.AddItem(a.Logo(), ui.LogoWidth, 1, false)
	}

	return header
}

// Halt stop the application event loop.
func (a *App) Halt() {
	a.stopCommandSuggestions()
	a.lifecycle.mu.Lock()
	defer a.lifecycle.mu.Unlock()
	if a.cancelFn != nil {
		a.cancelFn()
		a.cancelFn = nil
	}
}

// Resume restarts periodic work after a temporary terminal handoff.
func (a *App) Resume() {
	parent := a.sessionContext()
	a.lifecycle.mu.Lock()
	if a.lifecycle.requested.Load() {
		a.lifecycle.mu.Unlock()
		return
	}
	if a.cancelFn != nil {
		a.cancelFn()
	}
	ctx, cancel := context.WithCancel(parent)
	a.cancelFn = cancel
	a.lifecycle.mu.Unlock()
	a.resetCommandSuggestions(ctx)

	go a.clusterUpdater(ctx)

	if a.Config.K9s.UI.Reactive {
		if err := a.ConfigWatcher(ctx, a); err != nil {
			slog.Warn("ConfigWatcher failed", slogs.Error, err)
		}
		if err := a.SkinsDirWatcher(ctx, a); err != nil {
			slog.Warn("SkinsWatcher failed", slogs.Error, err)
		}
		if err := a.CustomViewsWatcher(ctx, a); err != nil {
			slog.Warn("CustomView watcher failed", slogs.Error, err)
		}
		if err := a.CustomJumpsWatcher(ctx, a); err != nil {
			slog.Warn("CustomJumps watcher failed", slogs.Error, err)
		}
	}
}

func (a *App) clusterUpdater(ctx context.Context) {
	a.sessionMu.RLock()
	conn, factory, clusterModel := a.Conn(), a.factory, a.clusterModel
	a.sessionMu.RUnlock()
	if ctx.Err() != nil || conn == nil || !conn.ConnectionOK() || factory == nil || clusterModel == nil {
		slog.Debug("Skipping cluster updater - no valid connection")
		return
	}

	if err := a.refreshCluster(ctx); err != nil {
		slog.Error("Cluster updater failed!", slogs.Error, err)
		return
	}

	bf := model.NewExpBackOff(ctx, clusterRefresh, 2*time.Minute)
	delay := clusterRefresh
	for {
		select {
		case <-ctx.Done():
			slog.Debug("ClusterInfo updater canceled!")
			return
		case <-time.After(delay):
			if err := a.refreshCluster(ctx); err != nil {
				slog.Error("Cluster updates failed. Giving up ;(", slogs.Error, err)
				if delay = bf.NextBackOff(); delay == backoff.Stop {
					a.BailOut(1)
					return
				}
			} else {
				bf.Reset()
				delay = clusterRefresh
			}
		}
	}
}

func (a *App) refreshCluster(ctx context.Context) error {
	a.sessionMu.RLock()
	conn, factory, command, revision := a.Conn(), a.factory, a.command, a.Config.DestinationRevision()
	a.sessionMu.RUnlock()
	if conn == nil || factory == nil || a.clusterModel == nil || ctx.Err() != nil {
		return nil
	}

	c := a.Content.Top()
	ok := conn.CheckConnectivity()
	if ctx.Err() != nil || a.Config.DestinationRevision() != revision {
		return nil
	}
	if ok {
		if atomic.LoadInt32(&a.conRetry) > 0 {
			atomic.StoreInt32(&a.conRetry, 0)
			a.Status(model.FlashInfo, "K8s connectivity OK")
			if c != nil {
				a.connectivityComponent(c, true)
			}
		} else {
			a.ClearStatus(true)
		}
		factory.ValidatePortForwards()
	} else if c != nil {
		atomic.AddInt32(&a.conRetry, 1)
		a.connectivityComponent(c, false)
	}

	count, maxConnRetry := atomic.LoadInt32(&a.conRetry), a.Config.K9s.MaxConnRetry
	if count > 0 && retainedDisconnectedWorkspace(c) {
		// Explicit snapshots and connection recovery remain usable without the
		// browsing session. Do not exhaust the background retry budget here.
		a.Status(model.FlashWarn, "K8s connection unavailable; retained workspace · :connection checks · :ctx reconnect")
		return nil
	}
	if count >= maxConnRetry {
		slog.Error("Conn check failed. Bailing out!",
			slogs.Retry, count,
			slogs.MaxRetries, maxConnRetry,
		)
		ExitStatus = fmt.Sprintf("Lost K8s connection (%d). Bailing out!", count)
		a.BailOut(1)
	}
	if count > 0 {
		a.Status(model.FlashWarn, fmt.Sprintf("Dial K8s Toast [%d/%d]", count, maxConnRetry))
		return fmt.Errorf("conn check failed (%d/%d)", count, maxConnRetry)
	}

	// Reload alias
	if command != nil {
		go func() {
			if ctx.Err() != nil || a.Config.DestinationRevision() != revision {
				return
			}
			if err := command.Reset(a.Config.ContextAliasesPath(), false); err != nil {
				slog.Warn("Command reset failed", slogs.Error, err)
				a.QueueUpdateDraw(func() {
					if ctx.Err() != nil || a.Config.DestinationRevision() != revision {
						return
					}
					a.Logo().Warn("Aliases load failed!")
				})
			}
		}()
	}
	// Update cluster info
	a.clusterModel.Refresh()

	return nil
}

func (a *App) switchNS(ns string) error {
	if a.Config.ActiveNamespace() == ns {
		return nil
	}
	if ns == client.ClusterScope {
		ns = client.BlankNamespace
	}
	if err := a.Config.SetActiveNamespace(ns); err != nil {
		return err
	}
	a.statusIndicator().RefreshIdentity()

	return a.factory.SetActiveNS(ns)
}

func (a *App) switchContext(ci *cmd.Interpreter, force bool) error {
	contextName, ok := ci.HasContext()
	if (!ok || a.Config.ActiveContextName() == contextName) && !force {
		return nil
	}
	a.Halt()
	defer a.Resume()
	{
		a.Config.Reset()
		ct, err := a.Config.ActivateContext(contextName)
		if err != nil {
			return err
		}
		if cns, ok := ci.NSArg(); ok {
			ct.Namespace.Active = cns
		}
		p := cmd.NewInterpreter(a.Config.ActiveView())
		p.ResetContextArg()
		if p.IsContextCmd() {
			a.Config.SetActiveView(client.PodGVR.String())
		}
		ns := a.Config.ActiveNamespace()
		if !a.Conn().IsValidNamespace(ns) {
			slog.Warn("Unable to validate namespace", slogs.Namespace, ns)
			if err := a.Config.SetActiveNamespace(ns); err != nil {
				return err
			}
		}
		a.Flash().Infof("Using %q namespace", ns)

		if err := a.Config.Save(true); err != nil {
			slog.Error("Fail to save config to disk", slogs.Subsys, "config", slogs.Error, err)
		}

		if a.factory == nil && a.Conn() != nil {
			a.factory = watch.NewFactory(a.Conn())
			a.clusterModel = model.NewClusterInfo(a.factory, a.version, a.Config.K9s)
			a.clusterModel.AddListener(a.clusterInfo())
			a.clusterModel.AddListener(a.statusIndicator())
		}

		if a.factory != nil {
			a.initFactory(ns)
		}

		if err := a.command.Reset(a.Config.ContextAliasesPath(), true); err != nil {
			return err
		}

		slog.Debug("Switching Context",
			slogs.Context, contextName,
			slogs.Namespace, ns,
			slogs.View, a.Config.ActiveView(),
		)
		a.statusIndicator().RefreshIdentity()
		a.Flash().Infof("Switching context to %q::%q", contextName, ns)
		a.ReloadStyles()
		a.gotoResource(a.Config.ActiveView(), "", true, true)
		if a.clusterModel != nil {
			go a.clusterModel.Reset(a.factory)
		}
	}

	return nil
}

func (a *App) initFactory(ns string) {
	a.factory.Terminate()
	a.factory.Start(ns)
}

// BailOut requests final shutdown. Resource cleanup belongs to Run's defer,
// so keyboard callbacks never wait for recording flushes or network deletion.
func (a *App) BailOut(exitCode int) {
	a.requestExit(exitCode)
	a.Application.Stop()
}

// Run starts the application loop and owns final cleanup on every return path.
func (a *App) Run() error {
	defer a.Shutdown()
	defer a.stopCommandSuggestions()
	stopSignals := a.initSignals()
	defer stopSignals()
	a.Resume()

	go func() {
		if !a.Config.K9s.IsSplashless() {
			select {
			case <-a.sessionContext().Done():
				return
			case <-time.After(splashDelay):
			}
		}
		a.QueueUpdateDraw(func() {
			a.Main.SwitchToPage("main")
			if a.CmdBuff().IsActive() {
				a.SetFocus(a.Prompt())
			}
		})
	}()

	if err := a.command.defaultCmd(true); err != nil {
		return err
	}
	if a.lifecycle.requested.Load() {
		return a.exitError()
	}
	return a.runApplication()
}

// Status reports a new app status for display.
func (a *App) Status(l model.FlashLevel, msg string) {
	a.Flash().SetMessage(l, msg)
}

// IsBenchmarking check if benchmarks are active.
func (a *App) IsBenchmarking() bool {
	return a.Logo().IsBenchmarking()
}

// ClearStatus reset logo back to normal.
func (a *App) ClearStatus(flash bool) {
	a.QueueUpdate(func() {
		a.Logo().Reset()
		if flash {
			a.Flash().Clear()
		}
	})
}

// PrevCmd pops the command stack.
func (a *App) PrevCmd(*tcell.EventKey) *tcell.EventKey {
	if !a.Content.IsLast() {
		a.Content.Pop()
	}

	return nil
}

// destinationCmd reveals unabridged context and namespace in every layout.
func (a *App) destinationCmd(*tcell.EventKey) *tcell.EventKey {
	dialog.ShowMessage(a.Styles, a.Content.Pages, "Destination", a.statusIndicator().FullDestination())
	return nil
}

func (a *App) toggleHeaderCmd(evt *tcell.EventKey) *tcell.EventKey {
	if a.Prompt().InCmdMode() {
		return evt
	}

	a.QueueUpdateDraw(func() {
		a.showHeader = !a.showHeader
		a.toggleHeader(a.showHeader, a.showLogo)
	})

	return nil
}

func (a *App) toggleCrumbsCmd(evt *tcell.EventKey) *tcell.EventKey {
	if a.Prompt().InCmdMode() {
		return evt
	}

	a.QueueUpdateDraw(func() {
		a.showCrumbs = !a.showCrumbs
		a.toggleCrumbs(a.showCrumbs)
	})

	return nil
}

func (a *App) gotoCmd(evt *tcell.EventKey) *tcell.EventKey {
	if a.CmdBuff().IsActive() && !a.CmdBuff().Empty() {
		a.gotoResource(a.GetCmd(), "", true, true)
		a.ResetCmd()
		return nil
	}

	return evt
}

func (a *App) cowCmd(msg string) {
	d := a.Styles.Dialog()
	dialog.ShowError(&d, a.Content.Pages, msg)
}

func (a *App) dirCmd(path string, pushCmd bool) error {
	slog.Debug("Exec Dir command", slogs.Path, path)
	_, err := os.Stat(path)
	if err != nil {
		return err
	}
	if path == "." {
		dir, err := os.Getwd()
		if err == nil {
			path = dir
		}
	}
	if pushCmd {
		a.cmdHistory.Push("dir " + path)
	}

	return a.inject(NewDir(path), true)
}

func (a *App) quitCmd(evt *tcell.EventKey) *tcell.EventKey {
	noExit := a.Config.K9s.NoExitOnCtrlC
	if a.InCmdMode() {
		if isBailoutEvt(evt) && noExit {
			return nil
		}
		return evt
	}

	if !noExit {
		a.BailOut(0)
	}

	return nil
}

func (a *App) helpCmd(evt *tcell.EventKey) *tcell.EventKey {
	if evt != nil && evt.Rune() == '?' && a.Prompt().InCmdMode() {
		return evt
	}

	top := a.Content.Top()
	if top != nil && top.Name() == "help" {
		a.Content.Pop()
		return nil
	}

	if err := a.inject(NewHelp(a), false); err != nil {
		a.Flash().Err(err)
	}

	a.Prompt().Deactivate()
	return nil
}

// previousCommand returns to the command prior to the current one in the history
func (a *App) previousCommand(evt *tcell.EventKey) *tcell.EventKey {
	if evt != nil && evt.Rune() == rune(ui.KeyLeftBracket) && a.Prompt().InCmdMode() {
		return evt
	}
	c, ok := a.cmdHistory.Back()
	if !ok {
		a.App.Flash().Warn("Can't go back any further")
		return evt
	}
	a.gotoResource(c, "", true, false)
	return nil
}

// nextCommand returns to the command subsequent to the current one in the history
func (a *App) nextCommand(evt *tcell.EventKey) *tcell.EventKey {
	if evt != nil && evt.Rune() == rune(ui.KeyRightBracket) && a.Prompt().InCmdMode() {
		return evt
	}
	c, ok := a.cmdHistory.Forward()
	if !ok {
		a.App.Flash().Warn("Can't go forward any further")
		return evt
	}
	// We go to the resource before updating the history so that
	// gotoResource doesn't add this command to the history
	a.gotoResource(c, "", true, false)
	return nil
}

// lastCommand switches between the last command and the current one a la `cd -`
func (a *App) lastCommand(evt *tcell.EventKey) *tcell.EventKey {
	if evt != nil && evt.Rune() == ui.KeyDash && a.Prompt().InCmdMode() {
		return evt
	}
	c, ok := a.cmdHistory.Top()
	if !ok {
		a.App.Flash().Warn("No previous view to switch to")
		return evt
	}
	a.gotoResource(c, "", true, false)

	return nil
}

func (a *App) aliasCmd(*tcell.EventKey) *tcell.EventKey {
	if a.Content.Top() != nil && a.Content.Top().Name() == aliasTitle {
		a.Content.Pop()
		return nil
	}

	if err := a.inject(NewAlias(client.AliGVR), false); err != nil {
		a.Flash().Err(err)
	}

	return nil
}

func (a *App) gotoResource(c, path string, clearStack, pushCmd bool) {
	err := a.command.run(cmd.NewInterpreter(c), path, clearStack, pushCmd)
	if err != nil {
		d := a.Styles.Dialog()
		dialog.ShowErrorRecovery(&d, a.Content.Pages, err.Error(), dialog.ErrorRecovery{
			Title:       "Command failed",
			Instruction: "Your prior view is retained. Edit the command before submitting it again; ? lists contextual actions.",
			ActionLabel: "Edit command",
			Action:      func() { a.restoreFailedCommand(c) },
		})
	}
}

func (a *App) restoreFailedCommand(command string) {
	if top := a.Content.Top(); top != nil {
		a.SetFocus(top)
	}
	a.CmdBuff().SetActive(true)
	a.CmdBuff().SetText(command, "", true)
}

func (a *App) inject(c model.Component, clearStack bool) error {
	ctx := context.WithValue(a.sessionContext(), internal.KeyApp, a)
	if err := c.Init(ctx); err != nil {
		slog.Error("Component init failed",
			slogs.Error, err,
			slogs.CompName, c.Name(),
		)
		return err
	}
	if clearStack {
		a.Content.Clear()
	}
	a.Content.Push(c)

	return nil
}

func (a *App) clusterInfo() *ClusterInfo {
	return a.Views()["clusterInfo"].(*ClusterInfo)
}

func (a *App) statusIndicator() *ui.StatusIndicator {
	return a.Views()["statusIndicator"].(*ui.StatusIndicator)
}

// Investigation presentation state is owned by the draw goroutine. The connectivity
// poller is not; ignore queued work if navigation has changed the top component.
func (a *App) connectivityComponent(c model.Component, connected bool) {
	if retainedDisconnectedWorkspace(c) {
		return
	}
	switch c.(type) {
	case *HubbleView, *Pulse, *inspectionDetails, *comparisonView, *evidenceView, *capabilityDetails, *workspaceLogEntry:
		a.QueueUpdateDraw(func() {
			if a.Content.Top() != c {
				return
			}
			if atomic.LoadInt32(&a.conRetry) > 0 {
				c.Stop()
			} else {
				c.Start()
			}
		})
		return
	}
	if connected {
		c.Start()
	} else {
		c.Stop()
	}
}

func retainedDisconnectedWorkspace(c model.Component) bool {
	switch c.(type) {
	case *connectionHealthDetails, *dailyWorkspace, *desiredReviewView, *rolloutReviewView,
		*capacityView, *configurationView, *maintenanceView, *accessView, *taskbookView, *fleetWorkspace, *backupView:
		return true
	default:
		return false
	}
}

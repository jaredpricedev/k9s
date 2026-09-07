// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/color"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/data"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	logTitle            = "logs"
	logMessage          = "Waiting for logs...\n"
	logFmt              = "([hilite:bg:]%s[-:bg:-])[[green:bg:b]%s[-:bg:-]] "
	logCoFmt            = "([hilite:bg:]%s:[hilite:bg:b]%s[-:bg:-])[[green:bg:b]%s[-:bg:-]] "
	defaultFlushTimeout = 50 * time.Millisecond
)

// Log represents a generic log viewer.
type Log struct {
	childStylesAttached bool
	*tview.Flex

	workbench         *logWorkbench
	promptPurpose     string
	lifecycleMu       sync.Mutex
	lifecycleSeq      atomic.Uint64
	originalCapture   func(*tcell.EventKey) *tcell.EventKey
	started           bool
	app               *App
	logs              *Logger
	indicator         *LogIndicator
	ansiWriter        io.Writer
	model             *model.Log
	cancelFn          context.CancelFunc
	cancelUpdates     bool
	mx                sync.Mutex
	follow            bool
	columnLock        bool
	requestOneRefresh bool
}

var _ model.Component = (*Log)(nil)

// NewLog returns a new viewer.
func NewLog(gvr *client.GVR, opts *dao.LogOptions) *Log {
	return &Log{
		Flex:  tview.NewFlex(),
		model: model.NewLog(gvr, opts, defaultFlushTimeout),
	}
}

func (*Log) SetCommand(*cmd.Interpreter)            {}
func (*Log) SetFilter(string, bool)                 {}
func (*Log) SetLabelSelector(labels.Selector, bool) {}

// Init initializes the viewer.
func (l *Log) Init(ctx context.Context) (err error) {
	if l.app, err = extractApp(ctx); err != nil {
		return err
	}
	l.model.Configure(l.app.Config.K9s.Logger)

	l.SetBorder(true)
	l.SetDirection(tview.FlexRow)

	l.indicator = NewLogIndicator(l.app.Config, l.app.Styles, l.isContainerLogView())
	// The compact workbench status replaces the legacy indicator row.
	if !l.model.HasDefaultContainer() {
		l.indicator.ToggleAllContainers()
	}
	l.indicator.Refresh()

	l.logs = NewLogger(l.app)
	if e := l.logs.Init(ctx); e != nil {
		return e
	}
	l.logs.SetBorderPadding(0, 0, 1, 1)
	l.logs.SetText("[orange::d]" + logMessage)
	l.logs.SetWrap(l.app.Config.K9s.Logger.TextWrap)
	l.logs.SetMaxLines(l.app.Config.K9s.Logger.BufferSize)

	l.ansiWriter = tview.ANSIWriter(l.logs, l.app.Styles.Views().Log.FgColor.String(), l.app.Styles.Views().Log.BgColor.String())
	l.childStylesAttached = true
	l.workbench = newLogWorkbench(l, l.app.Config.K9s.Logger.BufferSize)
	l.workbench.follow = !l.app.Config.K9s.Logger.DisableAutoscroll
	l.workbench.showTime = l.app.Config.K9s.Logger.ShowTime
	l.workbench.wrap = l.app.Config.K9s.Logger.TextWrap
	l.workbench.columnLock = l.app.Config.K9s.Logger.ColumnLock
	l.workbench.detail.SetWrap(l.workbench.wrap)
	l.AddItem(l.workbench, 0, 1, true)
	l.bindKeys()
	l.workbench.register()

	l.StylesChanged(l.app.Styles)
	l.toggleFullScreen()

	l.model.Init(l.app.factory)
	contextName := l.app.Config.ActiveContextName()
	clusterName, _ := l.app.Config.ActiveClusterName(contextName)
	l.model.ConfigureSource(contextName, clusterName, true)
	l.updateTitle()

	l.follow = !l.app.Config.K9s.Logger.DisableAutoscroll
	l.columnLock = l.app.Config.K9s.Logger.ColumnLock

	l.model.ToggleShowTimestamp(l.app.Config.K9s.Logger.ShowTime)

	return nil
}

// InCmdMode checks if prompt is active.
func (l *Log) InCmdMode() bool {
	return l.logs.cmdBuff.InCmdMode()
}

// LogCanceled indicates no more logs are coming.
func (l *Log) LogCanceled() {
	if l.workbench != nil {
		l.workbench.flush()
		return
	}
	slog.Debug("Logs watcher canceled!")
	l.Flush([][]byte{[]byte("\n🏁 [red::b]Stream exited! No more logs...")})
}

// LogStop disables log flushes.
func (l *Log) LogStop() {
	if l.workbench != nil {
		return
	}
	slog.Debug("Logs watcher stopped!")
	l.mx.Lock()
	defer l.mx.Unlock()

	l.cancelUpdates = true
}

// LogResume resume log flushes.
func (l *Log) LogResume() {
	if l.workbench != nil {
		return
	}
	l.mx.Lock()
	defer l.mx.Unlock()

	l.cancelUpdates = false
}

// LogCleared clears the logs.
func (l *Log) LogCleared() {
	if l.workbench != nil {
		return
	}
	l.app.QueueUpdateDraw(func() {
		l.logs.Clear()
	})
}

// LogFailed notifies an error occurred.
func (l *Log) LogFailed(err error) {
	if l.workbench != nil {
		now := time.Now()
		l.workbench.ingest([]logstream.Entry{{
			RuntimeTime: now, Raw: err.Error(), Message: err.Error(),
			Marker: &logstream.Marker{
				Kind: "client-error", Origin: "client", Message: err.Error(), Time: now, Approximate: true,
			},
		}})
		l.workbench.flush()
		return
	}
	l.app.QueueUpdateDraw(func() {
		l.app.Flash().Err(err)
		if l.logs.GetText(true) == logMessage {
			l.logs.Clear()
		}
		if _, err = l.ansiWriter.Write([]byte(tview.Escape(color.Colorize(err.Error(), color.Red)))); err != nil {
			slog.Error("Log line write failed", slogs.Error, err)
		}
	})
}

// LogChanged updates the logs.
func (l *Log) LogChanged(lines [][]byte) {
	if l.workbench != nil {
		return
	}
	l.app.QueueUpdateDraw(func() {
		if l.logs.GetText(true) == logMessage {
			l.logs.Clear()
		}
		l.Flush(lines)
	})
}

// BufferCompleted indicates input was accepted.
func (l *Log) BufferCompleted(text, _ string) {
	// FishBuff emits debounced completion while typing on a worker. Apply only
	// on prompt deactivation (Enter/Escape), which occurs on the draw thread.
	if l.workbench != nil {
		return
	}
	l.model.Filter(text)
	l.updateTitle()
}

// BufferChanged indicates the buffer was changed.
func (*Log) BufferChanged(_, _ string) {}

// BufferActive indicates the buff activity changed.
func (l *Log) BufferActive(state bool, k model.BufferKind) {
	l.app.BufferActive(state, k)
	if l.workbench != nil && !state && !l.workbench.stopped.Load() {
		text := l.logs.cmdBuff.GetText()
		if l.promptPurpose == "sinceTime" {
			l.promptPurpose = ""
			if text != "" {
				if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
					l.workbench.notice = "Invalid timestamp: " + err.Error()
				} else {
					l.workbench.notice = "Server sinceTime jump: available server history only"
					l.runModel(func(ctx context.Context) { _ = l.model.SetSinceTime(ctx, text) })
				}
			}
		} else if err := l.workbench.filter(text); err != nil {
			l.workbench.notice = "Filter ERROR: " + err.Error()
		} else {
			l.workbench.notice = "Client filter applied; markers retained"
		}
		l.workbench.render()
	}
}

// StylesChanged reports skin changes.
func (l *Log) StylesChanged(s *config.Styles) {
	l.SetBackgroundColor(s.Views().Log.BgColor.Color())
	l.logs.SetTextColor(s.Views().Log.FgColor.Color())
	l.logs.SetBackgroundColor(s.Views().Log.BgColor.Color())
	if l.workbench != nil {
		l.workbench.table.SetBackgroundColor(s.Views().Log.BgColor.Color())
		l.workbench.detail.SetBackgroundColor(s.Views().Log.BgColor.Color())
		l.workbench.status.SetBackgroundColor(s.Views().Log.BgColor.Color())
	}
}

// GetModel returns the log model.
func (l *Log) GetModel() *model.Log {
	return l.model
}

// Hints returns a collection of menu hints.
func (l *Log) Hints() model.MenuHints {
	hints := l.logs.Actions().Hints()
	primary := map[string]bool{"?": true, "s": true, "c": true, "Shift-R": true, "p": true}
	for i := range hints {
		hints[i].Visible = primary[hints[i].Mnemonic]
	}
	return hints
}

// ExtraHints returns additional hints.
func (*Log) ExtraHints() map[string]string {
	return nil
}

func (l *Log) cancel() {
	l.mx.Lock()
	defer l.mx.Unlock()
	if l.cancelFn != nil {
		l.cancelFn()
		l.cancelFn = nil
	}
}

func (l *Log) getContext() context.Context {
	l.cancel()
	ctx := context.Background()
	ctx, l.cancelFn = context.WithCancel(ctx)

	return ctx
}

// Start runs the component.
// runModel serializes collection lifecycle off the draw thread. A newer request
// cancels its predecessor and obsolete queued requests cannot resurrect streams.
func (l *Log) runModel(action func(context.Context)) {
	ctx := l.getContext()
	seq := l.lifecycleSeq.Add(1)
	go func() {
		l.lifecycleMu.Lock()
		defer l.lifecycleMu.Unlock()
		if seq != l.lifecycleSeq.Load() || ctx.Err() != nil {
			return
		}
		action(ctx)
	}()
}
func (l *Log) Start() {
	if l.started {
		return
	}
	l.started = true
	l.model.AddListener(l)
	l.app.Styles.AddListener(l)
	if !l.childStylesAttached {
		l.app.Styles.AddListener(l.logs)
		l.app.Styles.AddListener(l.indicator)
		l.childStylesAttached = true
	}
	l.logs.cmdBuff.AddListener(l)
	l.logs.cmdBuff.AddListener(l.logs)
	l.logs.cmdBuff.AddListener(l.app.Prompt())
	l.workbench.start()
	l.originalCapture = l.app.GetInputCapture()
	l.app.SetInputCapture(l.captureWorkbenchKey)
	l.runModel(l.model.Start)
	l.updateTitle()
}

// Stop never joins a draw worker or disk scan from the draw thread.
func (l *Log) Stop() {
	if !l.started {
		l.logs.Stop()
		l.app.Styles.RemoveListener(l.indicator)
		l.childStylesAttached = false
		return
	}
	l.started = false
	l.workbench.stop()
	l.model.RemoveListener(l)
	l.cancel()
	seq := l.lifecycleSeq.Add(1)
	go func() {
		l.lifecycleMu.Lock()
		defer l.lifecycleMu.Unlock()
		if seq == l.lifecycleSeq.Load() {
			l.model.Stop()
		}
	}()
	l.app.SetInputCapture(l.originalCapture)
	l.app.Styles.RemoveListener(l)
	l.logs.Stop()
	l.app.Styles.RemoveListener(l.indicator)
	l.childStylesAttached = false
	l.logs.cmdBuff.RemoveListener(l)
	l.logs.cmdBuff.RemoveListener(l.app.Prompt())
}

// Name returns the component name.
func (*Log) Name() string { return logTitle }

func (l *Log) bindKeys() {
	l.logs.Actions().Bulk(ui.KeyMap{
		ui.Key0:         ui.NewKeyAction("tail", l.sinceCmd(-1), true),
		ui.Key1:         ui.NewKeyAction("head", l.sinceCmd(0), true),
		ui.Key2:         ui.NewKeyAction("1m", l.sinceCmd(60), true),
		ui.Key3:         ui.NewKeyAction("5m", l.sinceCmd(5*60), true),
		ui.Key4:         ui.NewKeyAction("15m", l.sinceCmd(15*60), true),
		ui.Key5:         ui.NewKeyAction("30m", l.sinceCmd(30*60), true),
		ui.Key6:         ui.NewKeyAction("1h", l.sinceCmd(60*60), true),
		tcell.KeyEnter:  ui.NewSharedKeyAction("Filter", l.filterCmd, false),
		tcell.KeyEscape: ui.NewKeyAction("Back", l.resetCmd, false),
		ui.KeyQ:         ui.NewKeyAction("Back", l.resetCmd, false),
		ui.KeyShiftC:    ui.NewKeyAction("Clear", l.clearCmd, true),
		ui.KeyM:         ui.NewKeyAction("Mark", l.markCmd, true),
		ui.KeyS:         ui.NewKeyAction("Toggle AutoScroll", l.toggleAutoScrollCmd, true),
		ui.KeyShiftL:    ui.NewKeyAction("Toggle ColumnLock", l.toggleColumnLockCmd, true),
		ui.KeyF:         ui.NewKeyAction("Toggle FullScreen", l.toggleFullScreenCmd, true),
		ui.KeyT:         ui.NewKeyAction("Toggle Timestamp", l.toggleTimestampCmd, true),
		ui.KeyW:         ui.NewKeyAction("Toggle Wrap", l.toggleTextWrapCmd, true),
		tcell.KeyCtrlS:  ui.NewKeyAction("Save", l.SaveCmd, true),
		ui.KeyC:         ui.NewKeyAction("Copy", cpCmd(l.app.Flash(), l.logs.TextView), true),
	})
	if l.model.HasDefaultContainer() {
		l.logs.Actions().Add(ui.KeyA, ui.NewKeyAction("Toggle AllContainers", l.toggleAllContainers, true))
	}
}

func (l *Log) resetCmd(evt *tcell.EventKey) *tcell.EventKey {
	if l.workbench != nil {
		if l.logs.cmdBuff.IsActive() {
			l.logs.cmdBuff.Reset()
			return nil
		}
		if l.workbench.expression != "" {
			if err := l.workbench.filter(""); err != nil {
				l.workbench.notice = err.Error()
				return nil
			}
			l.logs.cmdBuff.ClearText(false)
			l.workbench.render()
			return nil
		}
		return l.app.PrevCmd(evt)
	}
	if !l.logs.cmdBuff.IsActive() {
		if l.logs.cmdBuff.GetText() == "" {
			return l.app.PrevCmd(evt)
		}
	}

	l.logs.cmdBuff.Reset()
	l.logs.cmdBuff.SetActive(false)
	l.model.Filter(l.logs.cmdBuff.GetText())
	l.updateTitle()

	return nil
}

// SendStrokes (testing only!)
func (l *Log) SendStrokes(s string) {
	l.app.Prompt().SendStrokes(s)
}

// SendKeys (testing only!)
func (l *Log) SendKeys(kk ...tcell.Key) {
	for _, k := range kk {
		l.logs.keyboard(tcell.NewEventKey(k, ' ', tcell.ModNone))
	}
}

// Indicator returns the scroll mode viewer.
func (l *Log) Indicator() *LogIndicator {
	return l.indicator
}

func (l *Log) updateTitle() {
	sinceSeconds, since := l.model.SinceSeconds(), "tail"
	if sinceSeconds > 0 && sinceSeconds < 60*60 {
		since = fmt.Sprintf("%dm", sinceSeconds/60)
	}
	if sinceSeconds >= 60*60 {
		since = fmt.Sprintf("%dh", sinceSeconds/(60*60))
	}
	if l.model.IsHead() {
		since = "head"
	}

	title := " Logs"
	if l.model.LogOptions().Previous {
		title = " Previous Logs"
	}
	var (
		path, co = l.model.GetPath(), l.model.GetContainer()
		styles   = l.app.Styles.Frame()
	)
	if co == "" {
		title += ui.SkinTitle(fmt.Sprintf(logFmt, wbText(path), since), &styles)
	} else {
		title += ui.SkinTitle(fmt.Sprintf(logCoFmt, wbText(path), wbText(co), since), &styles)
	}

	buff := l.logs.cmdBuff.GetText()
	if l.workbench != nil {
		buff = l.workbench.expression
	}
	if buff != "" {
		title += ui.SkinTitle(fmt.Sprintf(ui.SearchFmt, wbText(buff)), &styles)
	}
	l.SetTitle(title)
}

// Logs returns the log viewer.
func (l *Log) Logs() *Logger {
	return l.logs
}

// EOL tracks end of lines.
var EOL = []byte{'\n'}

// Flush write logs to viewer.
func (l *Log) Flush(lines [][]byte) {
	defer func() {
		if l.cancelUpdates {
			l.cancelUpdates = false
		}
	}()

	if len(lines) == 0 || (!l.requestOneRefresh && !l.indicator.AutoScroll()) || l.cancelUpdates {
		return
	}
	if l.requestOneRefresh {
		l.requestOneRefresh = false
	}
	for i := range lines {
		if l.cancelUpdates {
			break
		}
		_, _ = l.ansiWriter.Write(lines[i])
	}
	if l.follow {
		if l.columnLock {
			// Enables end tracking without resetting column
			l.logs.SetScrollable(false).SetScrollable(true)
		} else {
			l.logs.ScrollToEnd()
		}
	}
}

// ----------------------------------------------------------------------------
// Actions...

func (l *Log) sinceCmd(n int) func(evt *tcell.EventKey) *tcell.EventKey {
	return func(*tcell.EventKey) *tcell.EventKey {
		if l.workbench != nil {
			l.workbench.flush()
			l.workbench.notice = "Reconnecting to available server history; retained observed rows remain"
			l.runModel(func(ctx context.Context) {
				if n == 0 {
					l.model.Head(ctx)
				} else {
					l.model.SetSinceSeconds(ctx, int64(n))
				}
			})
			return nil
		}
		l.logs.Clear()
		ctx := l.getContext()
		if n == 0 {
			l.model.Head(ctx)
		} else {
			l.model.SetSinceSeconds(ctx, int64(n))
		}
		l.requestOneRefresh = true
		l.updateTitle()

		return nil
	}
}

func (l *Log) toggleAllContainers(evt *tcell.EventKey) *tcell.EventKey {
	if l.app.InCmdMode() {
		return evt
	}
	l.indicator.ToggleAllContainers()
	if l.workbench != nil {
		l.runModel(l.model.ToggleAllContainers)
	} else {
		l.model.ToggleAllContainers(l.getContext())
	}
	l.updateTitle()

	return nil
}

func (l *Log) filterCmd(evt *tcell.EventKey) *tcell.EventKey {
	if l.workbench != nil {
		if l.logs.cmdBuff.IsActive() {
			l.logs.cmdBuff.SetActive(false)
		} else {
			l.workbench.expand()
		}
		return nil
	}
	if !l.logs.cmdBuff.IsActive() {
		_, _ = fmt.Fprintln(l.ansiWriter)
		return evt
	}

	l.logs.cmdBuff.SetActive(false)
	l.model.Filter(l.logs.cmdBuff.GetText())
	l.updateTitle()

	return nil
}

// SaveCmd dumps the logs to file.
func (l *Log) SaveCmd(*tcell.EventKey) *tcell.EventKey {
	if l.workbench != nil && len(l.workbench.rows) > 0 {
		l.workbench.exportVisible()
		return nil
	}
	path, err := saveData(l.app.Config.K9s.ContextScreenDumpDir(), l.model.GetPath(), l.logs.GetText(true))
	if err != nil {
		l.app.Flash().Err(err)
		return nil
	}
	l.app.Flash().Infof("Log %s saved successfully!", path)

	return nil
}

func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0744)
}

func saveData(dir, fqn, logs string) (string, error) {
	if err := ensureDir(dir); err != nil {
		return "", err
	}

	f := fmt.Sprintf("%s-%d.log", fqn, time.Now().UnixNano())
	path := filepath.Join(dir, data.SanitizeFileName(f))
	mod := os.O_CREATE | os.O_WRONLY
	file, err := os.OpenFile(path, mod, 0600)
	if err != nil {
		slog.Error("Unable to save log file",
			slogs.Path, path,
			slogs.Error, err,
		)
		return "", err
	}
	defer func() {
		if err := file.Close(); err != nil {
			slog.Error("Closing Log file failed",
				slogs.Path, path,
				slogs.Error, err,
			)
		}
	}()
	if _, err := file.WriteString(logs); err != nil {
		return "", err
	}

	return path, nil
}

func (l *Log) clearCmd(*tcell.EventKey) *tcell.EventKey {
	if l.workbench != nil {
		l.workbench.clearRetained()
		return nil
	}
	l.model.Clear()
	return nil
}

func (l *Log) markCmd(*tcell.EventKey) *tcell.EventKey {
	if l.workbench != nil {
		return l.workbench.key(tcell.NewEventKey(tcell.KeyRune, 'm', tcell.ModNone))
	}
	_, _, w, _ := l.GetRect()
	_, _ = fmt.Fprintf(l.ansiWriter, "[%s:-:b]%s[-:-:-]\n", l.app.Styles.Views().Log.FgColor.String(), strings.Repeat("-", w-4))
	l.follow = true

	return nil
}

func (l *Log) toggleTimestampCmd(evt *tcell.EventKey) *tcell.EventKey {
	if l.app.InCmdMode() {
		return evt
	}

	if l.workbench != nil {
		l.workbench.showTime = !l.workbench.showTime
		l.workbench.render()
	}
	l.indicator.ToggleTimestamp()
	l.model.ToggleShowTimestamp(l.indicator.showTime)
	l.indicator.Refresh()

	return nil
}

func (l *Log) toggleTextWrapCmd(evt *tcell.EventKey) *tcell.EventKey {
	if l.app.InCmdMode() {
		return evt
	}

	l.indicator.ToggleTextWrap()
	l.logs.SetWrap(l.indicator.textWrap)
	l.indicator.Refresh()

	return nil
}

// ToggleAutoScrollCmd toggles autoscroll status.
func (l *Log) toggleAutoScrollCmd(evt *tcell.EventKey) *tcell.EventKey {
	if l.app.InCmdMode() {
		return evt
	}

	l.indicator.ToggleAutoScroll()
	l.follow = l.indicator.AutoScroll()
	l.indicator.Refresh()

	return nil
}

func (l *Log) toggleColumnLockCmd(evt *tcell.EventKey) *tcell.EventKey {
	if l.app.InCmdMode() {
		return evt
	}

	l.indicator.ToggleColumnLock()
	l.columnLock = l.indicator.ColumnLock()
	if l.workbench != nil {
		l.workbench.notice = "Column lock applies to horizontal table offset while following"
		l.workbench.columnLock = l.columnLock
	}
	l.indicator.Refresh()

	return nil
}

func (l *Log) toggleFullScreenCmd(evt *tcell.EventKey) *tcell.EventKey {
	if l.app.InCmdMode() {
		return evt
	}
	l.indicator.ToggleFullScreen()
	l.toggleFullScreen()
	l.indicator.Refresh()

	return nil
}

func (l *Log) toggleFullScreen() {
	l.SetFullScreen(l.indicator.FullScreen())
	l.SetBorder(!l.indicator.FullScreen())
	if l.indicator.FullScreen() {
		l.logs.SetBorderPadding(0, 0, 0, 0)
	} else {
		l.logs.SetBorderPadding(0, 0, 1, 1)
	}
}

func (l *Log) isContainerLogView() bool {
	return l.model.HasDefaultContainer()
}

// Local help/history/raw-record actions precede application-wide bindings only
// while this Log owns focus. Prompt editing always stays in the prompt.
func (l *Log) captureWorkbenchKey(evt *tcell.EventKey) *tcell.EventKey {
	workbenchKey := evt.Rune() == '?' || evt.Rune() == '[' || evt.Rune() == ']' || evt.Key() == tcell.KeyCtrlR
	if !l.workbench.stopped.Load() && !l.app.Prompt().InCmdMode() && !l.logs.cmdBuff.IsActive() && workbenchKey {
		return l.workbench.key(evt)
	}
	if l.originalCapture != nil {
		return l.originalCapture(evt)
	}
	return evt
}

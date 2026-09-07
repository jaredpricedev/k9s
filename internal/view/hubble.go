// SPDX-License-Identifier: Apache-2.0
// Native Cilium/Hubble integration for k9+.
package view

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/hubble"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	hubbleStatusMode       = "status"
	hubblePeersMode        = "peers"
	hubbleConversationMode = "conversation"
	hubbleHelpMode         = "help"
)

type hubblePeerRow struct {
	Peer  hubble.Peer
	Count int
}

// All view fields belong to the draw goroutine. Only Session ingests concurrently.
type HubbleView struct {
	statusOnly bool
	started    bool
	helpReturn string
	*tview.Flex
	app                      *App
	table                    *tview.Table
	detail, status           *tview.TextView
	prompt                   *tview.InputField
	pages                    *tview.Pages
	scope                    hubble.Scope
	resolve                  func(context.Context) (hubble.Scope, error)
	config                   hubble.Config
	session                  *hubble.Session
	query                    hubble.Query
	mode, expression, notice string
	frozen                   bool
	displayed                []hubble.Event
	rows                     []hubble.Event
	peers                    []hubblePeerRow
	peer                     hubble.Peer
	selected                 hubble.Event
	cancel                   context.CancelFunc
	generation               uint64
	originalCapture          func(*tcell.EventKey) *tcell.EventKey
	prompting                bool
	peerRow, flowRow         int
}

var _ model.Component = (*HubbleView)(nil)

func newHubbleView(scope hubble.Scope, statusOnly bool) *HubbleView {
	w := &HubbleView{
		statusOnly: statusOnly, Flex: tview.NewFlex().SetDirection(tview.FlexRow), scope: scope, mode: hubblePeersMode,
		table: tview.NewTable(), detail: tview.NewTextView(), status: tview.NewTextView(), prompt: tview.NewInputField(), pages: tview.NewPages(),
	}
	if statusOnly {
		w.mode = hubbleStatusMode
	}
	w.table.SetSelectable(true, false).SetFixed(1, 0)
	w.detail.SetWrap(true).SetScrollable(true)
	w.status.SetWrap(false)
	w.prompt.SetLabel("Filter: ")
	w.pages.AddPage("table", w.table, true, true).AddPage(modeDetail, w.detail, true, false)
	w.AddItem(w.status, 5, 0, false).AddItem(w.pages, 0, 1, true)
	w.SetBorder(true)
	w.table.SetInputCapture(w.key)
	w.detail.SetInputCapture(w.key)
	w.table.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		w.freeze()
		return action, event
	})
	w.prompt.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			w.applyFilter(w.prompt.GetText())
		}
		if key == tcell.KeyEnter || key == tcell.KeyEscape {
			w.prompting = false
			w.RemoveItem(w.prompt)
			w.focusContent()
		}
	})
	return w
}
func (*HubbleView) Name() string                           { return "Cilium / Hubble" }
func (*HubbleView) SetCommand(*cmd.Interpreter)            {}
func (*HubbleView) SetLabelSelector(labels.Selector, bool) {}
func (w *HubbleView) SetFilter(s string, _ bool)           { w.applyFilter(s) }
func (w *HubbleView) InCmdMode() bool                      { return w.prompting }
func (*HubbleView) ExtraHints() map[string]string          { return nil }
func (*HubbleView) Hints() model.MenuHints {
	return model.MenuHints{
		{Mnemonic: "enter", Description: "Inspect", Visible: true},
		{Mnemonic: "s", Description: "Freeze/Resume", Visible: true},
		{Mnemonic: "/", Description: "Filter", Visible: true},
		{Mnemonic: "1/2", Description: "Source/Dest pod", Visible: true},
		{Mnemonic: "r", Description: "Reconnect", Visible: true},
		{Mnemonic: "esc", Description: "Back", Visible: true},
	}
}
func (w *HubbleView) Init(ctx context.Context) error {
	var err error
	w.app, err = extractApp(ctx)
	if err != nil {
		return err
	}
	ct, err := w.app.Config.CurrentContext()
	if err != nil {
		return err
	}
	w.config = ct.Hubble
	bg := w.app.Styles.BgColor()
	w.SetBackgroundColor(bg)
	w.table.SetBackgroundColor(bg)
	w.status.SetBackgroundColor(bg)
	w.detail.SetBackgroundColor(bg)
	w.SetTitle(" Cilium / Hubble " + w.app.Config.ActiveContextName() + " ")
	return nil
}
func (w *HubbleView) Start() {
	if w.started {
		return
	}
	w.started = true
	w.originalCapture = w.app.GetInputCapture()
	w.app.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if w.prompting {
			return e
		}
		if !w.app.Prompt().InCmdMode() && (e.Rune() == '/' || e.Rune() == '?' || e.Key() == tcell.KeyEscape) {
			return w.key(e)
		}
		if w.originalCapture != nil {
			return w.originalCapture(e)
		}
		return e
	})
	// Back from a pod jump preserves the inspected snapshot. Resume is explicit.
	if w.session != nil {
		w.frozen = true
		w.notice = "Observation stopped while away; r reconnects (possible gap)"
		w.render()
		return
	}
	w.restart()
}
func (w *HubbleView) Stop() {
	wasStarted := w.started
	w.started = false
	w.generation++
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	w.freeze()
	if w.app != nil && wasStarted {
		w.app.SetInputCapture(w.originalCapture)
	}
}
func (w *HubbleView) restart() {
	if w.app == nil {
		return
	}
	if w.cancel != nil {
		w.cancel()
	}
	w.generation++
	generation := w.generation
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.notice = "History/live handoff and reconnect may have gaps"
	// Resolution and connection never run on the draw goroutine.
	resolve := w.resolve
	scope := w.scope
	cfg := w.config
	q := w.query
	go func() {
		var err error
		if resolve != nil {
			scope, err = resolve(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		w.app.QueueUpdateDraw(func() {
			if generation != w.generation {
				return
			}
			if err != nil {
				w.notice = err.Error()
				w.render()
				return
			}
			scope.Cluster = cfg.ClusterName
			w.scope = scope
			w.session = hubble.NewSession(cfg, scope, q, 10000)
			w.displayed = nil
			w.frozen = false
			w.peer = hubble.Peer{}
			w.rows = nil
			if w.statusOnly {
				w.mode = hubbleStatusMode
			} else {
				w.mode = hubblePeersMode
			}
			session := w.session
			w.render()
			go session.Run(ctx)
			go w.refreshLoop(ctx, generation)
		})
	}()
}
func (w *HubbleView) refreshLoop(ctx context.Context, generation uint64) {
	timer := time.NewTicker(250 * time.Millisecond)
	defer timer.Stop()
	var queued atomic.Bool
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if !queued.CompareAndSwap(false, true) {
				continue
			}
			w.app.QueueUpdateDraw(func() {
				defer queued.Store(false)
				if generation == w.generation {
					w.render()
				}
			})
		}
	}
}
func (w *HubbleView) focus(p tview.Primitive) {
	if w.app != nil {
		w.app.SetFocus(p)
	}
}
func (w *HubbleView) freeze() {
	if w.statusOnly {
		return
	}
	if w.frozen {
		return
	}
	w.frozen = true /* displayed is exactly the dataset last painted, never a newer snapshot. */
}
func (w *HubbleView) applyFilter(s string) {
	q, err := hubble.Compile(s)
	if err != nil {
		w.notice = err.Error() + "; previous filter preserved"
		w.render()
		return
	}
	oldStructured := w.query.Text == "" && w.expression != ""
	w.expression = s
	w.query = q
	w.notice = ""
	if strings.ContainsAny(s, "=<>!") || oldStructured {
		w.restart()
	} else {
		w.render()
	}
}
func (w *HubbleView) key(e *tcell.EventKey) *tcell.EventKey {
	if w.prompting {
		return e
	}
	if e.Key() == tcell.KeyEscape || e.Rune() == 'q' {
		return w.back(e)
	}
	if e.Key() == tcell.KeyEnter {
		return w.inspect()
	}
	switch e.Rune() {
	case 's':
		if w.app != nil && w.cancel == nil {
			w.restart()
			return nil
		}
		if w.mode == hubbleStatusMode {
			return nil
		}
		if w.frozen {
			w.frozen = false
			if w.mode == modeDetail {
				w.mode = hubbleConversationMode
				w.focusContent()
			}
		} else {
			w.freeze()
		}
		w.render()
		return nil
	case 'r':
		w.restart()
		return nil
	case '/':
		w.freeze()
		w.prompting = true
		w.prompt.SetText(w.expression)
		w.AddItem(w.prompt, 1, 0, false)
		w.focus(w.prompt)
		return nil
	case '1', '2':
		w.freeze()
		var ev hubble.Event
		if w.mode == modeDetail {
			ev = w.selected
		} else if w.mode == hubbleConversationMode {
			row, _ := w.table.GetSelection()
			if row > 0 && row <= len(w.rows) {
				ev = w.rows[row-1]
			}
		}
		p := ev.Source
		if e.Rune() == '2' {
			p = ev.Destination
		}
		w.jump(&p)
		return nil
	case '?':
		if w.mode != hubbleHelpMode {
			w.helpReturn = w.mode
		}
		w.freeze()
		w.mode = hubbleHelpMode
		w.render()
		w.focus(w.detail)
		return nil
	}
	if hubbleNavigation(e) {
		w.freeze()
	}
	return e
}
func (w *HubbleView) jump(p *hubble.Peer) {
	if p.Pod == "" {
		w.notice = "No reported pod for this endpoint"
		w.render()
		return
	}
	// Relay can include remote clusters. A same-named local pod is not evidence
	// that the remote endpoint is the same object; refuse cross-cluster guesses.
	if p.Cluster != "" && p.Cluster != w.config.ClusterName {
		w.notice = "Pod cluster unverified: set hubble.clusterName to the local Cilium cluster name"
		w.render()
		return
	}
	if w.app != nil {
		pod := NewPod(client.PodGVR)
		pod.SetInstance(p.Pod)
		if err := w.app.inject(pod, false); err != nil {
			w.notice = err.Error()
			w.render()
		}
	}
}
func (w *HubbleView) render() {
	w.SetTitle(" Cilium / Hubble | " + tview.Escape(hubble.Clean(w.scope.Title)) + " | " + w.mode + " ")
	st := hubble.Status{Phase: "not connected"}
	var evicted uint64
	if w.session != nil {
		st = w.session.Status()
		if !w.frozen {
			w.displayed, evicted = w.session.Store.Snapshot()
		} else {
			_, evicted = w.session.Store.Snapshot()
		}
	}
	state := st.Phase
	if w.frozen {
		state = "FROZEN snapshot | collector: " + st.Phase
	}
	coverage := "unknown"
	if st.CoverageKnown {
		coverage = fmt.Sprintf("%d connected / %d unavailable", st.Connected, st.Unavailable)
	}
	loss := fmt.Sprint(st.Lost)
	if w.statusOnly {
		loss = "not observed (status only)"
	}
	w.status.SetText(fmt.Sprintf(
		"%s | node coverage: %s | retained observed events: %d\n"+
			"Reported loss: %s (%s) | local evictions: %d | L7 redacted\n"+
			"%s %s %s\n"+
			"%s | filter: %s\n"+
			"Enter inspect | s freeze/resume | / filter | 1/2 pod | r reconnect | ? help | Esc back",
		hubble.Clean(state), coverage, len(w.displayed), loss, hubble.Clean(st.LossDetail), evicted,
		hubble.Clean(st.Error), hubble.Clean(st.CoverageError), hubble.Clean(st.NodeEvent),
		hubble.Clean(w.notice), hubble.Clean(w.expression)))
	if w.mode == modeDetail {
		w.pages.SwitchToPage(modeDetail)
		e := w.selected
		w.setDetail(fmt.Sprintf(
			"Observed event %d — %s\n"+
				"%s\n"+
				"Source: %s\n"+
				"Destination: %s\n"+
				"Node: %s\n"+
				"%s %d -> %d\n"+
				"Verdict: %s\n"+
				"Drop reason: %s\n"+
				"L7: %s\n"+
				"Policy evidence: %s", e.ID, e.Origin, e.Time.Format(time.RFC3339Nano), e.Source, e.Destination, e.Node,
			e.Protocol, e.SourcePort, e.DestinationPort, e.Verdict, e.DropReason, e.L7, e.Policy))
		return
	}
	if w.mode == hubbleHelpMode {
		w.pages.SwitchToPage(modeDetail)
		w.setDetail("Hubble network inspection\n" +
			"\n" +
			"Peers count observed events in the retained dataset, not connections or requests.\n" +
			"Enter freezes peers, opens both directions, then event detail. Navigation also freezes.\n" +
			"s explicitly resumes. Incoming traffic cannot evict the frozen snapshot.\n" +
			"1 / 2 jump to reported source / destination pods. Esc backtracks.\n" +
			"r reconnects and replaces the dataset; history/live handoff can have gaps.\n" +
			"\n" +
			"/ plain text searches locally. Structured AND filters:\n" +
			"verdict=dropped protocol=tcp port=443 ip=10.0.0.0/8\n" +
			"Supported fields: verdict, protocol, port (either end), ip (either end).\n" +
			"Malformed expressions leave the prior filter intact. Valid structured changes restart observation.\n" +
			"\n" +
			"Missing flows do not prove traffic was allowed or denied. Coverage is Relay-reported.\n" +
			"No L7 record means visibility unknown. DNS success does not prove policy authorization.\n" +
			"L7 payloads are discarded; export, capture and policy changes are unavailable.")
		return
	}
	w.renderTable(&st)
}

func (w *HubbleView) renderTable(st *hubble.Status) {
	w.pages.SwitchToPage("table")
	row, col := w.table.GetSelection()
	offR, offC := w.table.GetOffset()
	var selectedKey string
	if w.mode == hubblePeersMode && row > 0 && row <= len(w.peers) {
		selectedKey = w.peers[row-1].Peer.Key()
	}
	var selectedID uint64
	if w.mode == hubbleConversationMode && row > 0 && row <= len(w.rows) {
		selectedID = w.rows[row-1].ID
	}
	w.table.Clear()
	put := w.putRow

	switch w.mode {
	case hubbleStatusMode:
		put(0, "NODE", "STATE", "VERSION")
		for i, n := range st.Nodes {
			put(i+1, n.Name, n.State, n.Version)
		}
		if len(st.Nodes) == 0 {
			put(1, "Node detail unavailable", st.CoverageError, st.Version)
		}
	case hubblePeersMode:
		put(0, "PEER", "KIND", "OBSERVED EVENTS")
		counts := map[string]hubblePeerRow{}
		for i := range w.displayed {
			e := &w.displayed[i]
			if !w.query.Match(e) {
				continue
			}
			p := w.scope.Other(e)
			v := counts[p.Key()]
			v.Peer = p
			v.Count++
			counts[p.Key()] = v
		}
		w.peers = nil
		for _, p := range counts {
			w.peers = append(w.peers, p)
		}
		sort.Slice(w.peers, func(i, j int) bool { return w.peers[i].Peer.Key() < w.peers[j].Peer.Key() })
		for i, p := range w.peers {
			put(i+1, p.Peer.String(), p.Peer.Kind, fmt.Sprint(p.Count))
			if p.Peer.Key() == selectedKey {
				row = i + 1
			}
		}
	case hubbleConversationMode:
		put(0, "TIME", "SOURCE", "DESTINATION", "PROTOCOL", "VERDICT", "ORIGIN")
		w.rows = nil
		for i := range w.displayed {
			e := &w.displayed[i]
			if (e.Source.Key() == w.peer.Key() || e.Destination.Key() == w.peer.Key()) && w.query.Match(e) {
				w.rows = append(w.rows, *e)
			}
		}
		for i := range w.rows {
			e := &w.rows[i]
			put(i+1, e.Time.Format("15:04:05.000"), e.Source.String(), e.Destination.String(),
				fmt.Sprintf("%s %d → %d", e.Protocol, e.SourcePort, e.DestinationPort), e.Verdict, e.Origin)
			if e.ID == selectedID {
				row = i + 1
			}
		}
	}
	if w.table.GetRowCount() == 1 {
		put(1, "No observed events; visibility or traffic may be missing")
	}
	if row < 1 {
		row = 1
	}
	if row >= w.table.GetRowCount() {
		row = w.table.GetRowCount() - 1
	}
	w.table.Select(row, col)
	w.table.SetOffset(offR, offC)
	w.SetTitle(" Cilium / Hubble | " + tview.Escape(hubble.Clean(w.scope.Title)) + " | " + w.mode + " ")
}

func (w *HubbleView) setDetail(text string) {
	if w.detail.GetText(false) != text {
		w.detail.SetText(text)
	}
}

func (w *HubbleView) focusContent() {
	if w.mode == modeDetail || w.mode == hubbleHelpMode {
		w.focus(w.detail)
	} else {
		w.focus(w.table)
	}
}

func hubbleNavigation(e *tcell.EventKey) bool {
	switch e.Key() {
	case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyHome, tcell.KeyEnd:
		return true
	}
	return strings.ContainsRune("jkgG", e.Rune()) && e.Rune() != 0
}

func (w *HubbleView) back(e *tcell.EventKey) *tcell.EventKey {
	switch w.mode {
	case modeDetail:
		w.mode = hubbleConversationMode
		w.table.Select(w.flowRow, 0)
	case hubbleConversationMode:
		w.mode = hubblePeersMode
		w.table.Select(w.peerRow, 0)
	case hubbleHelpMode:
		w.mode = w.helpReturn
	default:
		if w.app != nil {
			return w.app.PrevCmd(e)
		}
		return nil
	}
	w.render()
	w.focusContent()
	return nil
}

func (w *HubbleView) inspect() *tcell.EventKey {
	w.freeze()
	row, _ := w.table.GetSelection()
	switch w.mode {
	case hubblePeersMode:
		if row > 0 && row <= len(w.peers) {
			w.peerRow = row
			w.peer = w.peers[row-1].Peer
			w.mode = hubbleConversationMode
			w.table.Select(1, 0)
		}
	case hubbleConversationMode:
		if row > 0 && row <= len(w.rows) {
			w.flowRow = row
			w.selected = w.rows[row-1]
			w.mode = modeDetail
		}
	}
	w.render()
	if w.mode == modeDetail {
		w.focus(w.detail)
	}
	return nil
}

func (w *HubbleView) putRow(r int, values ...string) {
	for c, v := range values {
		cell := tview.NewTableCell(tview.Escape(hubble.Clean(v)))
		if r == 0 {
			cell.SetSelectable(false).SetTextColor(tcell.ColorAqua)
		}
		w.table.SetCell(r, c, cell)
	}
}

// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"errors"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

const (
	browserEnv        = "K9PLUS_BROWSER"
	grafanaDialogPage = "grafana-handoff"
	grafanaDialogMax  = 100
)

// linkDelivery hands a link to the person: the system browser when this
// machine has one that is theirs, otherwise the clipboard. Its parts are
// fields so the order can be tested without starting a program.
type linkDelivery struct {
	goos       string
	getenv     func(string) string
	lookPath   func(string) (string, error)
	start      func(name string, args ...string) error
	copyNative func(string) error
	copyOSC52  func(string) error
}

func newLinkDelivery() *linkDelivery {
	return &linkDelivery{goos: runtime.GOOS, getenv: os.Getenv, lookPath: exec.LookPath, start: startDetached,
		copyNative: clipboard.WriteAll, copyOSC52: writeOSC52}
}

// grafanaDelivery is replaced in tests.
var grafanaDelivery = newLinkDelivery()

// startDetached starts an opener without a shell, without the terminal and
// without waiting for it. The link is one argument, never interpreted.
func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// deliveryOutcome states what was done with a link, in plain sentences.
type deliveryOutcome struct {
	Opened, Copied bool
	Lines          []string
}

// safeLink refuses anything but an http(s) URL without credentials.
func safeLink(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || strings.ContainsAny(raw, " \r\n\t\x00") {
		return errors.New("not an http(s) link without credentials")
	}
	return nil
}

// opener names the program that opens a link in this person's browser, or
// says why there is none. A remote or display-less session has none: a
// browser started there would not be on their screen.
func (d *linkDelivery) opener() (name string, args []string, why string) {
	switch strings.ToLower(strings.TrimSpace(d.getenv(browserEnv))) {
	case "off", "none", "no", "false", "0":
		return "", nil, browserEnv + " turns the browser off"
	}
	if d.getenv("SSH_CONNECTION") != "" || d.getenv("SSH_TTY") != "" {
		return "", nil, "this is an SSH session, so a browser here would not be on your screen"
	}
	switch d.goos {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		if d.getenv("DISPLAY") == "" && d.getenv("WAYLAND_DISPLAY") == "" {
			return "", nil, "no display (DISPLAY and WAYLAND_DISPLAY are unset)"
		}
		name = "xdg-open"
	}
	if _, err := d.lookPath(name); err != nil {
		return "", nil, name + " is not installed"
	}
	return name, args, ""
}

// open tries the system browser. restricted is the reason the app must not
// start a local program, or "".
func (d *linkDelivery) open(link, restricted string) (bool, string) {
	if err := safeLink(link); err != nil {
		return false, "Browser not launched: " + err.Error() + "."
	}
	if restricted != "" {
		return false, "Browser not launched: " + restricted + "."
	}
	name, args, why := d.opener()
	if why != "" {
		return false, "Browser not launched: " + why + "."
	}
	if err := d.start(name, append(args, link)...); err != nil {
		return false, "Browser not launched: " + name + " could not be started."
	}
	return true, "Asked the system to open it in your browser (" + name + ")."
}

// copy puts text on the clipboard: the native one, then the terminal's
// through OSC 52, which also works over SSH. K9PLUS_CLIPBOARD narrows it.
func (d *linkDelivery) copy(text string) (bool, string) {
	mode := clipboardMode()
	if mode != clipboardModeOSC52 && d.copyNative(text) == nil {
		return true, "Copied to the clipboard."
	}
	if mode != clipboardModeNative && d.copyOSC52(text) == nil {
		return true, "Sent to your terminal's clipboard (OSC 52); the terminal decides whether to accept it."
	}
	return false, "Not copied: no clipboard could be reached. The link is below."
}

// deliver opens the link when it can, copies it otherwise, and always says
// what happened so the dialog can show the link in every case.
func (d *linkDelivery) deliver(link, restricted string) deliveryOutcome {
	opened, openLine := d.open(link, restricted)
	if opened {
		return deliveryOutcome{Opened: true, Lines: []string{openLine}}
	}
	copied, copyLine := d.copy(link)
	return deliveryOutcome{Copied: copied, Lines: []string{copyLine, openLine}}
}

// grafanaHandoff is the small dialog that states what a hand-off did and
// always shows the link, so it can be read or selected over SSH.
type grafanaHandoff struct {
	*tview.Flex
	body, keys *tview.TextView
	app        *App
	target     *grafanaTarget
	// origin is the selection the target was made from, when known.
	origin     SelectedResourceTarget
	links      []grafanaLink
	current    int
	outcome    deliveryOutcome
	problem    string
	deliverNow bool
	// offerList adds the key that opens the full dashboard list.
	offerList  bool
	configPath string
	restricted string
	delivery   *linkDelivery
	previous   func(*tcell.EventKey) *tcell.EventKey
	width      int
	lines      int
	keyLine    [2]string
}

// grafanaRestricted is the reason the app starts no local program, or "".
func grafanaRestricted(app *App) string {
	if app.Config.IsReadOnly() {
		return "read-only mode starts no local program"
	}
	return ""
}

func grafanaConfigPath(app *App) string {
	contextName := app.Config.ActiveContextName()
	cluster, err := app.Config.ActiveClusterName(contextName)
	if err != nil {
		return "the context config.yaml"
	}
	return config.AppContextConfig(cluster, contextName)
}

func showGrafanaHandoff(app *App, h *grafanaHandoff) {
	h.app, h.delivery = app, grafanaDelivery
	h.restricted, h.configPath = grafanaRestricted(app), grafanaConfigPath(app)
	h.body = tview.NewTextView().SetDynamicColors(true).SetWrap(false).SetScrollable(true)
	h.keys = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	h.Flex = tview.NewFlex().SetDirection(tview.FlexRow).AddItem(h.body, 0, 1, true).AddItem(h.keys, 1, 0, false)
	h.SetBorder(true).SetBorderPadding(0, 0, 1, 1)
	title := " " + grafanaKeyLabel + " "
	if h.problem != "" {
		title = " Grafana unavailable "
	}
	h.SetTitle(title)
	h.applyStyles()
	if h.deliverNow && h.link() != nil && h.link().URL != "" {
		h.outcome = h.delivery.deliver(h.link().URL, h.restricted)
	}
	h.previous = app.GetInputCapture()
	app.Content.AddPage(grafanaDialogPage, h, false, true)
	app.SetInputCapture(h.keyboard)
	app.SetFocus(h)
}

func (h *grafanaHandoff) applyStyles() {
	p, d := h.app.Styles.Semantic(), h.app.Styles.Dialog()
	h.SetBackgroundColor(d.BgColor.Color())
	for _, item := range []*tview.TextView{h.body, h.keys} {
		item.SetBackgroundColor(d.BgColor.Color())
		item.SetTextColor(p.Text.Color())
	}
	h.SetBorderColor(p.Focus.Color())
	h.SetTitleColor(p.Focus.Color())
}

func (h *grafanaHandoff) link() *grafanaLink {
	if h.current < 0 || h.current >= len(h.links) {
		return nil
	}
	return &h.links[h.current]
}

func (h *grafanaHandoff) close() {
	h.app.SetInputCapture(h.previous)
	h.app.Content.RemovePage(grafanaDialogPage)
	if top := h.app.Content.Top(); top != nil {
		h.app.SetFocus(top)
	}
}

func (h *grafanaHandoff) byName(name string) int {
	for i := range h.links {
		if h.links[i].Name == name {
			return i
		}
	}
	return -1
}

// choose switches to another link of the same target and delivers it.
func (h *grafanaHandoff) choose(name string) {
	index := h.byName(name)
	if index < 0 || len(h.links) < 2 {
		return
	}
	h.current, h.outcome = index, deliveryOutcome{}
	if link := h.link(); link.URL != "" {
		h.outcome = h.delivery.deliver(link.URL, h.restricted)
	}
	h.refresh()
}

func (h *grafanaHandoff) refresh() {
	h.width = 0
	h.body.ScrollToBeginning()
}

func (h *grafanaHandoff) keyboard(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyEscape, tcell.KeyEnter:
		h.close()
		return nil
	case tcell.KeyCtrlC:
		h.close()
		if h.previous != nil {
			return h.previous(event)
		}
		return event
	case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn:
		h.body.InputHandler()(event, func(tview.Primitive) {})
		return nil
	case tcell.KeyRune:
	default:
		return nil
	}
	link := h.link()
	switch event.Rune() {
	case 'q':
		h.close()
	case 'o':
		if link != nil && link.URL != "" {
			opened, line := h.delivery.open(link.URL, h.restricted)
			h.outcome = deliveryOutcome{Opened: opened, Lines: []string{line}}
			h.refresh()
		}
	case 'c':
		if link != nil && link.URL != "" {
			copied, line := h.delivery.copy(link.URL)
			h.outcome = deliveryOutcome{Copied: copied, Lines: []string{line}}
			h.refresh()
		}
	case 'y':
		if link != nil && link.Snippet != "" {
			copied, line := h.delivery.copy(link.Snippet + "\n")
			// Say what went to the clipboard: after y it is not the link,
			// whichever clipboard took it.
			if copied {
				line = "YAML snippet, not the link: " + line
			}
			h.outcome = deliveryOutcome{Copied: copied, Lines: []string{line}}
			h.refresh()
		}
	case 'd':
		h.choose("Dashboard")
	case 'e':
		h.choose("Explore metrics")
	case 'l':
		h.choose("Explore logs")
	case 'a':
		if h.offerList && h.target != nil {
			h.close()
			h.app.openDashboards(h.target, h.origin, "", "")
		}
	case 's':
		if h.problem != "" {
			h.close()
			NewCommand(h.app).sourcesCommand()
		}
	}
	return nil
}

// handoffTag renders a palette color as a tview tag; an unset color resets.
func handoffTag(c config.Color, bold bool) string {
	return timelineTag(c, bold)
}

// hardWrap splits a link into rows of at most width cells. A link has no
// spaces, so it is cut exactly and never indented: selecting the rows in a
// terminal yields the link again.
func hardWrap(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	runes := []rune(text)
	for len(runes) > width {
		out = append(out, string(runes[:width]))
		runes = runes[width:]
	}
	return append(out, string(runes))
}

// wrapText word-wraps escaped text and cuts any row a single long word
// left wider than width, so no row is ever clipped by the dialog's edge.
func wrapText(text string, width int) []string {
	var out []string
	for _, row := range tview.WordWrap(tview.Escape(text), width) {
		if runewidth.StringWidth(row) <= width {
			out = append(out, row)
			continue
		}
		out = append(out, hardWrap(row, width)...)
	}
	return out
}

// labelled word-wraps text under a fixed-width label.
func labelled(label, text string, width int) []string {
	const indent = 8
	rows := wrapText(text, max(width-indent, 10))
	out := make([]string, 0, len(rows))
	for i, row := range rows {
		prefix := strings.Repeat(" ", indent)
		if i == 0 {
			prefix = label + strings.Repeat(" ", max(indent-len(label), 1))
		}
		out = append(out, prefix+row)
	}
	return out
}

// text renders the dialog body and its key line for an inner width in
// cells. The key line is separate so it stays visible when the body scrolls.
func (h *grafanaHandoff) text(width int) (body []string, keys [2]string) {
	p := h.app.Styles.Semantic()
	reset := "[-:-:-]"
	plain := func(text string, color config.Color, bold bool) []string {
		var out []string
		for _, row := range wrapText(text, width) {
			out = append(out, handoffTag(color, bold)+row+reset)
		}
		return out
	}
	if h.problem != "" {
		out := plain(h.problem, p.Warning, false)
		out = append(out, "")
		out = append(out, plain("Nothing was contacted.", p.Text, false)...)
		return out, [2]string{"s open :sources · Esc close", "s :sources · Esc"}
	}
	var out []string
	if h.target != nil {
		out = append(out, plain(h.target.Label, p.Focus, true)...)
		if h.target.Note != "" {
			out = append(out, plain("("+h.target.Note+")", p.Muted, false)...)
		}
	}
	link := h.link()
	if link == nil {
		return out, [2]string{"Esc close", "Esc"}
	}
	what := link.Name
	if link.Detail != "" {
		what += " · " + link.Detail
	}
	out = append(out, labelled("Link", what, width)...)
	if link.Fills != "" {
		out = append(out, labelled("Fills", link.Fills, width)...)
	}
	if h.target != nil && link.URL != "" {
		out = append(out, labelled("Window", h.target.Window.String(), width)...)
	}
	if link.URL == "" {
		out = append(out, "")
		out = append(out, plain(link.Unavailable, p.Warning, false)...)
	} else {
		color := p.Warning
		if h.outcome.Opened || h.outcome.Copied {
			color = p.Healthy
		}
		for i, line := range h.outcome.Lines {
			label := ""
			if i == 0 {
				label = "Result"
			}
			rows := labelled(label, line, width)
			if i == 0 {
				for j := range rows {
					rows[j] = handoffTag(color, false) + rows[j] + reset
				}
			}
			out = append(out, rows...)
		}
		out = append(out, "")
		for _, row := range hardWrap(link.URL, width) {
			out = append(out, handoffTag(p.Focus, false)+tview.Escape(row)+reset)
		}
	}
	if link.Snippet != "" {
		out = append(out, "")
		out = append(out, plain("Not declared, so no variable is filled.", p.Text, false)...)
		out = append(out, plain("To pin it, add this to the context config:", p.Text, false)...)
		for _, row := range hardWrap(h.configPath, width) {
			out = append(out, tview.Escape(row))
		}
		for _, row := range strings.Split(link.Snippet, "\n") {
			// A row wider than the dialog (a 64-character UID at 60
			// columns) continues on the next row; it is never clipped.
			for _, part := range hardWrap("  "+row, width) {
				out = append(out, tview.Escape(part))
			}
		}
		out = append(out, plain("The variable names are the usual ones; check the dashboard's own. Nothing is written for you.", p.Muted, false)...)
	}
	for form, short := range []bool{false, true} {
		var parts []string
		word := func(long, brief string) string {
			if short {
				return brief
			}
			return long
		}
		if link.URL != "" {
			parts = append(parts, "o open", "c copy")
		}
		if link.Snippet != "" {
			parts = append(parts, word("y copy YAML", "y YAML"))
		}
		for _, other := range []struct{ key, name, long, brief string }{{"d", "Dashboard", "dashboard", "dash"},
			{"e", "Explore metrics", "Explore metrics", "metrics"}, {"l", "Explore logs", "Explore logs", "logs"}} {
			if index := h.byName(other.name); index >= 0 && index != h.current && len(h.links) > 1 {
				parts = append(parts, other.key+" "+word(other.long, other.brief))
			}
		}
		if h.offerList && h.target != nil {
			parts = append(parts, word("a all dashboards", "a all"))
		}
		parts = append(parts, word("Esc close", "Esc"))
		keys[form] = strings.Join(parts, " · ")
	}
	return out, keys
}

// Draw centers the dialog, sized to its text and never past the screen.
func (h *grafanaHandoff) Draw(screen tcell.Screen) {
	screenWidth, screenHeight := screen.Size()
	width := max(min(screenWidth-2, grafanaDialogMax), 20)
	inner := width - 4
	if inner != h.width {
		h.width = inner
		rows, keys := h.text(inner)
		h.lines, h.keyLine = len(rows), keys
		h.body.SetText(strings.Join(rows, "\n"))
	}
	// Border, body, one blank row, key line.
	want := h.lines + 4
	height := max(min(want, screenHeight-2), 5)
	prefix := ""
	if want > height {
		prefix = "Up/Down scroll · "
	}
	keys := prefix + h.keyLine[1]
	if long := prefix + h.keyLine[0]; runewidth.StringWidth(long) <= inner {
		keys = long
	} else if runewidth.StringWidth(keys) > inner {
		// Tightest form: every key stays named instead of being cut off.
		keys = strings.ReplaceAll(strings.Replace(keys, "Up/Down scroll", "Up/Dn", 1), " · ", "  ")
	}
	p := h.app.Styles.Semantic()
	h.keys.SetText(handoffTag(p.Muted, false) + tview.Escape(ui.Truncate(keys, inner)) + "[-:-:-]")
	h.Flex.ResizeItem(h.keys, 1, 0)
	h.SetRect((screenWidth-width)/2, max((screenHeight-height)/2, 0), width, height)
	h.Flex.Draw(screen)
}

// searchLink is the link of a dashboard that Grafana returned but the
// context does not declare: its variables are unknown, so none is filled.
func searchLink(g *observability.GrafanaSource, hit *observability.DashboardHit, t *grafanaTarget) grafanaLink {
	link := grafanaLink{Name: "Dashboard", Detail: safeProviderText(observability.Clean(hit.UID, 64) + " · " + hit.Title)}
	if t.Kind != "" {
		link.Snippet = observability.PinSnippet(t.Kind, hit.UID)
	}
	u, err := g.DashboardUIDURL(hit.UID, t.Window.linkRange())
	if err != nil {
		link.Unavailable = observability.Clean(err.Error(), 200)
		return link
	}
	link.URL = u
	return link
}

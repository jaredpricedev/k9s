// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

// Draw keeps the resource list primary. Only the selected chart is displayed
// when there is room; a resize never shrinks every resource into tiny tiles.
func (p *Pulse) Draw(screen tcell.Screen) {
	p.Grid.Box.Draw(screen)
	x, y, width, height := p.GetInnerRect()
	if ui.DrawTaskSizeNotice(screen, p.Grid.Box) {
		return
	}
	if len(p.chartGVRs) == 0 {
		return
	}
	p.selectedIndex = min(max(p.selectedIndex, 0), len(p.chartGVRs)-1)
	listWidth := width
	wide := width >= 110 && height >= 20
	if wide {
		listWidth = width / 2
	}

	namespace := p.model.GetNamespace()
	if namespace == "" {
		namespace = "all"
	}
	pulsePrint(screen, "Pulses / "+namespace, x, y, width, p.app.Styles.Semantic().Focus.Color())
	pulsePrint(screen, p.coverageSummary(time.Now()), x, y+1, width, p.app.Styles.Semantic().Unknown.Color())
	nameWidth := listWidth - 27
	pulsePrint(screen, "Resource", x+2, y+3, nameWidth, p.app.Styles.Semantic().Muted.Color())
	pulsePrint(screen, "Read state", x+2+nameWidth, y+3, 14, p.app.Styles.Semantic().Muted.Color())
	pulsePrint(screen, "total/fault", x+listWidth-11, y+3, 11, p.app.Styles.Semantic().Muted.Color())

	rows := height - 10
	start := max(0, p.selectedIndex-rows+1)
	start = min(start, max(0, len(p.chartGVRs)-rows))
	rangeText := fmt.Sprintf("Showing %d-%d/%d | selected %d", start+1, min(start+rows, len(p.chartGVRs)), len(p.chartGVRs), p.selectedIndex+1)
	pulsePrint(screen, rangeText, x, y+2, listWidth, p.app.Styles.Semantic().Muted.Color())
	for row := 0; row < rows && start+row < len(p.chartGVRs); row++ {
		index := start + row
		gvr := p.chartGVRs[index]
		state, counts := p.resourceRow(gvr, time.Now())
		color := p.app.Styles.Semantic().Muted.Color()
		if state == string(model.HealthDenied) || state == string(model.HealthStale) || state == string(model.HealthUnavailable) {
			color = p.app.Styles.Semantic().Unknown.Color()
		}
		if point := p.healthPoints[gvr].At(time.Now()); point.HasValue() && point.Faults > 0 {
			color = p.app.Styles.Semantic().Failure.Color()
		}
		marker := "  "
		if index == p.selectedIndex {
			marker, color = "> ", p.app.Styles.Semantic().Progress.Color()
		}
		atY := y + 4 + row
		pulsePrint(screen, marker, x, atY, 2, color)
		pulsePrint(screen, pulseResourceName(gvr), x+2, atY, nameWidth-1, color)
		pulsePrint(screen, state, x+2+nameWidth, atY, 14, color)
		tview.Print(screen, tview.Escape(ui.Truncate(counts, 11)), x+listWidth-11, atY, 11, tview.AlignRight, color)
	}
	selected := p.chartGVRs[p.selectedIndex]
	p.drawSelectedDetail(screen, selected, x, y+height-5, width)
	pulsePrint(screen, "Up/Down select  Enter browse  s source  m metrics", x, y+height-1, width, p.app.Styles.Semantic().Progress.Color())
	if wide {
		chartX, chartWidth := x+listWidth+2, width-listWidth-2
		pulsePrint(screen, "Selected chart: "+pulseResourceName(selected), chartX, y+3, chartWidth, p.app.Styles.Semantic().Focus.Color())
		chart := p.charts[selected]
		if selected != client.CpuGVR && selected != client.MemGVR {
			point := p.healthPoints[selected].At(time.Now())
			p.updateHealthChart(&point)
		}
		chart.SetRect(chartX, y+5, chartWidth, height-11)
		chart.Draw(screen)
	}
}

func pulsePrint(screen tcell.Screen, text string, x, y, width int, color tcell.Color) {
	if width <= 0 {
		return
	}
	tview.Print(screen, tview.Escape(ui.Truncate(text, width)), x, y, width, tview.AlignLeft, color)
}

func pulseResourceName(gvr *client.GVR) string {
	switch gvr {
	case client.CpuGVR:
		return "CPU metrics"
	case client.MemGVR:
		return "Memory metrics"
	default:
		return gvr.R()
	}
}

func (p *Pulse) resourceRow(gvr *client.GVR, now time.Time) (state, counts string) {
	if gvr == client.CpuGVR || gvr == client.MemGVR {
		sample := p.metricsSample.At(now)
		state = string(sample.State)
		if state == "" {
			state = string(model.HealthLoading)
		}
		return state, "m: source"
	}
	pt := p.healthPoints[gvr].At(now)
	state, counts = string(pt.State), "--"
	if state == "" {
		state = string(model.HealthLoading)
	}
	if pt.HasValue() {
		counts = fmt.Sprintf("%d/%d", pt.Total, pt.Faults)
	}
	return state, counts
}

func (p *Pulse) coverageSummary(now time.Time) string {
	counts := make(map[model.HealthState]int)
	total := len(p.chartGVRs) - 2
	for _, gvr := range p.chartGVRs[:total] {
		state := p.healthPoints[gvr].At(now).State
		if state == "" {
			state = model.HealthLoading
		}
		counts[state]++
	}
	readable := counts[model.HealthAvailable] + counts[model.HealthEmpty]
	parts := []string{fmt.Sprintf("Coverage %d/%d read", readable, total)}
	if readable < total {
		parts = append(parts, "partial")
	}
	for _, state := range []model.HealthState{model.HealthDenied, model.HealthUnavailable, model.HealthStale, model.HealthAbsent, model.HealthLoading} {
		if counts[state] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[state], state))
		}
	}
	return strings.Join(parts, " | ")
}

func (p *Pulse) drawSelectedDetail(screen tcell.Screen, gvr *client.GVR, x, y, width int) {
	pulsePrint(screen, "Selected: "+pulseResourceName(gvr), x, y, width, p.app.Styles.Semantic().Focus.Color())
	if gvr == client.CpuGVR || gvr == client.MemGVR {
		pulsePrint(screen, "Sample: "+ui.MetricDescription(p.metricsSample), x, y+1, width, p.app.Styles.Semantic().Muted.Color())
		pulsePrint(screen, "Value: "+pulseMetricText(&p.metricsPoint, gvr == client.MemGVR), x, y+2, width, p.app.Styles.Semantic().Text.Color())
		pulsePrint(screen, "m: complete source and availability", x, y+3, width, p.app.Styles.Semantic().Muted.Color())
		return
	}
	pt := p.healthPoints[gvr].At(time.Now())
	state, counts := p.resourceRow(gvr, time.Now())
	status := "State: " + state
	if pt.Failure != "" {
		status += " / " + string(pt.Failure)
	}
	if pt.HasValue() {
		status += " | total/fault " + counts
	}
	pulsePrint(screen, status, x, y+1, width, p.app.Styles.Semantic().Text.Color())
	source := pt.Source
	if source == "" {
		source = pulseNotCollected
	}
	readTime := pulseNotCollected
	if !pt.ObservedAt.IsZero() {
		readTime = pt.ObservedAt.UTC().Format("15:04:05 UTC")
	}
	pulsePrint(screen, "Source: "+source+" | read "+readTime, x, y+2, width, p.app.Styles.Semantic().Muted.Color())
	next := "s: " + pulseNextCheck(&pt)
	if pt.Message != "" {
		next = "s: " + pt.Message
	}
	pulsePrint(screen, next, x, y+3, width, p.app.Styles.Semantic().Unknown.Color())
}

// Focus retains the selected resource across Back and terminal resize.
func (p *Pulse) Focus(delegate func(tview.Primitive)) {
	if len(p.chartGVRs) > 0 {
		delegate(p.charts[p.chartGVRs[p.selectedIndex]])
	}
}

// HasFocus follows the selected chart directly. Grid visibility is populated by
// Grid.Draw, which this responsive renderer intentionally does not call.
func (p *Pulse) HasFocus() bool {
	return len(p.chartGVRs) > 0 && p.charts[p.chartGVRs[p.selectedIndex]].HasFocus()
}

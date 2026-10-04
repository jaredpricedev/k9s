// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/render"
	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/k9s/internal/tchart"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	cpuFmt     = " %s [%s::b]%s[white::-]([%s::]%sm[white::]/[%s::]%sm[-::])"
	memFmt     = " %s [%s::b]%s[white::-]([%s::]%sMi[white::]/[%s::]%sMi[-::])"
	pulseTitle = "Pulses"
	NSTitleFmt = "[fg:bg:b] %s([hilite:bg:b]%s[fg:bg:-])[fg:bg:-] "
	dirLeft    = 1
	dirRight   = -dirLeft
	dirDown    = 4
	dirUp      = -dirDown
	grayC      = "gray"
)

var corpusGVRs = append(model.PulseGVRs, client.CpuGVR, client.MemGVR)

type Charts map[*client.GVR]Graphable

// Graphable represents a graphic component.
type Graphable interface {
	tview.Primitive

	// ID returns the graph id.
	ID() string

	// Add adds a metric
	Add(ok, fault int)

	AddMetric(time.Time, float64)

	// SetLegend sets the graph legend
	SetLegend(string)

	SetColorIndex(int)

	SetMax(float64)
	GetMax() float64

	// SetSeriesColors sets charts series colors.
	SetSeriesColors(...tcell.Color)

	// GetSeriesColorNames returns the series color names.
	GetSeriesColorNames() []string

	// SetFocusColorNames sets the focus color names.
	SetFocusColorNames(fg, bg string)

	// SetBackgroundColor sets chart bg color.
	SetBackgroundColor(tcell.Color)

	SetBorderColor(tcell.Color) *tview.Box

	// IsDial returns true if chart is a dial
	IsDial() bool
}

// Pulse represents a command health view.
type Pulse struct {
	*tview.Grid

	app           *App
	gvr           *client.GVR
	model         *model.Pulse
	cancelFn      context.CancelFunc
	actions       *ui.KeyActions
	charts        Charts
	selectedIndex int
	chartGVRs     client.GVRs
	metricsSample client.MetricSample
	metricsPoint  dao.Point
	generation    uint64
	healthPoints  map[*client.GVR]model.HealthPoint
}

// NewPulse returns a new alias view.
func NewPulse(gvr *client.GVR) ResourceViewer {
	return &Pulse{
		Grid:    tview.NewGrid(),
		model:   model.NewPulse(gvr),
		actions: ui.NewKeyActions(),
		gvr:     gvr,
	}
}

// Init initializes the view.
func (p *Pulse) Init(ctx context.Context) error {
	p.SetBorder(false)
	p.SetGap(0, 0)
	p.SetBorderPadding(0, 0, 0, 0)
	var err error
	if p.app, err = extractApp(ctx); err != nil {
		return err
	}

	ns := p.app.Config.ActiveNamespace()
	frame := p.app.Styles.Frame()
	p.SetTitle(ui.SkinTitle(fmt.Sprintf(NSTitleFmt, pulseTitle, ns), &frame))

	// Namespaced Pulse includes Services and Events so failures in those reads
	// remain inspectable. Nodes and Namespaces belong to the all-scope view.
	index, chartRow := 2, 8
	if client.IsAllNamespace(ns) {
		index, chartRow = 0, 8
	}
	p.chartGVRs = corpusGVRs[index:]

	p.charts = make(Charts, len(p.chartGVRs))
	p.healthPoints = make(map[*client.GVR]model.HealthPoint, len(p.chartGVRs)-2)
	var x, y, col int
	for _, gvr := range p.chartGVRs[:len(p.chartGVRs)-2] {
		p.healthPoints[gvr] = model.HealthPoint{GVR: gvr, Namespace: ns, State: model.HealthLoading}
		p.charts[gvr] = p.makeGA(image.Point{X: x, Y: y}, image.Point{X: 2, Y: 2}, gvr)
		p.charts[gvr].(*tchart.Gauge).SetStatus("loading")
		col, y = col+1, y+2
		if y > 6 {
			y = 0
		}
		if col >= 4 {
			col, x = 0, x+2
		}
	}
	// Collection workers report availability. Discovery never blocks this UI callback.
	p.charts[client.CpuGVR] = p.makeSP(image.Point{X: chartRow, Y: 0}, image.Point{X: 2, Y: 4}, client.CpuGVR, "c")
	p.charts[client.MemGVR] = p.makeSP(image.Point{X: chartRow, Y: 4}, image.Point{X: 2, Y: 4}, client.MemGVR, "Gi")
	p.charts[client.CpuGVR].SetLegend(" CPU N/A (waiting for a sample) ")
	p.charts[client.MemGVR].SetLegend(" MEM N/A (waiting for a sample) ")
	if index != 0 {
		p.selectedIndex = p.findIndex(p.charts[client.PodGVR])
	}
	p.GetItem(p.selectedIndex).Focus = true
	p.app.SetFocus(p.charts[p.chartGVRs[p.selectedIndex]])

	p.bindKeys()
	p.app.Styles.AddListener(p)
	p.StylesChanged(p.app.Styles)
	p.model.SetNamespace(ns)

	return nil
}

// InCmdMode checks if prompt is active.
func (*Pulse) InCmdMode() bool {
	return false
}

func (*Pulse) SetCommand(*cmd.Interpreter)            {}
func (*Pulse) SetFilter(string, bool)                 {}
func (*Pulse) SetLabelSelector(labels.Selector, bool) {}

// StylesChanged notifies the skin changed.
func (p *Pulse) StylesChanged(s *config.Styles) {
	p.SetBackgroundColor(s.Charts().BgColor.Color())
	for _, c := range p.charts {
		c.SetFocusColorNames(s.Charts().FocusFgColor.String(), s.Charts().FocusBgColor.String())
		if c.IsDial() {
			c.SetBackgroundColor(s.Charts().DialBgColor.Color())
			c.SetSeriesColors(s.Charts().DefaultDialColors.Colors()...)
		} else {
			c.SetBackgroundColor(s.Charts().ChartBgColor.Color())
			c.SetSeriesColors(s.Charts().DefaultChartColors.Colors()...)
		}
		if ss, ok := s.Charts().ResourceColors[c.ID()]; ok {
			c.SetSeriesColors(ss.Colors()...)
		}
	}
}

// SeriesChanged update cluster time series.
func (p *Pulse) SeriesChanged(tt dao.TimeSeries) {
	if len(tt) == 0 {
		return
	}

	cpu, ok := p.charts[client.CpuGVR]
	if !ok {
		return
	}
	mem, ok := p.charts[client.MemGVR]
	if !ok {
		return
	}

	for i := range tt {
		t := tt[i]
		if !t.Sample.Fresh() {
			continue
		}
		if t.Sample.Source == client.PodMetricsSource {
			cpu.SetMax(float64(t.Value.CurrentCPU))
			mem.SetMax(float64(t.Value.CurrentMEM))
		} else {
			cpu.SetMax(float64(t.Value.AllocatableCPU))
			mem.SetMax(float64(t.Value.AllocatableMEM))
		}
		cpu.AddMetric(t.Time, float64(t.Value.CurrentCPU))
		mem.AddMetric(t.Time, float64(t.Value.CurrentMEM))
	}

	last := tt[len(tt)-1]
	p.metricsSample = last.Sample
	p.metricsPoint = last
	if !last.Sample.Fresh() {
		unknown := p.app.Styles.Semantic().Unknown.Color()
		cpu.SetBorderColor(unknown)
		mem.SetBorderColor(unknown)
		cpu.SetLegend(" CPU " + pulseMetricText(&last, false) + " ")
		mem.SetLegend(" MEM " + pulseMetricText(&last, true) + " ")
		return
	}
	cpu.SetBorderColor(p.app.Styles.Semantic().Muted.Color())
	mem.SetBorderColor(p.app.Styles.Semantic().Muted.Color())
	if last.Sample.Source == client.PodMetricsSource {
		// Namespace totals have no allocatable capacity denominator. A changing
		// usage total cannot establish a workload health threshold.
		cpu.SetColorIndex(0)
		mem.SetColorIndex(0)
		cpu.SetSeriesColors(p.app.Styles.Semantic().Progress.Color())
		mem.SetSeriesColors(p.app.Styles.Semantic().Progress.Color())
		cpu.SetLegend(" CPU " + pulseMetricText(&last, false) + " (usage) ")
		mem.SetLegend(" MEM " + pulseMetricText(&last, true) + " (usage) ")
		return
	}
	perc := last.Sample.Values.PercCPU
	index := int(p.app.Config.K9s.Thresholds.LevelFor("cpu", perc))
	cpu.SetColorIndex(int(p.app.Config.K9s.Thresholds.LevelFor("cpu", perc)))
	nn := cpu.GetSeriesColorNames()
	if last.Value.CurrentCPU == 0 {
		nn[0] = grayC
	}
	if last.Value.AllocatableCPU == 0 {
		nn[1] = grayC
	}
	cpu.SetLegend(fmt.Sprintf(cpuFmt,
		cases.Title(language.English).String(client.CpuGVR.R()),
		p.app.Config.K9s.Thresholds.SeverityColor("cpu", perc),
		render.PrintPerc(perc),
		nn[index],
		render.AsThousands(last.Value.CurrentCPU),
		"white",
		render.AsThousands(int64(cpu.GetMax())),
	))

	nn = mem.GetSeriesColorNames()
	if last.Value.CurrentMEM == 0 {
		nn[0] = grayC
	}
	if last.Value.AllocatableMEM == 0 {
		nn[1] = grayC
	}
	perc = last.Sample.Values.PercMEM
	index = int(p.app.Config.K9s.Thresholds.LevelFor("memory", perc))
	mem.SetColorIndex(index)
	mem.SetLegend(fmt.Sprintf(memFmt,
		cases.Title(language.English).String(client.MemGVR.R()),
		p.app.Config.K9s.Thresholds.SeverityColor("memory", perc),
		render.PrintPerc(perc),
		nn[index],
		render.AsThousands(last.Value.CurrentMEM),
		"white",
		render.AsThousands(int64(mem.GetMax())),
	))
}

func pulseMetricText(point *dao.Point, memory bool) string {
	sample := point.Sample.At(time.Now())
	if sample.Source != client.PodMetricsSource || !sample.HasValue() {
		return ui.MetricPercent(client.MetricSample{}, sample, memory)
	}
	value := fmt.Sprintf("%dm", point.Value.CurrentCPU)
	if memory {
		value = fmt.Sprintf("%dMi", point.Value.CurrentMEM)
	}
	if sample.State == client.MetricsStale {
		state := "stale"
		if sample.Failure != "" {
			state += ": " + string(sample.Failure)
		}
		value += " (" + state + ")"
	}
	return value
}

// PulseChanged notifies the model data changed.
func (p *Pulse) PulseChanged(pt model.HealthPoint) {
	if p.model != nil && pt.Namespace != p.model.GetNamespace() {
		return
	}
	_, ok := p.charts[pt.GVR]
	if !ok {
		return
	}

	if p.healthPoints == nil {
		p.healthPoints = make(map[*client.GVR]model.HealthPoint)
	}
	if previous := p.healthPoints[pt.GVR]; previous.HasValue() && !pt.HasValue() {
		pt.Total, pt.Faults, pt.ObservedAt, pt.Source = previous.Total, previous.Faults, previous.ObservedAt, previous.Source
		pt.State, pt.Failure = model.HealthStale, pt.State
	}
	p.healthPoints[pt.GVR] = pt
	p.updateHealthChart(pt)
	p.updateCoverageTitle()
}

func (p *Pulse) updateHealthChart(pt model.HealthPoint) {
	v := p.charts[pt.GVR]
	if gauge, ok := v.(*tchart.Gauge); ok {
		status := ""
		if !pt.HasValue() {
			status = string(pt.State)
			if status == "" {
				status = "loading"
			}
		}
		gauge.SetStatus(status)
	}

	legend := cases.Title(language.English).String(pt.GVR.R())
	if pt.State == model.HealthStale {
		legend += " (stale)"
	}
	v.SetLegend(legend)
	colors := p.app.Styles.Charts().DefaultDialColors.Colors()
	if custom, ok := p.app.Styles.Charts().ResourceColors[v.ID()]; ok {
		colors = custom.Colors()
	}
	if pt.State != model.HealthAvailable && pt.State != model.HealthEmpty {
		unknown := p.app.Styles.Semantic().Unknown.Color()
		colors = []tcell.Color{unknown, unknown}
	}
	v.SetSeriesColors(colors...)
	if pt.State != model.HealthAvailable && pt.State != model.HealthEmpty {
		v.SetBorderColor(p.app.Styles.Semantic().Unknown.Color())
	} else if pt.Faults > 0 {
		v.SetBorderColor(p.app.Styles.Semantic().Failure.Color())
	} else {
		v.SetBorderColor(p.app.Styles.Semantic().Healthy.Color())
	}
	if pt.HasValue() {
		v.Add(pt.Total, pt.Faults)
	}
}

func (p *Pulse) updateCoverageTitle() {
	var readable int
	for _, gvr := range p.chartGVRs[:len(p.chartGVRs)-2] {
		pt := p.healthPoints[gvr].At(time.Now())
		if pt.State == model.HealthAvailable || pt.State == model.HealthEmpty {
			readable++
		}
	}
	frame := p.app.Styles.Frame()
	p.SetTitle(ui.SkinTitle(fmt.Sprintf(" Pulses(%s) | %d/%d readable ", p.model.GetNamespace(), readable, len(p.chartGVRs)-2), &frame))
}

// PulseFailed notifies the load failed.
func (p *Pulse) PulseFailed(err error) {
	p.app.Flash().Err(err)
}

func (p *Pulse) bindKeys() {
	p.actions.Add(tcell.KeyCtrlO, ui.NewKeyAction("Actions", p.app.actionsCmd, true))
	p.actions.Merge(ui.NewKeyActionsFromMap(ui.KeyMap{
		ui.KeyS:          ui.NewKeyAction("Health source", p.healthSourceCmd, true),
		ui.KeyM:          ui.NewKeyAction("Metric source", p.metricSourceCmd, true),
		tcell.KeyEnter:   ui.NewKeyAction("Goto", p.enterCmd, true),
		tcell.KeyTab:     ui.NewKeyAction("Next", p.nextFocusCmd(dirLeft), true),
		tcell.KeyBacktab: ui.NewKeyAction("Prev", p.nextFocusCmd(dirRight), true),
		tcell.KeyDown:    ui.NewKeyAction("Down", p.nextFocusCmd(dirDown), false),
		tcell.KeyUp:      ui.NewKeyAction("Up", p.nextFocusCmd(dirUp), false),
		tcell.KeyRight:   ui.NewKeyAction("Next", p.nextFocusCmd(dirLeft), false),
		tcell.KeyLeft:    ui.NewKeyAction("Prev", p.nextFocusCmd(dirRight), false),
		ui.KeyH:          ui.NewKeyAction("Prev", p.nextFocusCmd(dirRight), false),
		ui.KeyJ:          ui.NewKeyAction("Down", p.nextFocusCmd(dirDown), false),
		ui.KeyK:          ui.NewKeyAction("Up", p.nextFocusCmd(dirUp), false),
		ui.KeyL:          ui.NewKeyAction("Next", p.nextFocusCmd(dirLeft), false),
	}))
}

func (p *Pulse) healthSourceCmd(*tcell.EventKey) *tcell.EventKey {
	graph, ok := p.app.GetFocus().(Graphable)
	if !ok {
		return nil
	}
	gvr := p.chartGVRs[p.findIndex(graph)]
	if gvr == client.CpuGVR || gvr == client.MemGVR {
		return p.metricSourceCmd(nil)
	}
	pt := p.healthPoints[gvr].At(time.Now())
	text := fmt.Sprintf("Context: %s\nNamespace: %s\nResource: %s\nState: %s\nSource: %s\nRead at: %s\nLast attempt: %s\n", p.app.Config.ActiveContextName(), p.model.GetNamespace(), gvr, pt.State, pt.Source, pulseTime(pt.ObservedAt), pulseTime(pt.CheckedAt))
	if pt.HasValue() {
		text += fmt.Sprintf("\nRetained total: %d\nRetained faults: %d\n", pt.Total, pt.Faults)
	}
	if pt.Failure != "" {
		text += "Latest collection: " + string(pt.Failure) + "\n"
	}
	if pt.Message != "" {
		text += "\n" + pt.Message + "\n"
	}
	text += "\n" + pulseNextCheck(pt)
	view := NewDetails(p.app, "Pulse health source", "observation", contentInspection, true).Update(text)
	if err := p.app.inject(view, false); err != nil {
		p.app.Flash().Err(err)
	}
	return nil
}

func pulseTime(at time.Time) string {
	if at.IsZero() {
		return "not collected"
	}
	return at.UTC().Format(time.RFC3339)
}

func pulseNextCheck(pt model.HealthPoint) string {
	state := pt.State
	if state == model.HealthStale {
		state = pt.Failure
	}
	switch state {
	case model.HealthDenied:
		return "Check list permission for this resource and namespace; Enter opens the resource browser."
	case model.HealthAbsent:
		return "Confirm the resource API is served by this cluster; Enter opens the resource browser."
	case model.HealthUnavailable:
		return "Check the cluster connection and API availability; collection retries every 10s."
	case model.HealthLoading, "":
		return "Waiting for the initial read; collection retries every 10s."
	default:
		return "Counts describe a read-only health check of the resource snapshot, not an application SLO. Enter opens the resource browser."
	}
}

func (p *Pulse) metricSourceCmd(*tcell.EventKey) *tcell.EventKey {
	namespace := p.model.GetNamespace()
	if client.IsAllNamespaces(namespace) || namespace == "" {
		namespace = "all"
	}
	text := fmt.Sprintf("Context: %s\nNamespace: %s\nMetrics: %s\n\nCPU: %s\nMEM: %s\n\nMissing and stale samples do not enter chart "+
		"history or health thresholds.", p.app.Config.ActiveContextName(), namespace, ui.MetricDescription(p.metricsSample),
		pulseMetricText(&p.metricsPoint, false), pulseMetricText(&p.metricsPoint, true))
	if p.metricsSample.Source == client.PodMetricsSource {
		text += "\n\nNamespace metrics report usage totals; no allocatable capacity denominator is available, " +
			"so no utilization percent or health threshold is inferred."
	}
	view := NewDetails(p.app, "Pulse metric source", "observation", contentInspection, true).Update(text)
	if err := p.app.inject(view, false); err != nil {
		p.app.Flash().Err(err)
	}
	return nil
}

func (p *Pulse) keyboard(evt *tcell.EventKey) *tcell.EventKey {
	key := evt.Key()
	if key == tcell.KeyRune {
		key = tcell.Key(evt.Rune())
	}
	if a, ok := p.actions.Get(key); ok {
		return a.Action(evt)
	}

	return evt
}

func (p *Pulse) defaultContext() context.Context {
	return context.WithValue(context.Background(), internal.KeyFactory, p.app.factory)
}

func (*Pulse) Restart() {}

// Start initializes resource watch loop.
func (p *Pulse) Start() {
	p.Stop()
	generation, contextName := p.generation, p.app.Config.ActiveContextName()
	p.updateCoverageTitle()

	ctx := p.defaultContext()
	ctx, p.cancelFn = context.WithCancel(ctx)
	gaugeChan, metricsChan, err := p.model.Watch(ctx)
	if err != nil {
		slog.Error("Pulse watch failed", slogs.Error, err)
		return
	}

	go func() {
		for {
			if gaugeChan == nil && metricsChan == nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case check, ok := <-gaugeChan:
				if !ok {
					gaugeChan = nil
					continue
				}
				p.app.QueueUpdateDraw(func() {
					if p.generation == generation && p.app.Config.ActiveContextName() == contextName && p.app.Content.Top() == p {
						p.PulseChanged(check)
					}
				})
			case mx, ok := <-metricsChan:
				if !ok {
					metricsChan = nil
					continue
				}
				p.app.QueueUpdateDraw(func() {
					if p.generation == generation && p.app.Config.ActiveContextName() == contextName && p.app.Content.Top() == p {
						p.SeriesChanged(mx)
					}
				})
			}
		}
	}()
}

// Stop terminates watch loop.
func (p *Pulse) Stop() {
	p.generation++
	if p.cancelFn == nil {
		return
	}
	p.cancelFn()
	p.cancelFn = nil
}

// Refresh updates the view
func (*Pulse) Refresh() {}

// GVR returns a resource descriptor.
func (p *Pulse) GVR() *client.GVR {
	return p.gvr
}

// Name returns the component name.
func (*Pulse) Name() string {
	return pulseTitle
}

// App returns the current app handle.
func (p *Pulse) App() *App {
	return p.app
}

// SetInstance sets specific resource instance.
func (*Pulse) SetInstance(string) {}

// SetEnvFn sets the custom environment function.
func (*Pulse) SetEnvFn(EnvFunc) {}

// AddBindKeysFn sets up extra key bindings.
func (*Pulse) AddBindKeysFn(BindKeysFunc) {}

// SetContextFn sets custom context.
func (*Pulse) SetContextFn(ContextFunc) {}

func (*Pulse) GetContextFn() ContextFunc { return nil }

// GetTable return the view table if any.
func (*Pulse) GetTable() *Table {
	return nil
}

// Actions returns active menu bindings.
func (p *Pulse) Actions() *ui.KeyActions {
	return p.actions
}

// Hints returns the view hints.
func (p *Pulse) Hints() model.MenuHints {
	return p.actions.Hints()
}

// ExtraHints returns additional hints.
func (*Pulse) ExtraHints() map[string]string {
	return nil
}

func (p *Pulse) enterCmd(*tcell.EventKey) *tcell.EventKey {
	if graph, ok := p.app.GetFocus().(Graphable); ok {
		p.selectedIndex = p.findIndex(graph)
	}
	gvr := p.chartGVRs[p.selectedIndex]
	if gvr == client.CpuGVR || gvr == client.MemGVR {
		gvr = client.PodGVR
	}
	p.Stop()
	p.app.SetFocus(p.app.Main)
	p.app.gotoResource(gvr.String()+" "+p.model.GetNamespace(), "", false, true)
	return nil
}

func (p *Pulse) nextFocusCmd(direction int) func(*tcell.EventKey) *tcell.EventKey {
	return func(*tcell.EventKey) *tcell.EventKey {
		if len(p.chartGVRs) == 0 {
			return nil
		}
		if graph, ok := p.app.GetFocus().(Graphable); ok {
			p.selectedIndex = p.findIndex(graph)
		}
		step := 1
		if direction < 0 {
			step = -1
		}
		p.selectedIndex = (p.selectedIndex + step + len(p.chartGVRs)) % len(p.chartGVRs)
		p.app.SetFocus(p.charts[p.chartGVRs[p.selectedIndex]])
		return nil
	}
}

func (p *Pulse) makeSP(loc, span image.Point, gvr *client.GVR, unit string) *tchart.SparkLine {
	s := tchart.NewSparkLine(gvr.String(), unit)
	s.SetBackgroundColor(p.app.Styles.Charts().BgColor.Color())
	if cc, ok := p.app.Styles.Charts().ResourceColors[gvr.String()]; ok {
		s.SetSeriesColors(cc.Colors()...)
	} else {
		s.SetSeriesColors(p.app.Styles.Charts().DefaultChartColors.Colors()...)
	}
	s.SetLegend(fmt.Sprintf(" %s ", cases.Title(language.English).String(gvr.R())))
	s.SetInputCapture(p.keyboard)
	p.AddItem(s, loc.X, loc.Y, span.X, span.Y, 0, 0, false)

	return s
}

func (p *Pulse) makeGA(loc, span image.Point, gvr *client.GVR) *tchart.Gauge {
	g := tchart.NewGauge(gvr.String())
	g.SetBorder(false)
	g.SetBackgroundColor(p.app.Styles.Charts().BgColor.Color())
	if cc, ok := p.app.Styles.Charts().ResourceColors[gvr.String()]; ok {
		g.SetSeriesColors(cc.Colors()...)
	} else {
		g.SetSeriesColors(p.app.Styles.Charts().DefaultDialColors.Colors()...)
	}
	g.SetLegend(fmt.Sprintf(" %s ", cases.Title(language.English).String(gvr.R())))
	g.SetInputCapture(p.keyboard)
	p.AddItem(g, loc.X, loc.Y, span.X, span.Y, 0, 0, false)

	return g
}

// ----------------------------------------------------------------------------
// Helpers

func (p *Pulse) findIndex(g Graphable) int {
	for i, gvr := range p.chartGVRs {
		if gvr.String() == g.ID() {
			return i
		}
	}
	return 0
}

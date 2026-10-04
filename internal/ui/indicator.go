// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/render"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"github.com/mattn/go-runewidth"
)

// StatusIndicator keeps the destination visible independently of transient messages.
type StatusIndicator struct {
	*tview.TextView
	app       *App
	styles    *config.Styles
	permanent string
	previous  *model.ClusterMeta
	current   *model.ClusterMeta
	identity  bool
}

func NewStatusIndicator(app *App, styles *config.Styles) *StatusIndicator {
	s := &StatusIndicator{TextView: tview.NewTextView(), app: app, styles: styles}
	s.SetTextAlign(tview.AlignLeft)
	palette := styles.Semantic()
	s.SetTextColor(config.ReadableForeground(palette.Text.Color(), palette.Canvas.Color()))
	s.SetBackgroundColor(palette.Canvas.Color())
	s.SetDynamicColors(true)
	styles.AddListener(s)
	s.RefreshIdentity()
	return s
}

func (s *StatusIndicator) StylesChanged(styles *config.Styles) {
	s.styles = styles
	palette := styles.Semantic()
	s.SetBackgroundColor(palette.Canvas.Color())
	s.SetTextColor(config.ReadableForeground(palette.Text.Color(), palette.Canvas.Color()))
	if s.identity {
		s.RefreshIdentity()
	}
}

func (s *StatusIndicator) ClusterInfoUpdated(data *model.ClusterMeta) {
	s.ClusterInfoChanged(data, data)
}

func (s *StatusIndicator) ClusterInfoChanged(previous, current *model.ClusterMeta) {
	s.app.QueueUpdateDraw(func() {
		if !current.IsCurrent() {
			return
		}
		// A delayed model callback must not replace the current destination.
		if context := s.app.Config.ActiveContextName(); context != "" && current.Context != context {
			return
		}
		s.previous, s.current = previous, current
		s.RefreshIdentity()
	})
}

// RefreshIdentity reads only local configuration. It is safe on the UI thread.
func (s *StatusIndicator) RefreshIdentity() {
	s.identity = true
	_, _, width, _ := s.GetInnerRect()
	if width == 0 {
		width = 100
	}
	s.SetText(s.identityText(width))
}

func (s *StatusIndicator) Draw(screen tcell.Screen) {
	if s.identity {
		s.RefreshIdentity()
	}
	s.TextView.Draw(screen)
}

func (s *StatusIndicator) destination() (context, namespace, connection string) {
	context, namespace = s.app.Config.ActiveContextName(), s.app.Config.ActiveNamespace()
	connection = "UNKNOWN"
	if s.current.IsCurrent() && (context == "" || context == s.current.Context) {
		if context == "" {
			context = s.current.Context
		}
		if s.current.Connected {
			connection = "CONNECTED"
		} else {
			connection = "DISCONNECTED"
		}
	}
	if context == "" {
		context = "N/A"
	}
	if namespace == "" || client.IsAllNamespaces(namespace) {
		namespace = "all"
	}
	return
}

func (s *StatusIndicator) identityText(width int) string {
	context, namespace, connection := s.destination()
	production := false
	for _, name := range s.app.Config.K9s.UI.ProductionContexts {
		production = production || name == context
	}
	prefix := ModeLabel(s.app.Config.IsReadOnly()) + " " + connection + " "
	if production {
		prefix += "PRODUCTION "
	}
	metrics := ""
	if s.current.IsCurrent() && s.current.Context == context && width >= 80 {
		previous := s.previous
		if previous == nil {
			previous = s.current
		}
		metrics = fmt.Sprintf("  CPU %s  MEM %s", MetricPercent(previous.Metrics, s.current.Metrics, false), MetricPercent(previous.Metrics, s.current.Metrics, true))
		if runewidth.StringWidth(prefix)+len("ctx:1234 ns:1234 F2:destination")+tview.TaggedStringWidth(metrics) > width {
			state := s.current.Metrics.At(time.Now()).State
			if state == "" {
				state = client.MetricsUnavailable
			}
			metrics = "  metrics:" + string(state)
		}
	}
	// Reserve mode and connection first. F2 reveals the complete destination.
	budget := max(2, width-runewidth.StringWidth(prefix)-len("ctx: ns: F2:destination")-tview.TaggedStringWidth(metrics))
	namespaceWidth := min(runewidth.StringWidth(namespace), max(1, budget/3))
	contextWidth := max(1, budget-namespaceWidth)
	identity := prefix + "ctx:" + Truncate(context, contextWidth) + " ns:" + Truncate(namespace, namespaceWidth)
	identity = tview.Escape(identity)
	if production {
		palette := s.styles.Semantic()
		color := config.ReadableForeground(palette.Warning.Color(), palette.Canvas.Color())
		colorName := palette.Warning.String()
		if color.Hex() >= 0 {
			colorName = fmt.Sprintf("#%06x", color.Hex())
		}
		identity = fmt.Sprintf("[%s::b]%s[-::-]", colorName, identity)
	}
	if tview.TaggedStringWidth(identity+metrics) <= width {
		identity += metrics
	}
	if tview.TaggedStringWidth(identity+"  F2:destination") <= width {
		identity += "  F2:destination"
	}
	return identity
}

// FullDestination exposes the complete local destination, including long names.
func (s *StatusIndicator) FullDestination() string {
	context, namespace, connection := s.destination()
	mode := "read-write"
	if s.app.Config.IsReadOnly() {
		mode = "read-only"
	}
	details := fmt.Sprintf("Context: %s\nNamespace: %s\nMode: %s\nConnection: %s", context, namespace, mode, connection)
	if s.current.IsCurrent() && s.current.Context == context {
		details += fmt.Sprintf("\nCluster: %s\nUser: %s\nMetrics: %s", s.current.Cluster, s.current.User, MetricDescription(s.current.Metrics))
	}
	return details
}

func (s *StatusIndicator) SetPermanent(info string) {
	s.identity, s.permanent = false, info
	s.SetText(info)
}

func (s *StatusIndicator) Reset() {
	if s.identity {
		s.RefreshIdentity()
	} else {
		s.SetText(s.permanent)
	}
}

func (s *StatusIndicator) Err(msg string)  { s.app.Flash().SetMessage(model.FlashErr, msg) }
func (s *StatusIndicator) Warn(msg string) { s.app.Flash().SetMessage(model.FlashWarn, msg) }
func (s *StatusIndicator) Info(msg string) { s.app.Flash().SetMessage(model.FlashInfo, msg) }

// MetricPercent is the single availability/trend formatter for both header modes.
// Stale or missing observations never participate in a trend comparison.
//
//nolint:gocritic // Keep captured observations immutable across worker and UI boundaries.
func MetricPercent(previous, current client.MetricSample, memory bool) string {
	return metricPercent(previous, current, memory, false)
}

// CompactMetricPercent keeps complete availability states in the narrow cluster
// pane. Detailed failure reasons, observation time and source remain in F2.
//
//nolint:gocritic // Keep captured observations immutable across worker and UI boundaries.
func CompactMetricPercent(previous, current client.MetricSample, memory bool) string {
	return metricPercent(previous, current, memory, true)
}

//nolint:gocritic // Keep captured observations immutable across worker and UI boundaries.
func metricPercent(previous, current client.MetricSample, memory, compact bool) string {
	current = current.At(time.Now())
	if !current.HasValue() {
		state := current.State
		if state == "" {
			state = client.MetricsUnavailable
		}
		if compact {
			return string(state)
		}
		return "N/A (" + string(state) + ")"
	}
	oldValue, value := previous.Values.PercCPU, current.Values.PercCPU
	if memory {
		oldValue, value = previous.Values.PercMEM, current.Values.PercMEM
	}
	if current.State == client.MetricsStale {
		if compact {
			return render.PrintPerc(value) + " stale"
		}
		label := "stale"
		if current.Failure != "" {
			label += ": " + string(current.Failure)
		}
		return render.PrintPerc(value) + " (" + label + ")"
	}
	if previous.Fresh() && previous.Source == current.Source {
		switch {
		case oldValue < value:
			return render.PrintPerc(value) + " ↑"
		case oldValue > value:
			return render.PrintPerc(value) + " ↓"
		}
	}
	return render.PrintPerc(value)
}

//nolint:gocritic // Keep captured observations immutable across worker and UI boundaries.
func MetricDescription(sample client.MetricSample) string {
	sample = sample.At(time.Now())
	state := sample.State
	if state == "" {
		state = client.MetricsUnavailable
	}
	parts := []string{string(state)}
	if !sample.ObservedAt.IsZero() {
		parts = append(parts, "observed "+sample.ObservedAt.UTC().Format("15:04:05 UTC"))
	}
	if sample.Source != "" {
		parts = append(parts, sample.Source)
	}
	if sample.Reason != "" {
		parts = append(parts, sample.Reason)
	}
	return strings.Join(parts, " · ")
}

func ModeLabel(readOnly bool) string {
	if readOnly {
		return "[RO]"
	}
	return "[RW]"
}

// AsPercDelta represents a percentage with a delta indicator.
func AsPercDelta(ov, nv int) string {
	prev, cur := render.IntToStr(ov), render.IntToStr(nv)
	return cur + "%" + Deltas(prev, cur)
}

// SPDX-License-Identifier: Apache-2.0
package view

import (
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/hubble"
)

func (w *HubbleView) hubblePalette() logDetailPalette {
	styles := config.NewStyles()
	if w.app != nil && w.app.Styles != nil {
		styles = w.app.Styles
	}
	return logDetailPalette{
		foreground: styles.Body().FgColor.String(), key: styles.Views().Yaml.KeyColor.String(),
		value: styles.K9s.Frame.Status.ModifyColor.String(), muted: styles.Views().Log.Indicator.ToggleOffColor.String(),
		warning: styles.K9s.Frame.Status.PendingColor.String(), failure: styles.K9s.Frame.Status.ErrorColor.String(),
	}
}

func (w *HubbleView) hubbleDetail(e *hubble.Event) string {
	p := w.hubblePalette()
	section := func(title string) string { return detailStyled(p.key, "b", title) + "\n" }
	field := func(label, value string) string {
		if value == "" {
			value = "Not reported"
		}
		return "  " + detailStyled(p.muted, "", fmt.Sprintf("%-16s", label)) + detailStyled(p.foreground, "", value) + "\n"
	}
	verdictColor := p.warning
	switch e.Verdict {
	case "DROPPED", "ERROR":
		verdictColor = p.failure
	case "FORWARDED":
		verdictColor = p.value
	}
	var b strings.Builder
	b.WriteString(section("VERDICT"))
	verdict := e.Verdict
	if verdict == "" {
		verdict = "Not reported"
	}
	b.WriteString("  " + detailStyled(verdictColor, "b", verdict) + "\n")
	if e.DropReason != "" {
		b.WriteString(field("Drop reason", e.DropReason))
	}
	b.WriteString("\n" + section("ENDPOINTS"))
	b.WriteString(field("Source [1]", e.Source.String()))
	b.WriteString(field("Destination [2]", e.Destination.String()))
	b.WriteString(field("Transport", fmt.Sprintf("%s  %d → %d", e.Protocol, e.SourcePort, e.DestinationPort)))
	b.WriteString("\n" + section("OBSERVATION"))
	b.WriteString(field("Event / origin", fmt.Sprintf("%d / %s", e.ID, e.Origin)))
	b.WriteString(field("Timestamp", e.Time.Format(time.RFC3339Nano)))
	b.WriteString(field("Node", e.Node))
	b.WriteString("\n" + section("VISIBILITY & POLICY"))
	b.WriteString(field("L7", e.L7))
	b.WriteString(field("Policy evidence", e.Policy))
	b.WriteString("\n" + detailStyled(p.muted, "", "  Observed event, not a complete packet capture. L7 payloads are redacted."))
	return b.String()
}

func (w *HubbleView) renderHubbleStatus(st *hubble.Status, state, coverage, loss string, evicted uint64) {
	p := w.hubblePalette()
	stateColor := p.key
	if w.frozen {
		stateColor = p.warning
	}
	if st.LossDetail != "" {
		loss += " (" + st.LossDetail + ")"
	}
	lossColor := p.muted
	if st.Lost > 0 {
		lossColor = p.warning
	}
	shortcuts := "Enter inspect | s freeze/resume | / filter | 1/2 pod | r reconnect | ? help | Esc back"
	if w.statusOnly {
		shortcuts = "r reconnect | ? help | Esc back"
	}
	diagnostics := strings.TrimSpace(strings.Join([]string{st.Error, st.CoverageError, st.NodeEvent}, " "))
	diagnosticColor := p.muted
	if st.Error != "" || st.CoverageError != "" {
		diagnosticColor = p.failure
	}
	w.status.SetText(detailStyled(stateColor, "b", state) + detailStyled(p.muted, "", " | node coverage: "+coverage) +
		detailStyled(p.muted, "", fmt.Sprintf(" | retained observed events: %d", len(w.displayed))) + "\n" +
		detailStyled(lossColor, "", "Reported loss: "+loss) + detailStyled(p.muted, "", fmt.Sprintf(" | local evictions: %d | L7 redacted", evicted)) + "\n" +
		detailStyled(diagnosticColor, "", diagnostics) + "\n" +
		detailStyled(p.muted, "", w.notice) + detailStyled(p.key, "", " | filter: "+w.expression) + "\n" +
		detailStyled(p.muted, "", shortcuts))
}

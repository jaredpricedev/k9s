// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/tcell/v2"
	"github.com/mattn/go-runewidth"
)

// NextStep is the guidance for one selected row: the status evidence the row
// already reports, decoded, followed by the checks that open more evidence.
// It is a proposed check, never a diagnosis, and it is empty for a row that
// reports nothing wrong.
type NextStep struct {
	// Evidence restates what the row reports. Short is the same fact in
	// fewer cells for narrow frames; it is never a different claim.
	Evidence, Short string
	Level           statusLevel
	Checks          []NextCheck
}

// NextCheck names where to look next. A check with a Shortcut is an action
// this application registers ("p", ":pressure"); one without is plain advice.
type NextCheck struct {
	// Key is the bound key for a key check and zero for a typed command.
	Key      tcell.Key
	Shortcut string
	// Label is the short name; Detail says what to look for and is the
	// first text dropped when the line does not fit.
	Label, Detail string
}

// Empty reports whether the row needs no guidance.
func (s NextStep) Empty() bool { return s.Evidence == "" }

func keyCheck(key tcell.Key, label, detail string) NextCheck {
	return NextCheck{Key: key, Shortcut: tcell.KeyNames[key], Label: label, Detail: detail}
}

func commandCheck(command, detail string) NextCheck {
	return NextCheck{Shortcut: ":" + command, Detail: detail}
}

// Key names are registered at package init, so checks are built on use.
func logsCheck(previous bool) NextCheck {
	if previous {
		return keyCheck(KeyP, "previous logs", "")
	}
	return keyCheck(KeyL, "logs", "")
}

func pressureCheck() NextCheck { return commandCheck("pressure", "limit vs usage") }

// graphCheck and impactCheck name the two Prometheus-backed views. The view's
// filter drops them when no Prometheus source is declared for the context,
// so the line never names a check that would be refused.
func graphCheck() NextCheck { return commandCheck("graph", "memory vs limit over time") }

func impactCheck() NextCheck { return commandCheck("impact", "before vs after the rollout") }

func checkDescribe(detail string) NextCheck { return keyCheck(KeyD, "describe", detail) }

// nextStepFor derives guidance from the fields the list already holds, so a
// selection change never reads the cluster. Resources or states it does not
// recognize yield no guidance instead of a generic one.
func nextStepFor(resource string, h model1.Header, fields model1.Fields) NextStep {
	field := func(name string) string {
		if i, ok := h.IndexOf(name, true); ok && i < len(fields) {
			return strings.TrimSpace(fields[i])
		}
		return ""
	}
	switch resource {
	case "pods":
		return podNextStep(field("STATUS"), field(tableWhyColumn))
	case "persistentvolumeclaims":
		return claimNextStep(field("STATUS"))
	case "deployments", "statefulsets":
		return workloadNextStep(field(tableReadyColumn))
	case "nodes":
		return nodeNextStep(field("STATUS"))
	}
	return NextStep{}
}

// exitEvidence is the termination a WHY phrase reports, as render.podWhy
// writes it: "[container: ][last ]<Reason> exit <code>" or "... signal <n>".
type exitEvidence struct {
	container, reason string
	code              int
	signal, previous  bool
}

func parseExit(why string) (exitEvidence, bool) {
	var e exitEvidence
	words := strings.Fields(why)
	n := len(words)
	if n < 3 || (words[n-2] != "exit" && words[n-2] != "signal") {
		return e, false
	}
	code, err := strconv.Atoi(words[n-1])
	if err != nil {
		return e, false
	}
	e.code, e.signal, e.reason = code, words[n-2] == "signal", words[n-3]
	rest := words[:n-3]
	if len(rest) > 0 && rest[len(rest)-1] == "last" {
		e.previous = true
		rest = rest[:len(rest)-1]
	}
	switch {
	case len(rest) == 0:
	case len(rest) == 1 && strings.HasSuffix(rest[0], ":"):
		e.container = strings.TrimSuffix(rest[0], ":")
	default:
		return e, false
	}
	return e, true
}

// decodeExit states what an exit code or signal conventionally means. The
// wording stops at the convention: which process sent a signal, or why the
// application returned an error, is not in the status.
func decodeExit(e exitEvidence) (long, short string) {
	oom := e.reason == "OOMKilled"
	signal := 0
	switch {
	case e.signal:
		signal = e.code
	case e.code > 128 && e.code < 160:
		signal = e.code - 128
	}
	head := fmt.Sprintf("exit %d", e.code)
	if e.signal {
		head = fmt.Sprintf("signal %d", e.code)
	}
	if e.previous {
		head = "last " + head
	}
	if e.container != "" {
		head = e.container + ": " + head
	}
	short = head
	if oom {
		short += " OOMKilled"
	}
	var meaning string
	switch {
	case oom:
		meaning = "killed (SIGKILL), out of memory (OOMKilled)"
	case signal == 9:
		meaning = "killed (SIGKILL), usually the memory limit or the node"
	case signal == 15:
		meaning = "SIGTERM, a normal stop or a failed graceful shutdown"
	case signal == 11:
		meaning = "segfault (SIGSEGV)"
	case signal == 6:
		meaning = "aborted (SIGABRT)"
	case signal != 0:
		meaning = fmt.Sprintf("ended by signal %d", signal)
	case e.code == 126:
		meaning = "command not executable"
	case e.code == 127:
		meaning = "command not found in the image"
	case e.code == 1 || e.code == 2:
		meaning = "the application's own error"
	case e.code == 0:
		meaning = "exited without an error code"
	default:
		meaning = "application-defined error"
	}
	return head + ": " + meaning, short
}

func podNextStep(status, why string) NextStep {
	lower, reason := strings.ToLower(why), status
	if i := strings.LastIndex(reason, ":"); i >= 0 {
		// "Init:CrashLoopBackOff" keeps the reason after the stage.
		reason = reason[i+1:]
	}
	level := statusLevelOf("STATUS", status, tcell.ColorDefault)
	step := func(evidence, short string, checks ...NextCheck) NextStep {
		if level < levelProgress {
			level = levelWarning
		}
		if short == "" {
			short = evidence
		}
		return NextStep{Evidence: evidence, Short: short, Level: level, Checks: checks}
	}
	if e, ok := parseExit(why); ok {
		long, short := decodeExit(e)
		logs := logsCheck(e.previous)
		signal := e.code
		if !e.signal {
			signal -= 128
		}
		switch {
		case e.reason == "OOMKilled", signal == 9:
			return step(long, short, logs, pressureCheck(), graphCheck())
		case !e.signal && (e.code == 126 || e.code == 127):
			return step(long, short, checkDescribe("for the command and image"), logs)
		}
		return step(long, short, logs, checkDescribe("for events"))
	}
	switch reason {
	case "ImagePullBackOff", "ErrImagePull", "InvalidImageName", "ErrImageNeverPull", "ImageInspectError":
		return step(reason+": image not pulled", reason,
			checkDescribe("for the pull error"), NextCheck{Label: "check the image name and pull secret"})
	case "CreateContainerConfigError":
		return step(reason+": container configuration rejected", reason,
			commandCheck("config-review", "missing ConfigMap/Secret keys"), checkDescribe("for the message"))
	case "CreateContainerError", "RunContainerError", "StartError":
		return step(reason+": container did not start", reason, checkDescribe("for the runtime message"), logsCheck(true))
	case "CrashLoopBackOff":
		return step("CrashLoopBackOff: no previous exit reported", reason, logsCheck(true), checkDescribe("for events"))
	case "OOMKilled":
		return step("OOMKilled: killed (SIGKILL), out of memory", reason, logsCheck(false), pressureCheck(), graphCheck())
	case "Evicted":
		return step("Evicted by the node", reason, checkDescribe("for the eviction message"), commandCheck("capacity", "node allocatable"))
	case "Terminating":
		// Deletion is in progress; a readiness or exit hint would misread it.
		return NextStep{}
	case "Pending":
		if strings.Contains(lower, "nodes are available") || strings.Contains(lower, "nodes available") {
			evidence, checks := unschedulable(lower)
			return step(evidence, "Unschedulable", checks...)
		}
		if why == "" {
			return step("Pending: no scheduler message reported", "Pending", checkDescribe("for events"))
		}
		return step("Pending: see the reported message", "Pending", checkDescribe("for events"))
	case "ContainerCreating", "PodInitializing":
		// A pod that is starting without a reported message is in progress,
		// not failing; only a reported obstacle earns a line.
		if why == "" {
			return NextStep{}
		}
		if strings.Contains(lower, "mount") || strings.Contains(lower, "volume") || strings.Contains(lower, "attach") {
			return step(reason+": volume not ready, per the kubelet message", reason,
				commandCheck("storage", "bind/attach/mount stages"), checkDescribe("for events"))
		}
		return step(reason+": waiting, per the kubelet message", reason, checkDescribe("for events"))
	}
	if strings.HasSuffix(why, "running, not ready") {
		return step(why, "", checkDescribe("for the readiness probe"), logsCheck(false))
	}
	if level == levelFailure {
		return step(status+": no further status evidence", status, checkDescribe("for events"), logsCheck(false))
	}
	return NextStep{}
}

// unschedulable repeats which constraints the scheduler message names. A
// message with none it recognizes points at the message; it never picks one.
// The capacity review leads only when the message names resources.
func unschedulable(message string) (string, []NextCheck) {
	var kinds []string
	add := func(kind string, words ...string) {
		for _, w := range words {
			if strings.Contains(message, w) {
				kinds = append(kinds, kind)
				return
			}
		}
	}
	add("resources", "insufficient", "too many pods")
	add("taints", "taint")
	add("affinity/selector", "affinity", "node selector", "nodeselector")
	add("topology spread", "topology spread")
	add("volumes", "persistentvolumeclaim", "volume")
	add("cordoned nodes", "were unschedulable")
	add("host ports", "free ports")
	describe := checkDescribe("for the scheduler message")
	if len(kinds) == 0 {
		return "Unschedulable: see the scheduler message", []NextCheck{describe, commandCheck("capacity", "")}
	}
	evidence := "Unschedulable: " + strings.Join(kinds, " + ") + ", per the scheduler"
	switch kinds[0] {
	case "resources":
		return evidence, []NextCheck{commandCheck("capacity", "requests vs allocatable"), describe}
	case "volumes":
		return evidence, []NextCheck{commandCheck("storage", "claim binding"), describe}
	}
	return evidence, []NextCheck{describe, commandCheck("capacity", "")}
}

func claimNextStep(status string) NextStep {
	switch status {
	case "Pending":
		return NextStep{Evidence: "Pending: claim not bound", Short: "Pending", Level: levelWarning,
			Checks: []NextCheck{commandCheck("storage", "bind and provisioning stages"), checkDescribe("for events")}}
	case "Lost":
		return NextStep{Evidence: "Lost: bound volume no longer exists", Short: "Lost", Level: levelFailure,
			Checks: []NextCheck{commandCheck("storage", "claim and volume"), checkDescribe("for events")}}
	}
	return NextStep{}
}

func workloadNextStep(ready string) NextStep {
	if statusLevelOf(tableReadyColumn, ready, tcell.ColorDefault) != levelWarning {
		return NextStep{}
	}
	return NextStep{Evidence: "ready " + ready, Short: "ready " + ready, Level: levelWarning,
		Checks: []NextCheck{commandCheck("rollout", "conditions and replicas"), impactCheck(), keyCheck(tcell.KeyEnter, "pods", ""), checkDescribe("for events")}}
}

func nodeNextStep(status string) NextStep {
	if !strings.Contains(status, "NotReady") {
		return NextStep{}
	}
	return NextStep{Evidence: "NotReady: node condition", Short: "NotReady", Level: levelFailure,
		Checks: []NextCheck{checkDescribe("for conditions and events")}}
}

// available keeps only the checks the current view can really run. Advice
// without a shortcut is kept; a step whose every action is gone keeps its
// evidence, which is still true.
func (s NextStep) available(runnable func([]NextCheck) []NextCheck) NextStep {
	if runnable == nil || len(s.Checks) == 0 {
		return s
	}
	var actions []NextCheck
	for _, c := range s.Checks {
		if c.Shortcut != "" {
			actions = append(actions, c)
		}
	}
	keep := make(map[string]bool, len(actions))
	for _, c := range runnable(actions) {
		keep[c.Shortcut] = true
	}
	checks := make([]NextCheck, 0, len(s.Checks))
	for _, c := range s.Checks {
		if c.Shortcut == "" || keep[c.Shortcut] {
			checks = append(checks, c)
		}
	}
	s.Checks = checks
	return s
}

const (
	nextStepLabel    = "next: "
	nextStepMinWidth = 40
)

// nextStepPart is one run of the line: plain text, or a shortcut to emphasize.
type nextStepPart struct {
	text     string
	shortcut bool
	evidence bool
}

// fit lays the step out in at most width cells. It drops, in order, the
// details from the last check back, then the decoded evidence wording in
// favour of the short evidence, and only then trailing checks, so a narrower
// frame never hides a check before it has shortened the prose. Below a usable
// width, or when even evidence and one check cannot fit, it returns nothing:
// a clipped instruction is worse than none.
func (s NextStep) fit(width int, ascii bool) []nextStepPart {
	if s.Empty() || width < nextStepMinWidth {
		return nil
	}
	sep := " · "
	if ascii {
		sep = " | "
	}
	build := func(evidence string, checks []NextCheck, details int) []nextStepPart {
		parts := []nextStepPart{{text: nextStepLabel}, {text: evidence, evidence: true}}
		for i, c := range checks {
			parts = append(parts, nextStepPart{text: sep})
			text := c.Label
			if i < details && c.Detail != "" {
				text = strings.TrimSpace(text + " " + c.Detail)
			}
			if c.Shortcut != "" {
				parts = append(parts, nextStepPart{text: c.Shortcut, shortcut: true})
				if text != "" {
					text = " " + text
				}
			}
			parts = append(parts, nextStepPart{text: text})
		}
		return parts
	}
	fits := func(parts []nextStepPart) bool {
		total := 0
		for _, p := range parts {
			total += runewidth.StringWidth(p.text)
		}
		return total <= width
	}
	short := s.Short
	if short == "" {
		short = s.Evidence
	}
	least := min(1, len(s.Checks))
	for n := len(s.Checks); n >= least; n-- {
		for _, evidence := range []string{s.Evidence, short} {
			for details := n; details >= 0; details-- {
				if parts := build(evidence, s.Checks[:n], details); fits(parts) {
					return parts
				}
			}
		}
	}
	return nil
}

// SetNextStepFilter enables the next-step line for this table. The filter
// returns the checks the owning view can run now; a table without a filter
// never shows the line.
func (t *Table) SetNextStepFilter(ok func([]NextCheck) []NextCheck) {
	t.nextStepOK = ok
	t.nextStepID = ""
}

// SelectedNextStep returns the guidance for the selected row, limited to the
// checks the view can run. It reads only the rows already rendered and is
// recomputed when the selection or the rendered data changes.
func (t *Table) SelectedNextStep() NextStep {
	if t.nextStepOK == nil || t.nextStepData == nil {
		return NextStep{}
	}
	id, ok := t.GetRowID(t.GetSelectedRowIndex())
	if !ok || id == "" || t.GetSelectedRowIndex() == 0 {
		return NextStep{}
	}
	if id == t.nextStepID {
		return t.nextStep
	}
	t.nextStepID, t.nextStep = id, NextStep{}
	if re, found := t.nextStepData.FindRow(id); found {
		step := nextStepFor(t.gvr.GVR().Resource, t.nextStepData.Header(), re.Row.Fields)
		if !step.Empty() {
			t.nextStep = step.available(t.nextStepOK)
		}
	}
	return t.nextStep
}

// drawNextStep writes the guidance onto the bottom edge of the table frame.
// The edge always exists, so the line never moves a row, and a healthy
// selection leaves the border untouched.
func (t *Table) drawNextStep(screen tcell.Screen) {
	if t.styles == nil || t.nextStepOK == nil {
		return
	}
	selectable, _ := t.GetSelectable()
	if !selectable {
		return
	}
	x, y, width, height := t.GetRect()
	if height < 4 {
		return
	}
	step := t.SelectedNextStep()
	// A corner and one border cell stay on each side, and the text is padded
	// by a space each side.
	parts := step.fit(width-6, t.noIcon)
	if len(parts) == 0 {
		return
	}
	p := &t.presentation
	base := tcell.StyleDefault.Background(p.canvas).Foreground(p.text)
	evidence := p.warning
	if step.Level == levelFailure {
		evidence = p.failure
	}
	col, row := x+2, y+height-1
	put := func(text string, style tcell.Style) {
		for _, r := range text {
			screen.SetContent(col, row, r, nil, style)
			col += runewidth.RuneWidth(r)
		}
	}
	put(" ", base)
	for _, part := range parts {
		style := base
		switch {
		case part.shortcut:
			style = base.Foreground(p.focus).Bold(true)
		case part.evidence:
			style = base.Foreground(evidence).Bold(true)
		}
		put(part.text, style)
	}
	put(" ", base)
}

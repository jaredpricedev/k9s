// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/tcell/v2"
)

func (w *logWorkbench) compileFilter(text string) (query *logstream.Query, expr string, rules []string, err error) {
	text = strings.TrimSpace(text)
	rules = append([]string(nil), w.localRules...)
	expr = text
	if strings.HasPrefix(text, "-") && len(text) > 1 {
		rules = append(rules, strings.TrimSpace(text[1:]))
		expr = w.expression
	}
	merged, err := logstream.MergeRules(w.teamRules, rules)
	if err != nil {
		return nil, "", nil, err
	}
	query, err = logstream.CompileQuery(expr, merged)
	return query, expr, rules, err
}

func (w *logWorkbench) filter(text string) error {
	query, expression, rules, err := w.compileFilter(text)
	if err != nil {
		return err
	}
	w.query, w.expression, w.localRules = query, expression, rules
	return nil
}

func (*logWorkbench) validateFilter(text string) (string, error) {
	// Prompt buffer notifications can run on a worker. Validation therefore
	// parses only the draft and never reads presentation state owned by the UI.
	text = strings.TrimSpace(text)
	var err error
	if strings.HasPrefix(text, "-") && len(text) > 1 {
		_, err = logstream.MergeRules(nil, []string{strings.TrimSpace(text[1:])})
	} else {
		_, err = logstream.CompileQuery(text, nil)
	}
	return "log fields / regex", err
}

func (w *logWorkbench) activateFilter(event *tcell.EventKey) *tcell.EventKey {
	if w.owner.app.InCmdMode() {
		return event
	}
	w.owner.app.ResetPrompt(w.owner.logs.cmdBuff)
	w.owner.app.Prompt().SetFilterValidator(w.validateFilter)
	return nil
}

func (w *logWorkbench) refreshProfile() {
	if w.owner == nil {
		return
	}
	opts := w.owner.model.LogOptionsSnapshot()
	scope := logstream.ProfileScope(opts.Context, namespaceOf(opts.Path), opts.Labels, w.owner.app.Config.K9s.Logger.NoiseAppLabels, opts.WorkloadKind, opts.WorkloadName)
	token := fmt.Sprintf("%v/%s", scope, opts.Annotations["k9plus.io/log-noise"])
	if token == w.profileLoaded {
		return
	}
	team, err := logstream.TeamRules(opts.Annotations["k9plus.io/log-noise"])
	if err != nil {
		w.notice = "Team noise profile: " + err.Error()
		w.profileLoaded = token
		return
	}
	local, err := logstream.LoadProfile(w.profilePath(), scope)
	if err != nil {
		w.notice = "Local noise profile: " + err.Error()
		return
	}
	w.teamRules = team
	w.localRules = local
	w.scope = scope
	w.profileLoaded = token
	if err = w.filter(w.expression); err != nil {
		w.notice = err.Error()
	}
}
func namespaceOf(path string) string {
	parts := strings.SplitN(path, "/", 2)
	if len(parts) > 1 {
		return parts[0]
	}
	return ""
}
func (*logWorkbench) profilePath() string {
	return filepath.Join(config.AppConfigDir, "log-noise.json")
}
func (w *logWorkbench) activeRules() []string {
	rules := []string{}
	if w.expression != "" {
		rules = append(rules, w.expression)
	}
	for _, r := range append(append([]string{}, w.teamRules...), w.localRules...) {
		rules = append(rules, "-"+r)
	}
	if w.isolated != "" {
		rules = append(rules, "source="+w.isolated)
	}
	for k := range w.excluded {
		rules = append(rules, "exclude source="+k)
	}
	return rules
}
func (w *logWorkbench) visible(entries []logstream.Entry) []logstream.Entry {
	out := make([]logstream.Entry, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		key := e.Source.Key()
		if w.isolated != "" && key != w.isolated || w.excluded[key] {
			continue
		}
		// Informational provenance survives content/severity filters, but explicit
		// source isolation/exclusion applies to markers as well.
		if e.Marker == nil && !w.query.Match(*e) {
			continue
		}
		if w.mode == modePattern && e.Marker == nil {
			candidate := *e
			if w.patternSafe {
				candidate = logstream.SafeEntry(*e)
			}
			if logstream.EntryPattern(candidate).Key != w.patternKey {
				continue
			}
		}
		out = append(out, *e)
	}
	return out
}

// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// comparisonView records A once. Refresh captures B explicitly, and neither
// navigation nor normalization can silently move the chosen baseline.
type comparisonView struct {
	*Details
	target       SelectedResourceTarget
	baseline     *inspect.Observation
	other        inspect.Observation
	normalize    bool
	showEvidence bool
	retainedText string
	active       bool
	loader       func(context.Context, bool) inspect.Observation
	cancel       context.CancelFunc
	generation   uint64
}

func (c *Command) comparisonCommand() {
	var target SelectedResourceTarget
	switch v := c.app.Content.Top().(type) {
	case *inspectionDetails:
		target = v.target
	case SelectedResource:
		target = v.SelectedResource()
	case ResourceViewer:
		target = resolveSelectedResource(v, c.app.Config.ActiveContextName())
	default:
		c.app.Flash().Err(fmt.Errorf("select a resource to choose comparison baseline A"))
		return
	}
	c.app.openResourceComparison(target)
}

//nolint:gocritic // The asynchronous comparison owns an immutable copy of the selected identity.
func (a *App) openResourceComparison(target SelectedResourceTarget) {
	if err := target.Err(); err != nil {
		a.Flash().Err(err)
		return
	}
	if target.Context != a.Config.ActiveContextName() {
		a.Flash().Warn("Context changed; choose comparison A again")
		return
	}
	connection, err := pinInspectionConnection(a.Conn())
	if err != nil {
		a.Flash().Err(err)
		return
	}
	v := &comparisonView{Details: NewDetails(a, "Resource comparison", target.Path(), contentInspection, true), target: target, normalize: true}
	v.loader = func(ctx context.Context, baseline bool) inspect.Observation {
		captured := target
		if !baseline {
			// B intentionally observes the current identity, so a replacement is
			// labeled in the comparison rather than hidden or mistaken for A.
			captured.UID = ""
		}
		return loadResourceObservation(ctx, connection, captured)
	}
	v.Update("Capturing chosen baseline A...\nPress r after capture to obtain comparison observation B. A remains fixed.")
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}

func (v *comparisonView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Capture B (keep A)", func(*tcell.EventKey) *tcell.EventKey { v.capture(false); return nil }, true))
	v.actions.Add(ui.KeyN, ui.NewKeyAction("Toggle API noise", func(*tcell.EventKey) *tcell.EventKey { v.normalize = !v.normalize; v.renderComparison(); return nil }, true))
	v.actions.Add(ui.KeyO, ui.NewKeyAction("Overview / all evidence", func(*tcell.EventKey) *tcell.EventKey {
		v.showEvidence = !v.showEvidence
		v.renderComparison()
		return nil
	}, true))
	return nil
}

func (v *comparisonView) Start() {
	v.active = true
	v.app.Styles.RemoveListener(v.Details)
	v.app.Styles.AddListener(v.Details)
	if v.baseline == nil {
		v.capture(true)
	}
}

func (v *comparisonView) Stop() {
	v.active = false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.Details.Stop()
}

func (v *comparisonView) SelectedResource() SelectedResourceTarget { return v.target }

// CompactWorkspace keeps the destination visible while giving the changes room.
func (*comparisonView) CompactWorkspace() bool { return true }

func (v *comparisonView) capture(baseline bool) {
	if v.target.Context != v.app.Config.ActiveContextName() {
		v.app.Flash().Warn("Context changed; choose comparison A again")
		return
	}
	if !baseline && v.baseline == nil {
		v.app.Flash().Warn("Wait for chosen baseline A to finish before capturing B")
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	generation := v.generation
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	v.cancel = cancel
	v.app.Flash().Info("Capturing read-only resource observation...")
	go func() {
		defer cancel()
		observation := v.loader(ctx, baseline)
		if ctx.Err() == context.Canceled {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if !v.active || generation != v.generation || v.target.Context != v.app.Config.ActiveContextName() || v.app.Content.Top() != v {
				return
			}
			v.acceptObservation(observation, baseline)
			v.renderComparison()
		})
	}()
}

//nolint:gocritic // The view accepts an independent observation value, never caller-owned mutable state.
func (v *comparisonView) acceptObservation(observation inspect.Observation, baseline bool) {
	observation = inspect.SafeObservation(observation)
	if baseline {
		if v.baseline == nil {
			v.baseline = &observation
			v.other = inspect.Observation{
				Identity: observation.Identity, Source: "not captured", State: inspect.ObservationUnknown,
				Reason: "Press r to capture B; chosen baseline A stays fixed",
			}
		}
		return
	}
	v.other = observation
}

func (v *comparisonView) renderComparison() {
	if v.baseline == nil {
		return
	}
	comparison := inspect.Compare(*v.baseline, v.other, v.normalize)
	v.retainedText = inspect.ComparisonText(comparison)
	text := comparisonOverview(comparison, v.normalize)
	if v.showEvidence {
		text = v.retainedText
	}
	v.Update(text)
}

// comparisonOverview consumes typed changes. Full messages and values remain
// in retainedText; changing presentation never captures a new observation.
//
//nolint:gocritic // The formatter never mutates the comparison or its observations.
func comparisonOverview(c inspect.Comparison, normalized bool) string {
	const previewChanges = 6
	var b strings.Builder
	b.WriteString("RESOURCE COMPARISON · CHANGES FIRST\n")
	if c.Recreated {
		b.WriteString("RECREATED IDENTITY · B is a replacement object (different UID).\n")
	}
	if !c.Comparable {
		b.WriteString("Comparison unavailable · " + c.Notice + "\n")
	} else if len(c.Changes) == 0 {
		b.WriteString("No differences in retained fields · this does not establish health.\n")
	} else {
		fmt.Fprintf(&b, "%d changed fields", len(c.Changes))
		if c.Truncated {
			b.WriteString(" · incomplete comparison")
		}
		b.WriteString("\n")
		changes := append([]inspect.Change(nil), c.Changes...)
		sort.SliceStable(changes, func(i, j int) bool {
			return comparisonChangePriority(changes[i].Path) < comparisonChangePriority(changes[j].Path)
		})
		for i, change := range changes {
			if i == previewChanges {
				fmt.Fprintf(&b, "  +%d more changes · o opens all evidence\n", len(changes)-previewChanges)
				break
			}
			before, after := comparisonValues(change.Before, change.After)
			fmt.Fprintf(&b, "  %s %s\n    %s → %s\n", strings.ToUpper(change.Kind), comparisonPreview(change.Path, 88), before, after)
		}
	}
	b.WriteString("\nOBSERVATIONS · A stays fixed; r captures B\n")
	for i, observation := range []inspect.Observation{c.A, c.B} {
		label := "A · chosen baseline"
		if i == 1 {
			label = "B · comparison observation"
		}
		identity := observation.Identity
		fmt.Fprintf(&b, "%s · State: %s\n  %s · %s %s/%s · UID %s\n  Observed: %s\n  Source: %s\n", label, observation.State,
			identity.Context, identity.GVR, identity.Namespace, identity.Name, comparisonUnknown(identity.UID), comparisonObserved(observation.ObservedAt), observation.Source)
		if observation.Reason != "" {
			fmt.Fprintf(&b, "  %s\n", comparisonPreview(observation.Reason, 140))
		}
	}
	b.WriteString("\nLIMITATIONS\n")
	if normalized {
		fmt.Fprintf(&b, "API bookkeeping hidden · %d paths omitted · n reveals it.\n", len(c.Omitted))
	} else {
		b.WriteString("API bookkeeping included · n hides it.\n")
	}
	for _, observation := range []inspect.Observation{c.A, c.B} {
		if len(observation.Limits) > 0 {
			b.WriteString(comparisonPreview(strings.Join(observation.Limits, "; "), 180) + "\n")
		}
	}
	if c.Notice != "" && c.Comparable {
		b.WriteString(comparisonPreview(c.Notice, 180) + "\n")
	}
	b.WriteString("Credential redaction is heuristic; no desired configuration source selected.\n")
	b.WriteString("\no overview / all evidence · r capture B · n API noise · / search · Esc back\n")
	return b.String()
}

func comparisonChangePriority(path string) int {
	if strings.HasPrefix(path, "/spec/") {
		return 0
	}
	if strings.HasPrefix(path, "/status/") {
		return 1
	}
	if strings.HasPrefix(path, "/metadata/") {
		return 3
	}
	return 2
}

func comparisonValues(before, after any) (beforePreview, afterPreview string) {
	encode := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			return "unavailable"
		}
		return string(data)
	}
	a, b := []rune(encode(before)), []rune(encode(after))
	// Long arrays and objects often differ beyond an identical leading spec.
	// Center the preview on the first changed value rather than hiding it behind
	// a shared prefix. The full values remain available in all-evidence mode.
	common := 0
	for common < min(len(a), len(b)) && a[common] == b[common] {
		common++
	}
	start := 0
	if max(len(a), len(b)) > 72 {
		start = max(0, common-24)
	}
	preview := func(value []rune) string {
		prefix := ""
		if start > 0 {
			prefix = "…"
		}
		return prefix + comparisonPreview(string(value[min(start, len(value)):]), 72)
	}
	return preview(a), preview(b)
}

func comparisonObserved(at time.Time) string {
	if at.IsZero() {
		return workspaceUnknown
	}
	return at.UTC().Format(time.RFC3339Nano)
}

func comparisonPreview(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return text
}

func comparisonUnknown(text string) string {
	if text == "" {
		return workspaceUnknown
	}
	return text
}

// loadResourceObservation is shared with explicit evidence capture. The target
// is immutable and callers pin connection handles before asynchronous work.
//
//nolint:gocritic // Each observation request owns the selected identity independently of later UI selections.
func loadResourceObservation(ctx context.Context, connection client.Connection, target SelectedResourceTarget) inspect.Observation {
	gvr := ""
	if target.GVR != nil {
		gvr = target.GVR.String()
	}
	o := inspect.Observation{
		Identity: inspect.ResourceIdentity{Context: target.Context, GVR: gvr, Namespace: target.Namespace, Name: target.Name, UID: string(target.UID)},
		Source:   "Kubernetes API observation", ObservedAt: time.Now().UTC(), State: inspect.ObservationUnknown,
	}
	if err := target.Err(); err != nil {
		o.Reason = err.Error()
		return inspect.SafeObservation(o)
	}
	if target.GVR.R() == "secrets" {
		o.Object = map[string]any{"kind": "Secret"}
		return inspect.SafeObservation(o)
	}
	dyn, err := connection.DynDial()
	if err != nil {
		o.Reason = err.Error()
		return inspect.SafeObservation(o)
	}
	object, err := dyn.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		o.Reason = err.Error()
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			o.State = inspect.ObservationDenied
		} else if apierrors.IsNotFound(err) || ctx.Err() != nil {
			o.State = inspect.ObservationStale
		}
		return inspect.SafeObservation(o)
	}
	o.Identity.UID = string(object.GetUID())
	o.Object = object.Object
	o.State = inspect.ObservationComplete
	if err := verifySelectedIdentity(target, object); err != nil {
		o.State = inspect.ObservationStale
		o.Reason = err.Error()
	}
	return inspect.SafeObservation(o)
}

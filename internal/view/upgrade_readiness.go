// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/k9s/internal/upgrade"
	"github.com/derailed/tcell/v2"
	"k8s.io/apimachinery/pkg/version"
)

const upgradeReadinessCommand = "upgrade-readiness"
const upgradeReadinessTimeout = 10 * time.Second
const upgradeUnavailableState = "unavailable"
const upgradeCanceledState = "canceled/timeout"

type upgradeReadinessDetails struct {
	*Details
	request    connectionHealthRequest
	loader     func(context.Context, connectionHealthRequest) upgrade.Snapshot
	cancel     context.CancelFunc
	generation uint64
	started    bool
	snapshot   upgrade.Snapshot
	status     string
}

func (*upgradeReadinessDetails) CompactWorkspace() bool { return true }

func (c *Command) upgradeReadinessCommand(line string) {
	if len(strings.Fields(line)) != 1 {
		c.app.Flash().Warn("Use :upgrade-readiness")
		return
	}
	request := connectionHealthRequest{
		Context: c.app.Config.ActiveContextName(), Namespace: c.app.Config.ActiveNamespace(),
		Revision: c.app.Config.DestinationRevision(),
	}
	if !upgrade.ValidNamespace(request.Namespace) || upgrade.SafeField(request.Context) != request.Context {
		c.app.Flash().Warn("Upgrade readiness requires one explicit namespace and a bounded context name without control characters")
		return
	}
	connection, err := pinInspectionConnection(c.app.Conn())
	if err != nil {
		c.app.Flash().Err(err)
		return
	}
	v := &upgradeReadinessDetails{Details: NewDetails(c.app, "Upgrade readiness evidence", request.Context, contentInspection, true), request: request}
	v.loader = func(ctx context.Context, request connectionHealthRequest) upgrade.Snapshot {
		return loadUpgradeReadiness(ctx, connection, request)
	}
	if err := c.app.inject(v, false); err != nil {
		c.app.Flash().Err(err)
	}
}

func (d *upgradeReadinessDetails) Init(ctx context.Context) error {
	if err := d.Details.Init(ctx); err != nil {
		return err
	}
	d.actions.Add(ui.KeyR, ui.NewKeyAction("Refresh upgrade evidence", func(event *tcell.EventKey) *tcell.EventKey {
		if d.cmdBuff.IsActive() {
			return event
		}
		d.refresh()
		return nil
	}, true))
	return nil
}
func (d *upgradeReadinessDetails) Start() {
	d.started = true
	d.app.Prompt().SetModel(d.cmdBuff)
	if d.snapshot.ObservedAt.IsZero() {
		d.refresh()
	} else {
		d.Update(renderUpgradeReadiness(&d.snapshot, d.status, upgradeReadinessWidth(d)))
	}
}
func (d *upgradeReadinessDetails) Stop() {
	d.started = false
	d.generation++
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	d.Details.Stop()
}
func (d *upgradeReadinessDetails) current(generation uint64) bool {
	return d.started && generation == d.generation && d.app.Content.Top() == d &&
		d.request.Context == d.app.Config.ActiveContextName() && d.request.Namespace == d.app.Config.ActiveNamespace() &&
		d.request.Revision == d.app.Config.DestinationRevision()
}
func (d *upgradeReadinessDetails) refresh() {
	if d.request.Context != d.app.Config.ActiveContextName() ||
		d.request.Namespace != d.app.Config.ActiveNamespace() ||
		d.request.Revision != d.app.Config.DestinationRevision() {
		d.status = "Destination changed; reopen :upgrade-readiness to capture the intended context and namespace."
		d.Update(renderUpgradeReadiness(&d.snapshot, d.status, upgradeReadinessWidth(d)))
		return
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.generation++
	generation, request := d.generation, d.request
	ctx, cancel := context.WithTimeout(d.app.sessionContext(), upgradeReadinessTimeout)
	d.cancel = cancel
	d.status = "Reading bounded native facts..."
	d.Update(renderUpgradeReadiness(&d.snapshot, d.status, upgradeReadinessWidth(d)))
	loader := d.loader
	if loader == nil {
		return
	}
	go func() {
		defer cancel()
		snapshot := loader(ctx, request)
		if !d.app.IsRunning() {
			return
		}
		d.app.QueueUpdateDraw(func() {
			if d.current(generation) {
				d.acceptSnapshot(&snapshot)
				d.Update(renderUpgradeReadiness(&d.snapshot, d.status, upgradeReadinessWidth(d)))
			}
		})
	}()
}

func (d *upgradeReadinessDetails) acceptSnapshot(snapshot *upgrade.Snapshot) {
	if snapshot.Context != d.request.Context || snapshot.Namespace != d.request.Namespace {
		return
	}
	if snapshot.AllFactReadsFailed() && !d.snapshot.ObservedAt.IsZero() {
		d.status = fmt.Sprintf("Refresh failed at %s; retained earlier evidence. API:%s nodes:%s deployments:%s daemonsets:%s statefulsets:%s",
			evidenceTime(snapshot.ObservedAt), snapshot.ServerVersionState, snapshot.Nodes.State,
			snapshot.Deployments.State, snapshot.DaemonSets.State, snapshot.StatefulSets.State)
		return
	}
	d.snapshot, d.status = *snapshot, ""
}

func upgradeReadinessWidth(d *upgradeReadinessDetails) int {
	_, _, width, _ := d.GetRect()
	return width
}

func loadUpgradeReadiness(ctx context.Context, conn client.Connection, request connectionHealthRequest) upgrade.Snapshot {
	ctx, cancel := context.WithTimeout(ctx, upgrade.CollectionTimeout)
	defer cancel()
	snapshot := upgrade.Snapshot{
		Context: upgrade.SafeField(request.Context), Namespace: upgrade.SafeField(request.Namespace), NamespaceUID: "unknown",
		NamespaceState: upgradeUnavailableState, ObservedAt: time.Now(),
		Nodes: upgrade.Section{State: upgradeUnavailableState}, Deployments: upgrade.Section{State: upgradeUnavailableState},
		DaemonSets: upgrade.Section{State: upgradeUnavailableState}, StatefulSets: upgrade.Section{State: upgradeUnavailableState},
	}
	if !upgrade.ValidNamespace(request.Namespace) || upgrade.SafeField(request.Context) != request.Context || conn == nil {
		snapshot.ServerVersionState, snapshot.ServerVersion = upgradeUnavailableState, upgradeUnavailableState
		return snapshot
	}
	typed, err := conn.Dial()
	if err != nil {
		snapshot.ServerVersionState, snapshot.ServerVersion = upgradeUnavailableState, upgradeUnavailableState
		return snapshot
	}
	var info version.Info
	restClient := typed.Discovery().RESTClient()
	if restClient == nil {
		snapshot.ServerVersionState, snapshot.ServerVersion = upgradeUnavailableState, upgradeUnavailableState
	} else {
		readCtx, readCancel := context.WithTimeout(ctx, upgrade.ReadTimeout)
		body, readErr := restClient.Get().AbsPath("/version").Do(readCtx).Raw()
		readCancel()
		if readErr == nil {
			readErr = json.Unmarshal(body, &info)
		}
		if readErr != nil {
			snapshot.ServerVersionState, snapshot.ServerVersion = upgrade.StateForError(readErr), upgrade.StateForError(readErr)
		} else if info.GitVersion == "" {
			snapshot.ServerVersionState, snapshot.ServerVersion = "unsupported/404", upgradeUnavailableState
		} else {
			snapshot.ServerVersionState, snapshot.ServerVersion = upgrade.StateForError(nil), upgrade.SafeField(info.GitVersion)
		}
	}
	if ctx.Err() != nil {
		snapshot.ServerVersionState, snapshot.ServerVersion = upgradeCanceledState, upgradeCanceledState
		return snapshot
	}
	reader := typed
	collected := upgrade.Collect(ctx, reader, request.Namespace, request.Context, snapshot.ServerVersion, snapshot.ObservedAt)
	collected.ServerVersionState, collected.ServerVersion = snapshot.ServerVersionState, snapshot.ServerVersion
	return collected
}

func renderUpgradeReadiness(s *upgrade.Snapshot, status string, width int) string {
	if width < 40 {
		return "Terminal too small for upgrade readiness evidence (minimum 40 columns)."
	}
	if width < 80 {
		var b strings.Builder
		b.WriteString("Upgrade evidence\n")
		fmt.Fprintf(&b, "Context: %s\nNamespace: %s (%s)\nUID: %s\n",
			shortEvidence(s.Context, 24), shortEvidence(s.Namespace, 24), s.NamespaceState, shortEvidence(s.NamespaceUID, 20))
		fmt.Fprintf(&b, "API server: %s (%s)\nNodes: %s%s\n", s.ServerVersion, s.ServerVersionState, s.Nodes.State, sectionCoverage(s.Nodes))
		fmt.Fprintf(&b, "Deployments: %s%s\nDaemonSets: %s%s\nStatefulSets: %s%s\n",
			s.Deployments.State, sectionCoverage(s.Deployments), s.DaemonSets.State,
			sectionCoverage(s.DaemonSets), s.StatefulSets.State, sectionCoverage(s.StatefulSets))
		factRows := 1
		if width >= 60 {
			factRows = 2
		}
		appendCompactUpgradeFacts(&b, "node", s.Nodes, width, factRows)
		appendCompactUpgradeFacts(&b, "dep", s.Deployments, width, factRows)
		appendCompactUpgradeFacts(&b, "ds", s.DaemonSets, width, factRows)
		appendCompactUpgradeFacts(&b, "sts", s.StatefulSets, width, factRows)
		fmt.Fprintf(&b, "Source: native Kubernetes API\nObserved: %s\n", evidenceTime(s.ObservedAt))
		if status != "" {
			fmt.Fprintf(&b, "Status: %s\n", upgrade.SafeField(status))
		}
		b.WriteString("kubent/pluto: unsupported\nVersions do not prove compatibility.\nDeprecated API usage not exhausted.\n")
		return b.String()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Upgrade readiness evidence\nDestination: %s\nNamespace: %s (%s)  UID: %s\nSource: native Kubernetes API  Observed: %s\n",
		s.Context, s.Namespace, s.NamespaceState, shortEvidence(s.NamespaceUID, 20), evidenceTime(s.ObservedAt))
	fmt.Fprintf(&b, "API server version: %s (%s)\n", s.ServerVersion, s.ServerVersionState)
	renderUpgradeSection(&b, "Nodes / kubelet", s.Nodes)
	renderUpgradeSection(&b, "Deployments / container images", s.Deployments)
	renderUpgradeSection(&b, "DaemonSets / container images", s.DaemonSets)
	renderUpgradeSection(&b, "StatefulSets / container images", s.StatefulSets)
	b.WriteString("Optional kubent/pluto adapters: unsupported (no safe adapter configured)\n")
	b.WriteString("Version facts do not establish compatibility or upgrade safety. Inventory does not exhaust deprecated API usage.\n")
	if status != "" {
		fmt.Fprintf(&b, "Status: %s\n", status)
	}
	return b.String()
}

func appendCompactUpgradeFacts(b *strings.Builder, label string, section upgrade.Section, width, maxRows int) {
	nameWidth := width/2 - len(label) - 3
	versionWidth := width - nameWidth - len(label) - 4
	count := len(section.Items)
	if count > maxRows {
		count = maxRows
	}
	for _, fact := range section.Items[:count] {
		fmt.Fprintf(b, "  %s %s %s\n", label, shortEvidence(fact.Name, nameWidth), shortEvidence(fact.Version, versionWidth))
		fmt.Fprintf(b, "  UID:%s RV:%s\n", shortEvidence(fact.UID, 12), shortEvidence(fact.ResourceVersion, 12))
	}
	remaining := len(section.Items) - count
	if remaining > 0 {
		fmt.Fprintf(b, "  %s +%d more listed\n", label, remaining)
	}
}

func sectionCoverage(section upgrade.Section) string {
	coverage := fmt.Sprintf(" (%d facts", len(section.Items))
	if section.Truncated {
		coverage += "; first 100"
	}
	coverage += ")"
	if section.Detail != "" {
		coverage += " partial/unknown"
	}
	return coverage
}

func renderUpgradeSection(b *strings.Builder, name string, section upgrade.Section) {
	fmt.Fprintf(b, "%s: %s%s", name, section.State, sectionCoverage(section))
	if section.Detail != "" {
		fmt.Fprintf(b, " · %s", section.Detail)
	}
	b.WriteByte('\n')
	for _, fact := range section.Items {
		fmt.Fprintf(b, "  %s  %s  UID:%s RV:%s\n", fact.Name, fact.Version, shortEvidence(fact.UID, 12), shortEvidence(fact.ResourceVersion, 12))
	}
}

func shortEvidence(value string, limit int) string {
	if value == "" {
		return upgradeUnavailableState
	}
	return ui.Truncate(value, limit)
}
func evidenceTime(value time.Time) string {
	if value.IsZero() {
		return upgradeUnavailableState
	}
	return value.UTC().Format(time.RFC3339)
}

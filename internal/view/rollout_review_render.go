// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/review"
)

func rolloutTabText(snapshot *review.RolloutSnapshot, tab, width int, selectedUID, recoveryUID string) string {
	switch tab {
	case 1:
		return rolloutRevisions(snapshot, width, selectedUID)
	case 2:
		return rolloutPods(snapshot, width)
	case rolloutRecoveryTab:
		return rolloutRecovery(snapshot, recoveryUID, width)
	case rolloutEvidenceTab:
		return rolloutEvidence(snapshot)
	default:
		return rolloutOverview(snapshot, width)
	}
}

func rolloutOverview(snapshot *review.RolloutSnapshot, width int) string {
	var b strings.Builder
	state, reason := snapshot.Progress()
	marker := "[?]"
	switch state {
	case review.RolloutComplete:
		marker = "[+]"
	case review.RolloutBlocked:
		marker = "[!]"
	case review.RolloutProgressing, review.RolloutPaused:
		marker = "[~]"
	}
	b.WriteString("ROLLOUT OBSERVATION\n")
	fmt.Fprintln(&b, fitInvestigation(marker+" "+strings.ToUpper(state)+" · "+reason, width))
	if width < 60 && reason != "" {
		fmt.Fprintln(&b, fitInvestigation(reason, width))
	}
	fmt.Fprintf(&b, "Generation %s · observed %s\n", rolloutCount(snapshot.Generation), rolloutCount(snapshot.ObservedGeneration))
	b.WriteString("\nREPLICA COUNTS · retained Deployment status\n")
	if width < 60 {
		fmt.Fprintf(&b, "Desired %s · Updated %s\nReady %s · Available %s · Total %s\n",
			rolloutCount(snapshot.Desired), rolloutCount(snapshot.Updated),
			rolloutCount(snapshot.Ready), rolloutCount(snapshot.Available), rolloutCount(snapshot.Replicas))
	} else {
		columns := []int{11, 11, 11, 13, 11}
		counts := []string{rolloutCount(snapshot.Desired), rolloutCount(snapshot.Updated),
			rolloutCount(snapshot.Ready), rolloutCount(snapshot.Available), rolloutCount(snapshot.Replicas)}
		fmt.Fprintln(&b, tableRow([]string{"DESIRED", "UPDATED", "READY", "AVAILABLE", "TOTAL"}, columns))
		fmt.Fprintln(&b, tableRow(counts, columns))
	}
	b.WriteString("\nCONTROLLER CONDITIONS\n")
	if len(snapshot.Conditions) == 0 {
		b.WriteString("[?] No conditions retained; absence does not establish health.\n")
	}
	for _, condition := range snapshot.Conditions[:min(3, len(snapshot.Conditions))] {
		fmt.Fprintln(&b, fitInvestigation(condition.Type+"="+condition.Status+" · "+condition.Reason, width))
		if condition.Type == "Progressing" && !condition.UpdatedAt.IsZero() {
			fmt.Fprintln(&b, fitInvestigation("Last condition update: "+relativeAt(condition.UpdatedAt, snapshot.CapturedAt)+"; not rollout start", width))
		}
	}
	b.WriteString("\nRETAINED WORKLOAD EVIDENCE\n")
	fmt.Fprintf(&b, "%d UID-owned ReplicaSets · %d UID-owned Pods\n", len(snapshot.Revisions), len(snapshot.Pods))
	b.WriteString("2 revisions: exact templates · 3 Pods: declared image / imageID\n")
	b.WriteString("4 recovery: selected template comparison; nothing executed\n")
	b.WriteString("\nVISIBILITY\n")
	for _, coverage := range snapshot.Coverage {
		fmt.Fprintln(&b, fitInvestigation(coverage.Source+": "+coverage.State+" · "+coverage.Detail, width))
	}
	b.WriteString("\nController status is an observation, not proof of an accepted write's outcome.\n")
	return b.String()
}

func rolloutRevisions(snapshot *review.RolloutSnapshot, width int, selectedUID string) string {
	var b strings.Builder
	b.WriteString("REPLICA SET REVISIONS · UID-owned\nRetained subset; full identity in 5 Evidence\n\n")
	nameWidth := max(8, width-42)
	columns := []int{1, 5, nameWidth, 6, 6, 13}
	labels := []string{"", "REV", "REPLICA SET", "TOTAL", "READY", "TEMPLATE"}
	if width < 70 {
		columns = []int{1, 5, max(8, width-22), 13}
		labels = []string{"", "REV", "REPLICA SET", "TEMPLATE"}
	}
	fmt.Fprintln(&b, tableRow(labels, columns))
	for index := range snapshot.Revisions {
		r := &snapshot.Revisions[index]
		marker := " "
		if r.Identity.UID == selectedUID {
			marker = ">"
		}
		match := "differs spec"
		if !r.CurrentKnown {
			match = inspect.ObservationUnknown
		} else if r.Current {
			match = "matches spec"
		}
		values := []string{marker, rolloutKnown(r.Revision), r.Identity.Name, rolloutCount(r.Replicas), rolloutCount(r.Ready), match}
		if width < 70 {
			values = []string{marker, rolloutKnown(r.Revision), r.Identity.Name, match}
		}
		fmt.Fprintln(&b, tableRow(values, columns))
	}
	if len(snapshot.Revisions) == 0 {
		b.WriteString("[?] No UID-owned revision obtained; inspect collection coverage.\n")
	}
	b.WriteString("\nj/k selects a retained ReplicaSet; Enter compares its exact template.\n" +
		"Matching the current template does not prove the controller's chosen RS.\n" +
		"Revision number and creation age do not choose a recovery target.\n" +
		"Retained revisions can be pruned by revisionHistoryLimit.\n")
	return b.String()
}

func rolloutPods(snapshot *review.RolloutSnapshot, width int) string {
	var b strings.Builder
	b.WriteString("POD IMAGE EVIDENCE\nDeployment → controlling ReplicaSet UID → controlling Pod UID\n\n")
	if len(snapshot.Pods) == 0 {
		b.WriteString("[?] No owned Pod image records obtained; absence is not health.\n")
	}
	for index := range snapshot.Pods {
		pod := &snapshot.Pods[index]
		fmt.Fprintln(&b, fitInvestigation(pod.Identity.Name+" · "+rolloutKnown(pod.Phase), width))
		for _, image := range pod.Images {
			ready := "?"
			if image.Ready != nil {
				ready = "no"
				if *image.Ready {
					ready = "yes"
				}
			}
			fmt.Fprintln(&b, fitInvestigation("  "+image.Name+" ("+image.Role+") · "+rolloutKnown(image.State)+" · ready "+ready, width))
			fmt.Fprintln(&b, fitInvestigation("  declared: "+rolloutKnown(image.Declared), width))
			fmt.Fprintln(&b, fitInvestigation("  observed imageID: "+rolloutKnown(image.ImageID), width))
			if image.Reason != "" {
				fmt.Fprintln(&b, fitInvestigation("  reason: "+image.Reason, width))
			}
		}
		b.WriteByte('\n')
	}
	b.WriteString("Tags, controller revisions and runtime imageID are separate sources.\n" +
		"An imageID is reported by Pod status; the review does not resolve tags.\n" +
		"5 evidence retains full image identifiers, source identities and gaps.\n")
	return b.String()
}

func rolloutRecovery(snapshot *review.RolloutSnapshot, revisionUID string, width int) string {
	var b strings.Builder
	b.WriteString("RECOVERY CANDIDATE · NOT EXECUTED\n")
	preview := review.RolloutRecoveryPreview(snapshot, revisionUID)
	if preview.RevisionIdentity.UID == "" {
		fmt.Fprintln(&b, "[?] "+preview.Reason)
		b.WriteString("2 revisions · j/k choose ReplicaSet · Enter reviews its template.\n")
	} else {
		fmt.Fprintln(&b, fitInvestigation("Preview RS "+preview.RevisionIdentity.Name+" · revision "+rolloutKnown(preview.Revision), width))
		fmt.Fprintln(&b, fitInvestigation("UID "+preview.RevisionIdentity.UID+" · context "+preview.RevisionIdentity.Context, width))
		b.WriteString("A: current Deployment template · B: selected retained RS template\n")
		if preview.Comparison.Comparable {
			fmt.Fprintf(&b, "\n%d changed retained template fields\n", len(preview.Comparison.Changes))
			if len(preview.Comparison.Changes) == 0 {
				b.WriteString("No differences in reviewable retained fields; excluded fields remain unknown.\n")
			}
			for _, change := range preview.Comparison.Changes {
				fmt.Fprintln(&b, fitInvestigation(strings.ToUpper(change.Kind)+" "+change.Path, width))
				before, after := comparisonValues(change.Before, change.After)
				fmt.Fprintln(&b, fitInvestigation("  A "+before, width))
				fmt.Fprintln(&b, fitInvestigation("  B "+after, width))
			}
			if preview.Comparison.Truncated {
				b.WriteString("[~] Template comparison truncated; changes are incomplete.\n")
			}
		} else {
			fmt.Fprintln(&b, "[?] "+preview.Reason)
		}
	}
	b.WriteString("\nPREVIEW LIMITS\n")
	for _, limit := range preview.Limits {
		fmt.Fprintln(&b, limit)
	}
	b.WriteString("External ConfigMap/Secret contents are not historical template data.\n" +
		"Nothing was submitted; this is not a rollback or server dry-run plan.\n")
	return b.String()
}

func rolloutEvidence(snapshot *review.RolloutSnapshot) string {
	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return "[?] Retained rollout evidence unavailable: " + err.Error()
	}
	text := "ROLLOUT SOURCE EVIDENCE\nKubernetes API · retained typed facts and sanitized templates\n" +
		"Each template retains its context, UID, observation time and exclusions.\n" +
		"Sensitive fields are redacted heuristically; excluded fields remain unreviewed.\n\n" + string(encoded)
	if len(text) > inspect.MaxComparisonText {
		text = text[:inspect.MaxComparisonText] + "\n[~] Evidence display capped at 256 KiB; source projection is incomplete here.\n"
	}
	return text
}

func rolloutCoverageLine(coverage []review.RolloutCoverage, width int) string {
	parts := make([]string, 0, len(coverage))
	// Put collection gaps before successful sources so narrow views retain them.
	for _, complete := range []bool{false, true} {
		for _, c := range coverage {
			if (c.State == inspect.ObservationComplete) == complete {
				parts = append(parts, c.Source+": "+c.State)
			}
		}
	}
	return fitInvestigation("Read coverage · "+strings.Join(parts, " · "), width)
}

func rolloutCount(value *int64) string {
	if value == nil {
		return "?"
	}
	return fmt.Sprint(*value)
}

func rolloutKnown(value string) string {
	if value == "" {
		return inspect.ObservationUnknown
	}
	return value
}

func rolloutMarkup(app *App, text string) string {
	p := app.Styles.Semantic()
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		switch {
		case strings.HasPrefix(line, "[!] "):
			lines[index] = detailStyled(p.Failure.String(), "b", line)
		case strings.HasPrefix(line, "[+] "):
			lines[index] = detailStyled(p.Healthy.String(), "b", line)
		case strings.HasPrefix(line, "[~] "):
			lines[index] = detailStyled(p.Warning.String(), "", line)
		case strings.HasPrefix(line, "[?] "):
			lines[index] = detailStyled(p.Unknown.String(), "", line)
		case strings.HasPrefix(line, ">"):
			lines[index] = detailStyled(p.Focus.String(), "b", line)
		case line == "ROLLOUT OBSERVATION" || strings.HasPrefix(line, "REPLICA COUNTS") || line == "CONTROLLER CONDITIONS" ||
			line == "RETAINED WORKLOAD EVIDENCE" || line == "VISIBILITY" || line == "REPLICA SET REVISIONS" ||
			line == "POD IMAGE EVIDENCE" || line == "RECOVERY CANDIDATE · NOT EXECUTED" || line == "PREVIEW LIMITS" || line == "ROLLOUT SOURCE EVIDENCE":
			lines[index] = detailStyled(p.Focus.String(), "b", line)
		default:
			lines[index] = wbText(line)
		}
	}
	return strings.Join(lines, "\n")
}

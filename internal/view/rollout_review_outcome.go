// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/review"
	"github.com/derailed/tcell/v2"
)

func (v *rolloutReviewView) toggleOutcome(event *tcell.EventKey) *tcell.EventKey {
	if v.cmdBuff.IsActive() {
		return event
	}
	if v.following {
		v.stopOutcome()
		v.render()
		return nil
	}
	if !v.destinationCurrent() || v.snapshot == nil || v.snapshot.Generation == nil || v.snapshot.TemplateSHA256 == "" || v.follower == nil || v.loading {
		v.app.Flash().Warn("Capture a current controller identity, generation and template before following")
		return nil
	}
	// This entry is read-only. Only an accepted operation receipt may supply
	// AcceptedAt/OperationID in the separate recovery execution path.
	request := &review.RolloutOutcomeRequest{Identity: v.snapshot.Identity, Generation: *v.snapshot.Generation, TemplateSHA256: v.snapshot.TemplateSHA256}
	v.beginOutcome(request)
	return nil
}

func (v *rolloutReviewView) beginOutcome(request *review.RolloutOutcomeRequest) {
	v.stopOutcome()
	v.followGeneration++
	generation, destinationRevision := v.followGeneration, v.destinationRevision
	ctx, cancel := context.WithCancel(context.Background())
	v.followCancel, v.following = cancel, true
	v.outcome = &review.RolloutOutcome{Identity: request.Identity, Generation: request.Generation, TemplateSHA256: request.TemplateSHA256,
		AcceptedAt: request.AcceptedAt, OperationID: request.OperationID, StartedAt: time.Now().UTC(), State: review.RolloutProgressing,
		Reason: "Starting bounded named controller observation"}
	v.selectTab(0)
	v.render()
	update := func(outcome *review.RolloutOutcome) {
		if !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if v.app.Content.Top() == v {
				v.acceptOutcome(generation, destinationRevision, outcome)
			}
		})
	}
	follower := v.follower
	go func() { defer cancel(); update(follower(ctx, request, update)) }()
}

func (v *rolloutReviewView) acceptOutcome(generation, destinationRevision uint64, outcome *review.RolloutOutcome) {
	if !v.active || outcome == nil || generation != v.followGeneration || destinationRevision != v.app.Config.DestinationRevision() || !v.destinationCurrent() {
		return
	}
	v.outcome = outcome
	if !outcome.FinishedAt.IsZero() {
		v.following = false
		v.followCancel = nil
	}
	v.render()
}

func (v *rolloutReviewView) stopOutcome() {
	v.followGeneration++
	if v.followCancel != nil {
		v.followCancel()
		v.followCancel = nil
	}
	if v.following && v.outcome != nil {
		retained := *v.outcome
		retained.State, retained.Reason, retained.FinishedAt = review.RolloutCanceled,
			"Following stopped; retained evidence remains; no rollback of accepted changes was performed", time.Now().UTC()
		v.outcome = &retained
	}
	v.following = false
}

func rolloutOutcomeText(outcome *review.RolloutOutcome, width int) string {
	start := outcome.StartedAt
	label := "READ-ONLY FOLLOW-UP · no write submitted"
	if !outcome.AcceptedAt.IsZero() {
		start, label = outcome.AcceptedAt, "ACCEPTED WRITE · controller outcome observed separately"
	}
	end := outcome.FinishedAt
	if end.IsZero() {
		end = time.Now()
	}
	elapsed := max(time.Duration(0), end.Sub(start)).Round(time.Second)
	text := "OUTCOME FOLLOW-UP\n" + fitInvestigation(label, width) + "\n"
	marker := "[?] "
	switch outcome.State {
	case review.RolloutComplete:
		marker = "[+] "
	case review.RolloutBlocked:
		marker = "[!] "
	case review.RolloutProgressing, review.RolloutPaused, review.RolloutManual:
		marker = "[~] "
	}
	text += fitInvestigation(marker+outcome.State+" · "+outcome.Reason, width) + "\n"
	text += fmt.Sprintf("Target generation %d · elapsed %s · %d named reads\n", outcome.Generation, elapsed, outcome.Reads)
	text += "5 Evidence: full reason, source, identity and controller follow-up\n"
	return text
}

func rolloutOutcomeEvidence(outcome *review.RolloutOutcome) string {
	encoded, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		return "[?] Outcome evidence encoding unavailable"
	}
	if len(encoded) > inspect.MaxComparisonText {
		return "OUTCOME FOLLOW-UP EVIDENCE\n" + string(encoded[:inspect.MaxComparisonText]) + "\n[~] Evidence display capped at 256 KiB; projection incomplete here.\n"
	}
	return "OUTCOME FOLLOW-UP EVIDENCE\nNamed controller GET; original children retain their original timestamps.\n" + string(encoded) + "\n"
}

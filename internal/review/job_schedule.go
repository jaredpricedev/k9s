// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // Make explicit IANA timezone previews available in minimal containers.

	"github.com/derailed/k9s/internal/inspect"
	"github.com/robfig/cron/v3"
)

const MaxJobSchedulePreview = 3

// JobSchedulePreview is a calculation from a retained schedule, never evidence
// that the controller executed or missed a run. It contains no past-run claim.
type JobSchedulePreview struct {
	State, TimeZone, Reason string
	Times                   []time.Time
	Assumptions             []string
}

func PreviewJobSchedule(expression, timeZone string, at time.Time, suspended bool) JobSchedulePreview {
	p := JobSchedulePreview{State: inspect.ObservationUnknown, TimeZone: timeZone, Assumptions: []string{
		"Future matching schedule times only; controller delay, concurrency and deadlines may prevent execution.",
		"Timezone database rules apply: nonexistent local times are skipped; repeated local times may match twice.",
		"Missing retained Jobs do not prove missed execution; TTL and history limits can remove completed Jobs.",
	}}
	if suspended {
		p.Assumptions = append(p.Assumptions, "Schedule is suspended: these times are conditional on resuming, not expected executions.")
	}
	if at.IsZero() {
		p.Reason = "Observation time unavailable; no reference time for preview"
		return p
	}
	if strings.HasPrefix(expression, "TZ=") || strings.HasPrefix(expression, "CRON_TZ=") || strings.HasPrefix(expression, "@every") {
		p.Reason = "Use spec.timeZone and a native calendar schedule; embedded timezone/interval expressions are not previewed"
		return p
	}
	if timeZone == "" {
		p.TimeZone = "UTC"
		p.Assumptions = append(p.Assumptions, "UTC reference preview: spec.timeZone is absent and the controller's local timezone is unknown.")
	}
	location, err := time.LoadLocation(p.TimeZone)
	if err != nil {
		p.Reason = fmt.Sprintf("Timezone unavailable: %s", rolloutText(err.Error()))
		return p
	}
	schedule, err := cron.ParseStandard("CRON_TZ=" + p.TimeZone + " " + expression)
	if err != nil {
		p.Reason = "Schedule unavailable: " + rolloutText(err.Error())
		return p
	}
	cursor := at.In(location)
	for range MaxJobSchedulePreview {
		cursor = schedule.Next(cursor)
		if cursor.IsZero() {
			p.State = inspect.ObservationIncomplete
			p.Reason = "No further matching time within the parser's bounded calendar search"
			return p
		}
		p.Times = append(p.Times, cursor)
	}
	p.State = inspect.ObservationComplete
	if timeZone == "" {
		p.State = inspect.ObservationIncomplete
		p.Reason = "Controller timezone unknown; UTC reference calculation only"
	}
	return p
}

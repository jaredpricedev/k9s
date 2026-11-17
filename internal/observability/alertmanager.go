// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Alert states reported by Alertmanager.
const (
	AlertActive      = "active"
	AlertSuppressed  = "suppressed" // silenced or inhibited
	AlertUnprocessed = "unprocessed"
)

// Alert is one Alertmanager alert with sanitized, bounded text.
type Alert struct {
	Fingerprint string
	Labels      map[string]string // alertname, severity, namespace, pod, ... as sent
	Annotations map[string]string // summary, description, runbook_url, ...
	State       string            // AlertActive, AlertSuppressed or AlertUnprocessed
	SilencedBy  []string          // silence IDs
	InhibitedBy []string          // fingerprints of inhibiting alerts
	StartsAt    time.Time
	EndsAt      time.Time
	UpdatedAt   time.Time
	Receivers   []string
	// GeneratorURL is the rule's expression link as sent, with any URL
	// userinfo removed. It is text to show; it is never contacted.
	GeneratorURL string
}

// cleanLink bounds a link taken from a source and removes URL userinfo, so a
// credential embedded in it is never shown or retained.
func cleanLink(raw string) string {
	raw = Clean(strings.TrimSpace(raw), maxText)
	u, err := url.Parse(raw)
	switch {
	case err != nil && strings.Contains(raw, "@"):
		return ""
	case err == nil && u.User != nil:
		u.User = nil
		return u.String()
	}
	return raw
}

// Name returns the alertname label.
func (a *Alert) Name() string { return a.Labels["alertname"] }

// Silenced and Inhibited report why a suppressed alert is not notifying.
func (a *Alert) Silenced() bool  { return len(a.SilencedBy) > 0 }
func (a *Alert) Inhibited() bool { return len(a.InhibitedBy) > 0 }

// AlertQuery filters GET /api/v2/alerts. The zero value asks for active,
// silenced and inhibited alerts with no label filter.
type AlertQuery struct {
	Matchers                          []Matcher // sent as Alertmanager filter matchers, values escaped
	HideActive, HideSilenced          bool
	HideInhibited, IncludeUnprocessed bool
}

// AlertsResult is the outcome of one Alerts read. StateComplete with no
// Alerts means Alertmanager holds no matching alert at ObservedAt; it does
// not mean the cluster is healthy.
type AlertsResult struct {
	Status
	Alerts      []Alert
	NotRetained int // alerts beyond MaxAlerts
}

// Alerts reads GET /api/v2/alerts once.
func (c *Client) Alerts(ctx context.Context, q AlertQuery) AlertsResult {
	res := AlertsResult{Status: Status{Kind: c.kind, Source: c.display, State: StateUnavailable}}
	if c.kind != KindAlertmanager {
		res.Reason = "not an alertmanager source"
		return res
	}
	params := url.Values{
		"active":      {strconv.FormatBool(!q.HideActive)},
		"silenced":    {strconv.FormatBool(!q.HideSilenced)},
		"inhibited":   {strconv.FormatBool(!q.HideInhibited)},
		"unprocessed": {strconv.FormatBool(q.IncludeUnprocessed)},
	}
	for _, m := range q.Matchers {
		text, err := matchers([]Matcher{m})
		if err != nil {
			res.Reason = "filter refused: " + err.Error()
			return res
		}
		params.Add("filter", text)
	}
	body, st := c.Get(ctx, "/api/v2/alerts", params)
	res.Status = st
	if st.State != StateComplete {
		return res
	}
	var raw []struct {
		Fingerprint  string
		Labels       map[string]string
		Annotations  map[string]string
		StartsAt     time.Time
		EndsAt       time.Time
		UpdatedAt    time.Time
		Receivers    []struct{ Name string }
		GeneratorURL string
		Status       struct {
			State       string
			SilencedBy  []string
			InhibitedBy []string
		}
	}
	if json.Unmarshal(body, &raw) != nil {
		res.State, res.Reason = StateUnavailable, "response is not an Alertmanager alert list"
		return res
	}
	for i := range raw {
		if len(res.Alerts) >= MaxAlerts {
			res.NotRetained++
			continue
		}
		r := &raw[i]
		a := Alert{Fingerprint: Clean(r.Fingerprint, 64), Labels: cleanLabels(r.Labels), Annotations: make(map[string]string),
			State: Clean(r.Status.State, 32), StartsAt: r.StartsAt.UTC(), EndsAt: r.EndsAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
		for k := range cleanLabels(r.Annotations) {
			a.Annotations[k] = Clean(r.Annotations[k], maxText)
			if strings.HasSuffix(strings.ToLower(k), "url") {
				a.Annotations[k] = cleanLink(r.Annotations[k])
			}
		}
		a.GeneratorURL = cleanLink(r.GeneratorURL)
		for _, id := range r.Status.SilencedBy {
			a.SilencedBy = append(a.SilencedBy, Clean(id, 64))
		}
		for _, id := range r.Status.InhibitedBy {
			a.InhibitedBy = append(a.InhibitedBy, Clean(id, 64))
		}
		for _, rc := range r.Receivers {
			a.Receivers = append(a.Receivers, Clean(rc.Name, 128))
		}
		res.Alerts = append(res.Alerts, a)
	}
	if res.NotRetained > 0 {
		res.State, res.Reason = StateIncomplete, fmt.Sprintf("%d alerts not retained (limit %d)", res.NotRetained, MaxAlerts)
	}
	return res
}

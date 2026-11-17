// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Annotation is one Grafana annotation. Author identity is not retained.
type Annotation struct {
	ID           int64
	Time         time.Time
	TimeEnd      time.Time // zero unless the annotation is a region
	Text         string
	Tags         []string
	DashboardUID string
	PanelID      int64
	// AlertName, NewState and PrevState are set on alert-state annotations.
	AlertName, NewState, PrevState string
}

// AnnotationQuery filters GET /api/annotations. From and To are required
// and at most MaxWindow apart. Tags are matched with Grafana's matchAny.
type AnnotationQuery struct {
	From, To     time.Time
	Tags         []string
	DashboardUID string
	Limit        int // 0 or more than MaxAnnotations means MaxAnnotations
}

// AnnotationsResult is the outcome of one Annotations read.
type AnnotationsResult struct {
	Status
	Annotations []Annotation // ascending by Time
	From, To    time.Time
}

// DashboardHit is one dashboard from GET /api/search.
type DashboardHit struct {
	UID, Title, FolderTitle string
	Path                    string // Grafana-relative path such as /d/uid/slug
	Tags                    []string
}

// DashboardsResult is the outcome of one SearchDashboards read.
type DashboardsResult struct {
	Status
	Dashboards []DashboardHit
}

func cleanList(in []string, limit int) []string {
	var out []string
	for _, s := range in {
		if len(out) >= maxLabels {
			break
		}
		out = append(out, Clean(s, limit))
	}
	return out
}

// Annotations reads GET /api/annotations once.
func (c *Client) Annotations(ctx context.Context, q AnnotationQuery) AnnotationsResult {
	res := AnnotationsResult{Status: Status{Kind: c.kind, Source: c.display, State: StateUnavailable}, From: q.From.UTC(), To: q.To.UTC()}
	if c.kind != KindGrafana {
		res.Reason = "not a grafana source"
		return res
	}
	if q.From.IsZero() || q.To.IsZero() || !q.To.After(q.From) || q.To.Sub(q.From) > MaxWindow {
		res.Reason = "choose a positive window of at most seven days"
		return res
	}
	limit := q.Limit
	if limit <= 0 || limit > MaxAnnotations {
		limit = MaxAnnotations
	}
	params := url.Values{"from": {strconv.FormatInt(q.From.UnixMilli(), 10)}, "to": {strconv.FormatInt(q.To.UnixMilli(), 10)},
		"limit": {strconv.Itoa(limit)}}
	for _, tag := range q.Tags {
		params.Add("tags", tag)
	}
	if len(q.Tags) > 0 {
		params.Set("matchAny", "true")
	}
	if q.DashboardUID != "" {
		if !grafanaUID.MatchString(q.DashboardUID) {
			res.Reason = "dashboard UID refused"
			return res
		}
		params.Set("dashboardUID", q.DashboardUID)
	}
	body, st := c.Get(ctx, "/api/annotations", params)
	res.Status = st
	if st.State != StateComplete {
		return res
	}
	var raw []struct {
		ID           int64
		Time         int64
		TimeEnd      int64
		Text         string
		Tags         []string
		DashboardUID string
		PanelID      int64
		AlertName    string
		NewState     string
		PrevState    string
	}
	if json.Unmarshal(body, &raw) != nil {
		res.State, res.Reason = StateUnavailable, "response is not a Grafana annotation list"
		return res
	}
	dropped := 0
	for i := range raw {
		r := &raw[i]
		if len(res.Annotations) >= limit {
			dropped++
			continue
		}
		a := Annotation{ID: r.ID, Time: time.UnixMilli(r.Time).UTC(), Text: Clean(r.Text, maxText), Tags: cleanList(r.Tags, 128),
			DashboardUID: Clean(r.DashboardUID, 64), PanelID: r.PanelID, AlertName: Clean(r.AlertName, 256),
			NewState: Clean(r.NewState, 32), PrevState: Clean(r.PrevState, 32)}
		if r.TimeEnd > r.Time {
			a.TimeEnd = time.UnixMilli(r.TimeEnd).UTC()
		}
		res.Annotations = append(res.Annotations, a)
	}
	sortAnnotations(res.Annotations)
	if dropped > 0 || len(raw) == limit {
		// Grafana truncates silently at the limit, so a full page cannot be
		// called complete.
		res.State, res.Reason = StateIncomplete, fmt.Sprintf("annotation limit %d reached; older or newer entries may exist", limit)
	}
	return res
}

func sortAnnotations(a []Annotation) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j].Time.Before(a[j-1].Time); j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// SearchDashboards reads GET /api/search?type=dash-db once. query is free
// text sent as a URL parameter; limit 0 or above MaxDashboards means
// MaxDashboards.
func (c *Client) SearchDashboards(ctx context.Context, query string, tags []string, limit int) DashboardsResult {
	res := DashboardsResult{Status: Status{Kind: c.kind, Source: c.display, State: StateUnavailable}}
	if c.kind != KindGrafana {
		res.Reason = "not a grafana source"
		return res
	}
	if limit <= 0 || limit > MaxDashboards {
		limit = MaxDashboards
	}
	params := url.Values{"type": {"dash-db"}, "limit": {strconv.Itoa(limit)}}
	if query != "" {
		params.Set("query", Clean(query, 200))
	}
	for _, tag := range tags {
		params.Add("tag", tag)
	}
	body, st := c.Get(ctx, "/api/search", params)
	res.Status = st
	if st.State != StateComplete {
		return res
	}
	var raw []struct {
		UID, Title, URL, FolderTitle string
		Tags                         []string
	}
	if json.Unmarshal(body, &raw) != nil {
		res.State, res.Reason = StateUnavailable, "response is not a Grafana search result"
		return res
	}
	for i := range raw {
		r := &raw[i]
		if len(res.Dashboards) >= limit {
			break
		}
		if !grafanaUID.MatchString(r.UID) {
			continue
		}
		res.Dashboards = append(res.Dashboards, DashboardHit{UID: r.UID, Title: Clean(r.Title, 200), FolderTitle: Clean(r.FolderTitle, 200),
			Path: Clean(r.URL, 300), Tags: cleanList(r.Tags, 128)})
	}
	if len(raw) >= limit {
		res.State, res.Reason = StateIncomplete, fmt.Sprintf("dashboard limit %d reached; more may exist", limit)
	}
	return res
}

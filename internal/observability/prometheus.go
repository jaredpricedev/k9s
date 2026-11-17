// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PromSeries is one retained series: sanitized labels and finite samples
// in ascending time order.
type PromSeries struct {
	Labels  map[string]string
	Samples []Sample
}

// Last returns the newest sample, if any.
func (s PromSeries) Last() (Sample, bool) {
	if len(s.Samples) == 0 {
		return Sample{}, false
	}
	return s.Samples[len(s.Samples)-1], true
}

// PromResult is the outcome of one instant or range query.
//
// StateComplete with zero Series means the source answered and matched
// nothing. That is missing data, not a healthy reading.
type PromResult struct {
	Status
	Query      string        // exact PromQL sent
	Start, End time.Time     // equal for an instant query
	Step       time.Duration // zero for an instant query
	Series     []PromSeries
	// NotRetained counts series dropped by the series or sample limit;
	// Rejected counts samples that were not finite, ordered or in window.
	NotRetained, Rejected int
	// Error is what the source itself said when it rejected the query (HTTP
	// 400 or 422), for example a PromQL parse error: its own words, without
	// control characters and bounded. It can repeat parts of the query that
	// was sent; it never holds a header, a token or an address.
	Error string
}

// maxPromError bounds PromResult.Error in characters.
const maxPromError = 400

// promRejection reads the error text out of the body of a rejected query.
func promRejection(body []byte) string {
	var raw struct {
		ErrorType string `json:"errorType"`
		Error     string `json:"error"`
	}
	if len(body) == 0 || json.Unmarshal(body, &raw) != nil || raw.Error == "" {
		return ""
	}
	text := strings.Join(strings.Fields(Clean(raw.Error, maxPromError)), " ")
	if raw.ErrorType != "" {
		text = Clean(raw.ErrorType, 40) + ": " + text
	}
	return text
}

func promTime(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixNano())/1e9, 'f', 3, 64)
}

// Query runs one instant query at the given time (zero means now).
func (c *Client) Query(ctx context.Context, e Expr, at time.Time) PromResult {
	if at.IsZero() {
		at = c.now()
	}
	at = at.UTC()
	res := PromResult{Status: Status{Kind: c.kind, Source: c.display, State: StateUnavailable}, Start: at, End: at}
	if !c.promReady(&res, e) {
		return res
	}
	body, st, rejection := c.get(ctx, "/api/v1/query", url.Values{"query": {res.Query}, "time": {promTime(at)}})
	res.Status, res.Error = st, promRejection(rejection)
	if st.State == StateComplete {
		c.decodeProm(body, &res)
	}
	return res
}

// QueryRange runs one range query. The step is StepFor(end-start); the
// window must be positive and at most MaxWindow.
func (c *Client) QueryRange(ctx context.Context, e Expr, start, end time.Time) PromResult {
	start, end = start.UTC(), end.UTC()
	res := PromResult{Status: Status{Kind: c.kind, Source: c.display, State: StateUnavailable}, Start: start, End: end}
	if !c.promReady(&res, e) {
		return res
	}
	if start.IsZero() || end.IsZero() || !end.After(start) || end.Sub(start) > MaxWindow {
		res.Reason = "choose a positive window of at most seven days"
		return res
	}
	res.Step = StepFor(end.Sub(start))
	body, st, rejection := c.get(ctx, "/api/v1/query_range", url.Values{
		"query": {res.Query}, "start": {promTime(start)}, "end": {promTime(end)},
		"step": {strconv.FormatFloat(res.Step.Seconds(), 'f', 0, 64)},
	})
	res.Status, res.Error = st, promRejection(rejection)
	if st.State == StateComplete {
		c.decodeProm(body, &res)
	}
	return res
}

func (c *Client) promReady(res *PromResult, e Expr) bool {
	if c.kind != KindPrometheus {
		res.Reason = "not a prometheus source"
		return false
	}
	if err := instant(e); err != nil {
		res.Reason = "query refused: " + err.Error()
		return false
	}
	res.Query = e.String()
	return true
}

func (c *Client) decodeProm(body []byte, res *PromResult) {
	var raw struct {
		Status string
		Data   struct {
			ResultType string
			Result     json.RawMessage
		}
	}
	unsupported := func() {
		res.State, res.Reason, res.Series = StateUnavailable, "response is not a Prometheus query result", nil
	}
	if json.Unmarshal(body, &raw) != nil || raw.Status != "success" {
		unsupported()
		return
	}
	type rawPromSeries struct {
		Metric map[string]string
		Value  []json.RawMessage
		Values [][]json.RawMessage
	}
	var series []rawPromSeries
	switch raw.Data.ResultType {
	case "matrix", "vector":
		if json.Unmarshal(raw.Data.Result, &series) != nil {
			unsupported()
			return
		}
	case "scalar":
		var pair []json.RawMessage
		if json.Unmarshal(raw.Data.Result, &pair) != nil {
			unsupported()
			return
		}
		series = []rawPromSeries{{Value: pair}}
	default:
		unsupported()
		return
	}
	lo, hi := float64(res.Start.UnixNano())/1e9-1, float64(res.End.UnixNano())/1e9+1
	total := 0
	for _, rs := range series {
		if len(res.Series) >= MaxSeries {
			res.NotRetained++
			continue
		}
		pairs := rs.Values
		if rs.Value != nil {
			pairs = [][]json.RawMessage{rs.Value}
		}
		if total+len(pairs) > MaxSamples {
			res.NotRetained++
			continue
		}
		out := PromSeries{Labels: cleanLabels(rs.Metric)}
		var previous float64
		for _, pair := range pairs {
			var ts float64
			var value string
			if len(pair) != 2 || json.Unmarshal(pair[0], &ts) != nil || json.Unmarshal(pair[1], &value) != nil ||
				math.IsNaN(ts) || ts < lo || ts > hi || (len(out.Samples) > 0 && ts <= previous) {
				res.Rejected++
				continue
			}
			n, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
				res.Rejected++
				continue
			}
			out.Samples = append(out.Samples, Sample{Timestamp: time.Unix(0, int64(ts*1e9)).UTC(), Value: n})
			previous = ts
		}
		total += len(out.Samples)
		res.Series = append(res.Series, out)
	}
	if res.NotRetained > 0 {
		res.State = StateIncomplete
		res.Reason = fmt.Sprintf("%d series not retained (limits: %d series, %d samples)", res.NotRetained, MaxSeries, MaxSamples)
	}
}

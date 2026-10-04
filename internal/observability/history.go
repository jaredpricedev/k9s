// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
// Package observability retains bounded, explicitly requested Pod-name history.
package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/provider"
)

const MaxBytes = 1 << 20
const MaxSeries = 32
const MaxSamples = 4096

type Request struct {
	Scope      provider.Scope
	URL        string
	Start, End time.Time
	Step       time.Duration
}
type Sample struct {
	Timestamp time.Time
	Value     float64
}
type Series struct {
	Namespace, Pod, Container string
	Samples                   []Sample
	Gaps                      int
}
type Evidence struct {
	WindowMismatch     bool
	TargetMismatch     bool
	Query, Unit, State string
	Series             []Series
	Gaps               int
}
type Snapshot struct {
	Request    Request
	ObservedAt time.Time
	Evidence   []Evidence
}

// Validate parses the original URL before any sanitizing. Credentials are unsupported.
func Validate(r *Request) error {
	if r.Scope.Context == "" || r.Scope.GVR != "v1/pods" || r.Scope.TargetNamespace == "" || r.Scope.Name == "" || r.Scope.UID == "" {
		return errors.New("select a native Pod with captured identity")
	}
	for _, s := range []string{r.Scope.Name, r.Scope.TargetNamespace} {
		if len(s) > 253 || strings.ContainsAny(s, "\r\n\x00") {
			return errors.New("invalid Pod scope")
		}
	}
	if len(r.URL) > 2048 {
		return errors.New("provider URL exceeds limit")
	}
	u, err := url.Parse(r.URL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || strings.Contains(r.URL, "#") {
		return errors.New("use an HTTP(S) base URL without credentials, query or fragment")
	}
	if r.Start.IsZero() || r.End.IsZero() || !r.End.After(r.Start) || r.End.Sub(r.Start) > 24*time.Hour {
		return errors.New("choose a positive UTC window of at most 24 hours")
	}
	if !time.Unix(0, r.Start.UnixNano()).Equal(r.Start) || !time.Unix(0, r.End.UnixNano()).Equal(r.End) {
		return errors.New("requested dates outside supported timestamp range")
	}
	// At most 256 requested timestamps per series; global sample cap still applies.
	r.Step = time.Duration(math.Ceil(r.End.Sub(r.Start).Seconds()/255)) * time.Second
	if r.Step < 15*time.Second {
		r.Step = 15 * time.Second
	}
	r.URL = strings.TrimRight(u.String(), "/")
	return nil
}

func Queries(r *Request) []string {
	selector := fmt.Sprintf("{namespace=%s,pod=%s}", strconv.Quote(r.Scope.TargetNamespace), strconv.Quote(r.Scope.Name))
	return []string{"container_cpu_usage_seconds_total" + selector, "container_memory_working_set_bytes" + selector}
}

//nolint:gocritic // Own a request value so normalization cannot mutate the caller's captured destination/window.
func Collect(ctx context.Context, r Request) (*Snapshot, error) {
	if err := Validate(&r); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Do not inherit proxy credentials or an unrelated endpoint mapping.
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	s := &Snapshot{Request: r, ObservedAt: time.Now().UTC()}
	totalSeries, totalSamples := 0, 0
	for i, q := range Queries(&r) {
		e := Evidence{Query: q, Unit: []string{"cumulative CPU seconds", "memory working-set bytes"}[i], State: "unknown"}
		u, _ := url.Parse(r.URL + "/api/v1/query_range")
		params := url.Values{
			"query": {q},
			"start": {strconv.FormatFloat(float64(r.Start.UnixNano())/1e9, 'f', 9, 64)},
			"end":   {strconv.FormatFloat(float64(r.End.UnixNano())/1e9, 'f', 9, 64)},
			"step":  {strconv.FormatFloat(r.Step.Seconds(), 'f', 0, 64)},
		}
		u.RawQuery = params.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
		if err != nil {
			return nil, errors.New("provider request unavailable")
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("provider unavailable")
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
		resp.Body.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if readErr != nil {
			return nil, errors.New("provider response unavailable")
		}
		if len(body) > MaxBytes {
			return nil, errors.New("provider response exceeds limit")
		}
		if resp.StatusCode != http.StatusOK {
			switch resp.StatusCode {
			case 401, 403:
				e.State = "denied"
			case 404:
				e.State = "absent"
			default:
				e.State = "unavailable"
			}
			s.Evidence = append(s.Evidence, e)
			continue
		}
		var raw struct {
			Status   string
			Warnings []json.RawMessage
			Data     struct {
				ResultType string
				Result     []rawSeries
			}
		}
		if json.Unmarshal(body, &raw) != nil || raw.Status != "success" || raw.Data.ResultType != "matrix" {
			return nil, errors.New("provider history response unsupported")
		}
		totalSeries += len(raw.Data.Result)
		if totalSeries > MaxSeries {
			return nil, errors.New("provider series exceeds limit")
		}
		e.State = "partial" // A successful matrix cannot establish exhaustive history.
		if len(raw.Data.Result) == 0 {
			e.State = "empty / unknown coverage"
		}
		for _, rs := range raw.Data.Result {
			totalSamples += len(rs.Values)
			if totalSamples > MaxSamples {
				return nil, errors.New("provider samples exceeds limit")
			}
			retainSeries(&r, rs, &e)
		}
		s.Evidence = append(s.Evidence, e)
	}
	return s, nil
}

type rawSeries struct {
	Metric map[string]string
	Values [][]json.RawMessage
}

func retainSeries(r *Request, rs rawSeries, e *Evidence) {
	if rs.Metric["namespace"] != r.Scope.TargetNamespace || rs.Metric["pod"] != r.Scope.Name {
		e.TargetMismatch = true
		e.Gaps++
		e.State = "partial / target mismatch"
		return
	}
	container := rs.Metric["container"]
	if len(container) > 253 || strings.ContainsAny(container, "\r\n\x1b") {
		e.Gaps++
		return
	}
	series := Series{Namespace: r.Scope.TargetNamespace, Pod: r.Scope.Name, Container: container}
	previous := r.Start
	for _, pair := range rs.Values {
		if len(pair) != 2 {
			e.Gaps++
			continue
		}
		var ts float64
		var value string
		if json.Unmarshal(pair[0], &ts) != nil || json.Unmarshal(pair[1], &value) != nil || math.IsNaN(ts) || math.IsInf(ts, 0) ||
			ts < float64(r.Start.UnixNano())/1e9 || ts > float64(r.End.UnixNano())/1e9 {
			if !math.IsNaN(ts) && !math.IsInf(ts, 0) && (ts < float64(r.Start.UnixNano())/1e9 || ts > float64(r.End.UnixNano())/1e9) {
				e.WindowMismatch = true
			}
			e.Gaps++
			continue
		}
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			e.Gaps++
			continue
		}
		at := time.Unix(0, int64(ts*1e9)).UTC()
		if len(series.Samples) > 0 && !at.After(previous) {
			e.Gaps++
			continue
		}
		if at.Sub(previous) > r.Step*2 {
			series.Gaps++
		}
		series.Samples = append(series.Samples, Sample{Timestamp: at, Value: n})
		previous = at
	}
	if r.End.Sub(previous) > r.Step*2 {
		series.Gaps++
	}
	e.Gaps += series.Gaps
	e.Series = append(e.Series, series)
}

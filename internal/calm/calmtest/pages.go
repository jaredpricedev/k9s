// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package calmtest holds the three reference pages of the calm layout,
// built with the kit from invented data: the calmer :slow, the calmer Pod
// graph and the calmer Pod comparison. They are what the kit is tested
// against and what a view is modelled on. Nothing here reads a cluster.
package calmtest

import (
	"time"

	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/chart"
)

// Day is the invented day of the reference pages.
var Day = time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)

func at(hour, minute int) time.Time {
	return Day.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
}

// Stalls are the three episodes of the hang pages, as minutes past 14:00.
var Stalls = [][2]int{{8, 11}, {27, 31}, {53, 55}}

func stalls() []chart.Interval {
	out := make([]chart.Interval, len(Stalls))
	for i, s := range Stalls {
		out[i] = chart.Interval{Start: at(14, s[0]), End: at(14, s[1])}
	}
	return out
}

// hour is one sample a minute from 14:01 to 15:00: peak for a minute of a
// stall, base outside. A sample stands for the minute that ends at it, so
// the one taken at 14:09 is the first of the stall that began at 14:08.
func hour(base, peak float64) []chart.Point {
	out := make([]chart.Point, 0, 60)
	for minute := 1; minute <= 60; minute++ {
		v := base
		for _, s := range Stalls {
			if minute > s[0] && minute <= s[1] {
				v = peak
			}
		}
		out = append(out, chart.Point{T: at(14, minute), V: v})
	}
	return out
}

func over(limit float64) func(float64) bool {
	return func(v float64) bool { return v > limit }
}

var hourFrame = chart.Frame{Start: at(14, 0), End: at(15, 0), Step: time.Minute}

// Slow is the calmer :slow: three stalls, and what lines up with them.
func Slow(o calm.Options) calm.Page {
	p := o.Palette
	dropped := []chart.Point{}
	for minute := 1; minute <= 60; minute++ {
		v := 0.0
		if minute == 20 {
			v = 0.4
		}
		dropped = append(dropped, chart.Point{T: at(14, minute), V: v})
	}
	return calm.Page{
		Title: calm.Title{View: "Slow requests", Object: "Service gitlab-webservice-default", Window: "last 1h",
			Read: at(14, 57), Done: 35, Total: 35},
		Verdict: calm.Verdict{
			Sentences: []string{
				"3 stalls: clients gave up waiting at 14:08, 14:27 and 14:53, for 2 to 4 minutes each.",
				"Each time, pod hx4mt was CPU-throttled and had no free threads. Nothing else lines up.",
			},
			Caveat: "Overlap in time, not proof of cause.",
		},
		Frame: hourFrame,
		Bands: calm.Bands{Label: "stalls", Episodes: stalls()},
		Groups: []calm.Group{
			{Label: "LINES UP WITH THE STALLS", Color: p.Warn, Rows: []calm.Row{
				{Subject: "ingress", What: "clients gave up (499)", Number: "6.3%", Compare: "of requests", Color: p.Warn,
					Strip: calm.Levels(hour(0.002, 0.063), over(0.01))},
				{Subject: "pod hx4mt", What: "response time p99", Number: "47.9s", Compare: "peers 0.35s", Color: p.Fail, Selected: true,
					Strip: calm.Levels(hour(0.35, 47.9), over(1))},
				{Subject: "pod hx4mt", What: "CPU throttled", Number: "88%", Compare: "peers 1%", Color: p.Fail,
					Strip: calm.Levels(hour(0.01, 0.88), over(0.25))},
				{Subject: "pod hx4mt", What: "free threads", Number: "0", Compare: "of 4", Color: p.Fail,
					Strip: calm.Levels(hour(0, 4), over(3))},
				{Subject: "pod hx4mt", What: "not ready", Number: "twice", Compare: "up to 3m", Color: p.Fail,
					Strip: calm.Held(chart.Interval{Start: at(14, 9), End: at(14, 11)}, chart.Interval{Start: at(14, 28), End: at(14, 31)})},
			}},
			{Label: "DIFFERS, BUT NOT DURING A STALL", Role: calm.Secondary, Rows: []calm.Row{
				{Subject: "network", What: "packets dropped", Number: "0.4/s", Compare: "to pod 2bq7k", Color: p.Info,
					Strip: calm.Levels(dropped, over(0.1))},
			}},
			{Label: "QUIET", Role: calm.QuietRole, Rows: []calm.Row{
				{Subject: "pods 2bq7k, 8wzpd", What: "response time p99", Number: "0.35s"},
				{Subject: "postgresql · redis · gitaly", What: "latency, errors", Number: "normal"},
				{Subject: "dns", What: "lookup time p99", Number: "6ms"},
			}},
		},
		Next: []calm.Action{{Key: "B", Label: "probe the pods now"}, {Key: "m", Label: "compare the pods"},
			{Key: "Enter", Label: "open hx4mt"}, {Key: "D", Label: "open in Grafana"}},
		Keys: []string{"↑↓ row", "←→ time", "1-4 window", "a show every signal", "s queries", "Esc back"},
	}
}

// SlowUnread is the calmer :slow when a source did not answer: the
// verdict says so, and the source is listed as not read.
func SlowUnread(o calm.Options) calm.Page {
	page := Slow(o)
	page.Title.Done = 33
	page.Verdict = calm.Nothing("No stall in the last 1h in what was read.", 33, 35)
	page.Bands.Episodes = nil
	page.Groups = []calm.Group{
		{Label: "QUIET", Role: calm.QuietRole, Rows: []calm.Row{
			{Subject: "ingress", What: "clients gave up (499)", Number: "0%"},
			{Subject: "pods hx4mt, 2bq7k, 8wzpd", What: "response time p99", Number: "0.35s"},
		}},
		{Label: "NOT READ", Role: calm.NotReadRole, Color: o.Palette.Warn, Rows: []calm.Row{
			{Subject: "postgresql", What: "no latency metric found"},
			{Subject: "dns", What: "query timed out after 10s"},
		}},
	}
	return page
}

const mebi = 1 << 20

// Graph is the calmer Pod graph: one main chart and three strips under it
// on the same axis.
func Graph(o calm.Options) calm.Page {
	p := o.Palette
	frame := chart.Frame{Start: at(14, 45), End: at(15, 0), Step: 10 * time.Second}
	rollout := at(14, 48)
	var memory, cpu, network []chart.Point
	var restarts []time.Time
	for step := range 91 {
		t := frame.Start.Add(time.Duration(step) * frame.Step)
		if !t.After(rollout) {
			continue
		}
		// Memory climbs to the limit and falls back when the container
		// restarts, six times.
		phase := (step - 18) % 12
		v := 150.0 + float64(phase)*10.5
		if phase >= 10 {
			v = 254
		}
		if phase == 11 {
			restarts = append(restarts, t)
		}
		memory = append(memory, chart.Point{T: t, V: v * mebi})
		cpu = append(cpu, chart.Point{T: t, V: 0.34})
		network = append(network, chart.Point{T: t, V: 9000})
	}
	level := func(v float64) []chart.Point {
		out := make([]chart.Point, 0, len(memory))
		for _, point := range memory {
			out = append(out, chart.Point{T: point.T, V: v})
		}
		return out
	}
	panel := chart.Panel{Unit: chart.Bytes, Ceiling: 288 * mebi, Series: []chart.Series{
		{Name: "limit", Kind: chart.Reference, Limit: true, Color: p.Fail, Glyph: '─', ASCII: '-', Points: level(256 * mebi)},
		{Name: "request", Kind: chart.Reference, Color: p.Muted, Glyph: '┄', ASCII: '.', Points: level(128 * mebi)},
		{Name: "memory", Color: p.Accent, Points: memory},
	}}
	head := "  " + calm.HeadingText("MEMORY", p.Accent, o) + "  254Mi now  at limit   - limit 256Mi   . request 128Mi"
	return calm.Page{
		Title: calm.Title{View: "Graph", Object: "Pod checkout-api-7d9f8c6b5d-x2x9q", Window: "last 15m",
			Read: at(15, 0), Done: 10, Total: 10},
		Verdict: calm.Verdict{
			Sentences: []string{"Memory reached its 256Mi limit 6 times.", "The container restarted after each."},
			Caveat:    "Order in time, not proof of cause.",
		},
		Frame: frame,
		Bands: calm.Bands{Marks: []chart.Mark{{Key: 'R', T: rollout, Label: "rollout to rev 2 at 14:48 · memory limit changed 512Mi → 256Mi"}}},
		Chart: &calm.Chart{Head: head, Gutter: panel.GutterWidth(), Draw: func(f chart.Frame, gutter, height int) []string {
			return panel.Render(f, height, gutter, chart.Options{ASCII: o.ASCII, Cursor: -1, AxisColor: p.Muted, WarnColor: p.Warn})
		}},
		NumberAfter: true,
		Groups: []calm.Group{
			{Label: "ALSO", Role: calm.Secondary, Rows: []calm.Row{
				{What: "restarts", Number: "6", Color: p.Warn, Strip: calm.Events(restarts...).Unknown(chart.Interval{Start: frame.Start, End: rollout})},
				{What: "CPU", Number: "340m", Color: p.Accent, Strip: calm.Trend(cpu).Top(1)},
				{What: "network", Number: "9kB/s", Color: p.Accent, Strip: calm.Trend(network).Top(40000)},
			}},
		},
		Next: []calm.Action{{Key: "n", Label: "next chart (CPU)"}, {Key: "a", Label: "all charts"},
			{Key: "t", Label: "what changed"}, {Key: "D", Label: "open in Grafana"}},
		Keys: []string{"←→ time", "1-4 window", "s queries", "Esc back"},
	}
}

// Compare is the calmer Pod comparison: one group per signal, one row per
// Pod, the Pod that differs in its colour.
func Compare(o calm.Options) calm.Page {
	p := o.Palette
	signal := func(label string, peak, base, limit float64, odd, peer string) calm.Group {
		quiet := calm.Levels(hour(base, base), over(limit)).Top(peak)
		return calm.Group{Label: label, Color: p.Accent, Rows: []calm.Row{
			{Subject: "hx4mt", What: "worker-f", Number: odd, Tag: "differs", Color: p.Fail, Strip: calm.Levels(hour(base, peak), over(limit)).Top(peak)},
			{Subject: "2bq7k", What: "worker-d", Number: peer, Strip: quiet},
			{Subject: "8wzpd", What: "worker-e", Number: peer, Strip: quiet},
		}}
	}
	response := signal("RESPONSE TIME p99", 47.9, 0.35, 1, "47.9s", "0.35s")
	response.Head = "peak"
	return calm.Page{
		Title: calm.Title{View: "Compare pods", Object: "Deployment gitlab-webservice-default · 3 pods", Window: "last 1h",
			Read: at(14, 57), Done: 13, Total: 13},
		Verdict: calm.Verdict{
			Sentences: []string{"Pod hx4mt differs from the other two in 4 signals.", "The others match each other."},
			Caveat:    "A difference, not proof of cause.",
		},
		Frame:       hourFrame,
		Bands:       calm.Bands{Label: "stalls", Episodes: stalls()},
		NumberAfter: true,
		Groups: []calm.Group{
			response,
			signal("CPU THROTTLED", 0.88, 0.01, 0.25, "88%", "1%"),
			signal("REQUESTS WAITING FOR A THREAD", 40, 0, 5, "40", "0"),
			{Label: "ALSO DIFFERS", Role: calm.Secondary, Summary: []string{"CPU usage (1.5 cores, peers 0.4)"}},
			{Label: "SAME ON ALL 3", Role: calm.QuietRole, Summary: []string{"memory", "restarts", "errors", "network"}},
		},
		Next: []calm.Action{{Key: "Enter", Label: "open hx4mt"}, {Key: "g", Label: "group by node / revision / zone"},
			{Key: "B", Label: "probe the pods now"}},
		Keys: []string{"↑↓ signal", "←→ time", "1-4 window", "a show every signal", "s queries", "Esc back"},
	}
}

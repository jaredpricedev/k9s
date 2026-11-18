// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package impact

import (
	"strconv"
	"strings"
)

// Answer is what a comparison came to in plain sentences, for the top of a
// page whose rows carry every before and after value. No sentence repeats
// one of those values and none names a cause: each states how many signals
// moved after the rollout, in time order, and what the revision changed.
type Answer struct {
	// Lead states the comparison: how many signals got worse after the
	// rollout and which kinds they are (the signals themselves when they
	// are of one kind), or that none did and how the compared ones are
	// counted, or that nothing could be compared and why. Brief is Lead
	// without the kinds or the counts, for a page whose rows and groups
	// state them. What is missing is never left out of either: a signal
	// that was not compared stays counted in Brief.
	Lead, Brief string
	// Traffic states that the request rate changed so much that error
	// ratio and latency are not like-for-like, with both rates;
	// TrafficBrief says it with the relative change alone. Both are empty
	// when the rate stayed within TrafficShift.
	Traffic, TrafficBrief string
	// Changed states what the revision changed in its pod template: the
	// kinds of change and how many of each, with the memory limit as
	// before -> after. ChangedBrief leaves those two values out. The list
	// itself is Report.Changes.
	Changed, ChangedBrief string
}

// signalKinds are the words a signal goes by in the lead sentence. The
// three latency percentiles are one kind.
var signalKinds = map[string]string{
	SigRequests: "request rate", SigErrors: "errors", SigP50: "latency", SigP95: "latency", SigP99: "latency",
	SigRestarts: "restarts", SigMemory: "memory peak", SigMemLimit: "memory", SigCPU: "CPU usage",
	SigThrottled: "CPU throttling", SigReady: "ready replicas",
}

// inSeries joins words as a sentence lists them: "a", "a and b", "a, b and c".
func inSeries(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// when is the time of the rollout as a sentence states it: "at 04:29", and
// with the day when the read was on another one.
func (r *Report) when() string {
	at := r.Windows.Rollout.UTC()
	if read := r.ReadAt.UTC(); !r.ReadAt.IsZero() && (read.Year() != at.Year() || read.YearDay() != at.YearDay()) {
		return "on " + at.Format("Jan02") + " at " + at.Format("15:04")
	}
	return "at " + at.Format("15:04")
}

// Answer states the comparison in sentences. It uses the verdicts the
// comparison already came to and adds no rule of its own.
func (r *Report) Answer() Answer {
	var out Answer
	out.Changed, out.ChangedBrief = r.changed()
	if requests := r.Outcome(SigRequests); r.Traffic != "" && requests != nil {
		out.Traffic = r.Traffic
		out.TrafficBrief = "Request rate " + PercentChange(requests.Before.Value, requests.After.Value) + ": errors and latency are not like-for-like."
	}
	out.Lead, out.Brief = r.lead()
	return out
}

// verdictCount is the outcomes of a comparison counted by what they came
// to.
type verdictCount struct {
	// worse, better and same are the directional verdicts and unchanged;
	// moved is the neutral signals that are only higher or lower.
	worse, better, moved, same int
	// states counts the outcomes that have no verdict by their state.
	states map[string]int
	// kinds are the kinds of signal that got worse, in the order of the
	// signals, and phrases those signals.
	kinds, phrases []string
	// label is the name of the last signal that got worse as its row has
	// it, and shared says its kind is one that other signals have too.
	label  string
	shared bool
}

func (r *Report) verdicts() verdictCount {
	count := verdictCount{states: map[string]int{}}
	seen := map[string]bool{}
	for index := range r.Outcomes {
		outcome := &r.Outcomes[index]
		switch {
		case outcome.State != Compared:
			count.states[outcome.State]++
		case outcome.Verdict == Worse:
			count.worse++
			kind := signalKinds[outcome.Signal.ID]
			if kind == "" {
				kind = outcome.Signal.Phrase
			}
			if !seen[kind] {
				seen[kind] = true
				count.kinds = append(count.kinds, kind)
			}
			count.phrases = append(count.phrases, outcome.Signal.Phrase)
			count.label, count.shared = outcome.Signal.Label, false
			for id, other := range signalKinds {
				count.shared = count.shared || other == kind && id != outcome.Signal.ID
			}
		case outcome.Verdict == Better:
			count.better++
		case outcome.Verdict == Unchanged:
			count.same++
		default:
			count.moved++
		}
	}
	return count
}

// lead is the sentence of what the comparison came to, with and without
// the kinds and counts a page states elsewhere.
func (r *Report) lead() (lead, brief string) {
	rev := r.Rollout.Label()
	around := "around " + rev + ", rolled out " + r.when()
	if r.Failure != "" {
		// Nothing was read: the reason is the answer, at any width.
		lead = "Nothing was read " + around + ": " + strings.TrimRight(r.Failure, ".") + "."
		return lead, lead
	}
	count := r.verdicts()
	worse, better, moved, same, states, kinds := count.worse, count.better, count.moved, count.same, count.states, count.kinds
	uncompared := states[NotRead] + states[NotAvailable] + states[TooFewSamples]
	after := " after " + rev + " rolled out " + r.when()
	switch {
	case worse+better+moved+same == 0:
		// Missing data, said as that: how many signals, and in which way.
		var why []string
		for _, state := range []struct{ name, words string }{
			{NotRead, "not read"}, {NotAvailable, "not available"}, {TooFewSamples, "with too few samples before or after"},
		} {
			if n := states[state.name]; n > 0 {
				why = append(why, strconv.Itoa(n)+" "+state.words)
			}
		}
		brief = "No signal could be compared " + around + "."
		lead = brief
		if len(why) > 0 {
			lead = strings.TrimSuffix(brief, ".") + ": " + strings.Join(why, ", ") + "."
		}
	case worse == 0:
		// Nothing worse is not health: the sentence counts what was
		// compared and never drops what was not.
		head := "No compared signal got worse" + after
		var counts []string
		for _, part := range []struct {
			n     int
			words string
		}{{better, "better"}, {moved, "higher or lower"}, {same, "unchanged"}, {uncompared, "not compared"}} {
			if part.n > 0 {
				counts = append(counts, strconv.Itoa(part.n)+" "+part.words)
			}
		}
		lead = head + ": " + strings.Join(counts, ", ") + "."
		brief = head + "."
		if uncompared > 0 {
			brief = head + "; " + strconv.Itoa(uncompared) + " not compared."
		}
	case len(kinds) == 1:
		// One kind is said by its name: "Latency got worse". A kind that
		// stands for several signals is said by the name of the one that
		// got worse when the others did not: one percentile is not
		// "latency", and the short form names no other.
		name := kinds[0]
		if worse == 1 && count.shared {
			name = count.label
		}
		head := strings.ToUpper(name[:1]) + name[1:] + " got worse" + after
		lead, brief = head+".", head+"."
		if worse > 1 {
			lead = head + ": " + inSeries(count.phrases) + "."
		}
	default:
		head := strconv.Itoa(worse) + " signals got worse" + after
		lead = head + ": " + inSeries(kinds) + "."
		brief = head + "."
	}
	return lead, brief
}

// changeTally counts the entries of Report.Changes by kind.
type changeTally struct {
	images, memoryLimits, resources, env, commands, added, removed, other int
	envFrom                                                               bool
	// resource is the one resource setting other than a memory limit, when
	// there is exactly one.
	resource string
}

// resourceWords name the usual resource settings.
var resourceWords = map[string]string{
	"limits.cpu": "the CPU limit", "requests.cpu": "the CPU request", "requests.memory": "the memory request",
}

// tally reads the entries the way timeline.TemplateChanges writes them:
// "image a -> b", "env +A ~B -C", "limits.memory a -> b", "command/args
// changed (not shown)", each after the container's name when the template
// has several, and "container x added (image y)" or "container x removed".
// An entry it does not know is counted as one other change, so the
// sentence never says less than the list.
func tally(changes []string) changeTally {
	var t changeTally
	for _, change := range changes {
		fields := strings.Fields(change)
		if len(fields) >= 3 && fields[0] == "container" && (fields[2] == "added" || fields[2] == "removed") {
			if fields[2] == "added" {
				t.added++
			} else {
				t.removed++
			}
			continue
		}
		known := false
		for at := 0; at < 2 && at < len(fields) && !known; at++ {
			known = true
			switch kind := fields[at]; {
			case kind == "image":
				// An image entry is a change from one to another; without
				// one the word was the name of a container.
				if known = strings.Contains(change, " -> "); known {
					t.images++
				}
			case kind == "env":
				keys, sources := 0, strings.Contains(change, "envFrom sources changed")
				for _, key := range fields[at+1:] {
					if key != "->" && strings.ContainsAny(key[:1], "+~-") {
						keys++
					}
				}
				// An entry that names no key is not an env entry: the
				// word was the name of a container.
				if known = keys > 0 || sources; known {
					t.env, t.envFrom = t.env+keys, t.envFrom || sources
				}
			case kind == "limits.memory":
				t.memoryLimits++
			case strings.HasPrefix(kind, "limits.") || strings.HasPrefix(kind, "requests."):
				t.resources++
				t.resource = kind
			case kind == "command/args":
				t.commands++
			default:
				known = false
			}
		}
		if !known {
			t.other++
		}
	}
	return t
}

// changed is the sentence of what the revision changed, with and without
// the values of the memory limit.
func (r *Report) changed() (full, brief string) {
	rev := r.Rollout.Label()
	if rev != "" {
		rev = strings.ToUpper(rev[:1]) + rev[1:]
	}
	switch {
	case len(r.Changes) > 0:
	case r.Rollout.Previous == nil:
		full = rev + " is the earliest retained revision; there is no earlier one to diff."
		return full, full
	default:
		full = rev + " changed nothing in images, env keys, resources or commands."
		return full, full
	}
	t := tally(r.Changes)
	sentence := func(limit string) string {
		var parts []string
		add := func(n int, one, many string) {
			switch {
			case n == 1:
				parts = append(parts, one)
			case n > 1:
				parts = append(parts, strconv.Itoa(n)+" "+many)
			}
		}
		add(t.images, "the image", "images")
		memory := "the memory limit"
		if limit != "" {
			memory += " (" + limit + ")"
		}
		add(t.memoryLimits, memory, "memory limits")
		one, many := resourceWords[t.resource], "resource settings"
		if one == "" {
			one = "1 resource setting"
		}
		if t.memoryLimits > 0 {
			many = "other resource settings"
		}
		add(t.resources, one, many)
		add(t.env, "1 env var", "env vars")
		if t.envFrom {
			parts = append(parts, "the envFrom sources")
		}
		add(t.commands, "the command", "commands")
		if t.added+t.removed > 0 {
			var how []string
			if t.added > 0 {
				how = append(how, strconv.Itoa(t.added)+" added")
			}
			if t.removed > 0 {
				how = append(how, strconv.Itoa(t.removed)+" removed")
			}
			parts = append(parts, "the containers ("+strings.Join(how, ", ")+")")
		}
		add(t.other, "1 other setting", "other settings")
		return rev + " changed " + inSeries(parts) + "."
	}
	limit := ""
	if t.memoryLimits == 1 {
		limit = r.MemoryLimit
	}
	return sentence(limit), sentence("")
}

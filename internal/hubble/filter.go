// SPDX-License-Identifier: Apache-2.0
package hubble

import (
	"fmt"
	flow "github.com/cilium/cilium/api/v1/flow"
	"google.golang.org/protobuf/proto"
	"net/netip"
	"strconv"
	"strings"
)

type Query struct {
	Text   string
	Filter *flow.FlowFilter
	Port   string
}

// Compile accepts a deliberately small AND grammar. Plain text stays local.
func Compile(s string) (Query, error) {
	q := Query{Filter: &flow.FlowFilter{}}
	if !strings.ContainsAny(s, "=<>!") {
		q.Text = strings.ToLower(strings.TrimSpace(s))
		return q, nil
	}
	seen := map[string]bool{}
	for _, term := range strings.Fields(s) {
		key, val, ok := strings.Cut(term, "=")
		if !ok || val == "" || strings.ContainsAny(val, "=<>!") || seen[key] {
			return Query{}, fmt.Errorf("invalid structured expression %q", term)
		}
		seen[key] = true
		switch key {
		case "verdict":
			v, ok := flow.Verdict_value[strings.ToUpper(val)]
			if !ok {
				return Query{}, fmt.Errorf("unknown verdict %q", val)
			}
			q.Filter.Verdict = []flow.Verdict{flow.Verdict(v)}
		case "protocol":
			switch strings.ToLower(val) {
			case "tcp", "udp", "icmpv4", "icmpv6", "http", "dns":
				q.Filter.Protocol = []string{strings.ToLower(val)}
			default:
				return Query{}, fmt.Errorf("unsupported protocol %q", val)
			}
		case "port":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 || n > 65535 {
				return Query{}, fmt.Errorf("port must be 1..65535")
			}
			q.Port = val
		case "ip":
			if _, err := netip.ParseAddr(val); err != nil {
				if _, err = netip.ParsePrefix(val); err != nil {
					return Query{}, fmt.Errorf("invalid IP/CIDR")
				}
			}
			q.Filter.SourceIp = []string{val}
		default:
			return Query{}, fmt.Errorf("unknown field %q; use verdict, protocol, port, ip", key)
		}
	}
	return q, nil
}

// Scope uses exact local matching to compensate for Hubble's pod-prefix filters.
type Scope struct {
	Pods  []string
	Title string
}

func (s Scope) Contains(p Peer) bool {
	for _, pod := range s.Pods {
		if pod == p.Pod {
			return true
		}
	}
	return false
}
func (s Scope) Includes(e Event) bool {
	return len(s.Pods) == 0 || s.Contains(e.Source) || s.Contains(e.Destination)
}
func (s Scope) Other(e Event) Peer {
	if s.Contains(e.Source) {
		return e.Destination
	}
	return e.Source
}
func (q Query) Match(e Event) bool { return q.Text == "" || strings.Contains(e.SearchText(), q.Text) }

// Filters expands symmetric predicates as OR branches, while preserving AND
// between scope and each user predicate. Never protobuf.Merge repeated scope fields.
func Filters(s Scope, q Query) []*flow.FlowFilter {
	base := []*flow.FlowFilter{{}}
	if len(s.Pods) > 0 {
		base = []*flow.FlowFilter{{SourcePod: s.Pods}, {DestinationPod: s.Pods}}
	}
	var out []*flow.FlowFilter
	for _, b := range base {
		b = proto.Clone(b).(*flow.FlowFilter)
		b.Verdict = q.Filter.GetVerdict()
		b.Protocol = q.Filter.GetProtocol()
		variants := []*flow.FlowFilter{b}
		if ip := q.Filter.GetSourceIp(); len(ip) > 0 {
			a := proto.Clone(b).(*flow.FlowFilter)
			a.SourceIp = ip
			b.DestinationIp = ip
			variants = []*flow.FlowFilter{a, b}
		}
		for _, v := range variants {
			if q.Port != "" {
				a := proto.Clone(v).(*flow.FlowFilter)
				a.SourcePort = []string{q.Port}
				v.DestinationPort = []string{q.Port}
				out = append(out, a, v)
			} else {
				out = append(out, v)
			}
		}
	}
	return out
}

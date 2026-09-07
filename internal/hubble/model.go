// SPDX-License-Identifier: Apache-2.0
package hubble

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	flow "github.com/cilium/cilium/api/v1/flow"
)

// Peer keeps IP identity separate from reported names: DNS names are evidence,
// not proof of service identity or policy authorization.
type Peer struct{ Kind, Cluster, Pod, IP, Names string }

func (p *Peer) Key() string {
	if p.Pod != "" {
		return p.Cluster + "/pod/" + p.Pod
	}
	return p.Cluster + "/" + p.Kind + "/" + p.IP
}
func (p Peer) String() string {
	if p.Pod != "" {
		if p.Cluster != "" {
			return p.Cluster + "/" + p.Pod
		}
		return p.Pod
	}
	s := p.Kind + " " + p.IP
	if p.Names != "" {
		s += " (" + p.Names + ")"
	}
	return s
}
func peer(e *flow.Endpoint, ip string, names []string) Peer {
	p := Peer{Kind: "external IP", Cluster: Clean(e.GetClusterName()), IP: Clean(ip), Names: Clean(strings.Join(names, ", "))}
	if e.GetPodName() != "" && e.GetNamespace() != "" {
		p.Kind = "pod"
		p.Pod = Clean(e.GetNamespace() + "/" + e.GetPodName())
		return p
	}
	for _, l := range e.GetLabels() {
		if l == "reserved:kube-apiserver" {
			p.Kind = "kube-apiserver"
			return p
		}
		if l == "reserved:world" || l == "reserved:world-ipv4" || l == "reserved:world-ipv6" {
			p.Kind = "world"
		}
	}
	if p.Kind == "external IP" && p.Names != "" {
		p.Kind = "FQDN"
	}
	if ip == "" && p.Names == "" {
		p.Kind = "unknown"
	}
	return p
}

// Event intentionally cannot retain raw L7 payloads, summaries or extensions.
type Event struct {
	ID                                                      uint64
	Time                                                    time.Time
	Source, Destination                                     Peer
	Node, Verdict, Protocol, DropReason, L7, Policy, Origin string
	SourcePort, DestinationPort                             uint32
}

func Normalize(f *flow.Flow, origin string) Event {
	e := Event{
		Time:        f.GetTime().AsTime(),
		Source:      peer(f.GetSource(), f.GetIP().GetSource(), f.GetSourceNames()),
		Destination: peer(f.GetDestination(), f.GetIP().GetDestination(), f.GetDestinationNames()),
		Node:        Clean(f.GetNodeName()), Verdict: f.GetVerdict().String(), Origin: origin,
		L7: "Not reported; L7 visibility unknown", Policy: "Not reported",
	}
	if f.GetVerdict() == flow.Verdict_DROPPED {
		e.DropReason = f.GetDropReasonDesc().String()
	}
	l := f.GetL4()
	switch {
	case l.GetTCP() != nil:
		e.Protocol = "TCP"
		e.SourcePort = l.GetTCP().GetSourcePort()
		e.DestinationPort = l.GetTCP().GetDestinationPort()
	case l.GetUDP() != nil:
		e.Protocol = "UDP"
		e.SourcePort = l.GetUDP().GetSourcePort()
		e.DestinationPort = l.GetUDP().GetDestinationPort()
	case l.GetICMPv4() != nil:
		e.Protocol = "ICMPv4"
	case l.GetICMPv6() != nil:
		e.Protocol = "ICMPv6"
	default:
		e.Protocol = "unknown"
	}
	if l7 := f.GetL7(); l7 != nil {
		e.L7 = "Reported (payload redacted)"
		if h := l7.GetHttp(); h != nil {
			e.L7 = fmt.Sprintf("HTTP status=%d (payload redacted)", h.GetCode())
		}
		if l7.GetDns() != nil {
			e.L7 = "DNS reported (payload redacted); not a policy verdict"
		}
	}
	var pp []string
	for _, group := range []struct {
		name     string
		policies []*flow.Policy
	}{
		{"egress allowed", f.GetEgressAllowedBy()}, {"ingress allowed", f.GetIngressAllowedBy()},
		{"egress denied", f.GetEgressDeniedBy()}, {"ingress denied", f.GetIngressDeniedBy()},
	} {
		for _, p := range group.policies {
			pp = append(pp, group.name+": "+Clean(p.GetKind()+" "+p.GetNamespace()+"/"+p.GetName()))
		}
	}
	if len(pp) > 0 {
		e.Policy = Clean(strings.Join(pp, "; "))
	}
	return e
}

// Clean prevents terminal control injection and bounds untrusted display strings.
func Clean(s string) string {
	r := []rune(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, s))
	if len(r) > 1024 {
		r = r[:1024]
	}
	return string(r)
}
func (e *Event) SearchText() string {
	return strings.ToLower(fmt.Sprintf("%s %s %s %s %s %d %d %s", e.Source, e.Destination, e.Verdict, e.Protocol, e.Node, e.SourcePort, e.DestinationPort, e.DropReason))
}

type Store struct {
	mu                sync.Mutex
	events            []Event
	next              int
	sequence, evicted uint64
	full              bool
}

func NewStore(capacity int) *Store {
	if capacity < 1 {
		capacity = 1
	}
	return &Store{events: make([]Event, capacity)}
}

//nolint:gocritic // Store takes ownership of an immutable event value, independent of its producer.
func (s *Store) Add(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	e.ID = s.sequence
	if s.full {
		s.evicted++
	}
	s.events[s.next] = e
	s.next = (s.next + 1) % len(s.events)
	if s.next == 0 {
		s.full = true
	}
}
func (s *Store) Snapshot() (events []Event, evicted uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.full {
		return append([]Event{}, s.events[:s.next]...), s.evicted
	}
	r := append([]Event{}, s.events[s.next:]...)
	r = append(r, s.events[:s.next]...)
	return r, s.evicted
}

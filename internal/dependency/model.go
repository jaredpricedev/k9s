// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package dependency reviews explicit references and selected reports. An edge
// is source evidence, not causal dependence, reachability or application health.
package dependency

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/hubble"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/k9s/internal/networkpath"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	MaxEdges              = 256
	MaxGaps               = 64
	TypeConfiguration     = "configuration"
	TypeOwnership         = "ownership reference"
	TypeTraffic           = "reported traffic"
	GroupEdges            = "Explicit dependency evidence"
	StateTargetUIDMatched = "obtained target UID matches"
)

type Endpoint struct {
	Identity      inspect.ResourceIdentity
	ReportedKey   string
	DeclaredAlias string
}

type Edge struct {
	Type, Relation, State string
	From, To              Endpoint
	Source                networkpath.Source
	SourceAt, CapturedAt  time.Time
	Coverage              string
	SourceTimeKind        string
	Facts                 []networkpath.Fact
	Gaps                  []string
}

type FlowCoverage struct {
	StartedAt, CapturedAt, FirstReportedAt, LastReportedAt time.Time
	CoverageKnown                                          bool
	Lost, Evicted                                          uint64
	ProjectedOmitted, DropGroupsOmitted                    int
	Phase, Error, Detail                                   string
	Nodes                                                  []hubble.Node
	Connected, Unavailable                                 uint32
	NodesOmitted                                           int
}

type Review struct {
	Group                 networkpath.Scope
	StartedAt, CapturedAt time.Time
	Edges                 []Edge
	Coverage              []networkpath.Coverage
	Flows                 *FlowCoverage
	Gaps                  []string
	Omitted               int
}

type composer struct {
	review  *Review
	sources map[string]networkpath.Source
	seen    map[string]bool
}

func Compose(snapshot *networkpath.Snapshot) *Review {
	r := &Review{}
	if snapshot == nil {
		r.gap("Native source snapshot unavailable; no dependency inferred")
		return r
	}
	r.Group = snapshot.Scope
	r.Group.RouteNamespaces = []string{snapshot.Scope.Service.Namespace}
	r.StartedAt, r.CapturedAt = snapshot.StartedAt, snapshot.CapturedAt
	r.Coverage = append([]networkpath.Coverage(nil), snapshot.Coverage...)
	r.Omitted = snapshot.Omitted
	if snapshot.Scope.Service.UID == "" || snapshot.Scope.Service.Context == "" || snapshot.Scope.Service.GVR != networkpath.ServiceGVR ||
		snapshot.Service.Source.Identity != snapshot.Scope.Service {
		r.gap("Captured Service group UID is unavailable or changed; replacement sources are not substituted")
		return r
	}
	c := composer{review: r, sources: make(map[string]networkpath.Source), seen: make(map[string]bool)}
	c.remember(&snapshot.Service.Source)
	for i := range snapshot.Pods {
		c.remember(&snapshot.Pods[i].Source)
	}
	for i := range snapshot.Items {
		c.remember(&snapshot.Items[i].Source)
	}
	keys := make([]string, 0, len(c.sources))
	for key := range c.sources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	c.traffic(snapshot.Flows)
	c.configuration(snapshot)
	for _, key := range keys {
		source := c.sources[key]
		c.ownership(&source)
	}
	for _, coverage := range r.Coverage {
		if coverage.State != inspect.ObservationComplete {
			r.gap("Native query coverage incomplete; denied/absent/stale/truncated reads are not missing dependencies")
		}
	}
	if r.Omitted > 0 {
		r.gap("Native/edge projection limits omitted records; review is incomplete")
	}
	return r
}

func sourceKey(id *inspect.ResourceIdentity) string {
	return id.GVR + "/" + id.Namespace + "/" + id.Name + "/" + id.UID
}
func (c *composer) inGroup(id *inspect.ResourceIdentity) bool {
	return id.Context == c.review.Group.Service.Context && id.Namespace == c.review.Group.Service.Namespace && id.UID != "" && id.GVR != ""
}
func (c *composer) remember(source *networkpath.Source) {
	if !c.inGroup(&source.Identity) {
		c.review.gap("Sources outside the explicit namespace/identity group are excluded; no automatic federation")
		return
	}
	copySource := *source
	copySource.Owners = append([]networkpath.OwnerReference(nil), source.Owners...)
	c.sources[sourceKey(&source.Identity)] = copySource
}
func (c *composer) ownership(source *networkpath.Source) {
	if source.OwnerOmitted > 0 {
		c.review.Omitted += source.OwnerOmitted
	}
	for _, ref := range source.Owners {
		target := inspect.ResourceIdentity{Context: source.Identity.Context, GVR: ref.GVR, Namespace: source.Identity.Namespace, Name: ref.Name, UID: ref.UID}
		if ref.GVR == "" || !c.inGroup(&target) {
			c.review.gap("Unsupported/cluster-scoped/Secret owner kind excluded; current owner identity is unverified")
			continue
		}
		relation := "Owner reference"
		if ref.ControllerReported && ref.Controller {
			relation = "Controlling owner reference"
		}
		edge := c.newEdge(source, TypeOwnership, relation, &Endpoint{Identity: target}, &Endpoint{Identity: source.Identity})
		edge.State = c.targetState(&target)
		if edge.State != StateTargetUIDMatched {
			edge.Gaps = append(edge.Gaps, "Owner is a captured metadata reference; current target state is unverified")
		}
		c.add(&edge)
	}
}

func (c *composer) targetState(id *inspect.ResourceIdentity) string {
	if _, found := c.sources[sourceKey(id)]; found {
		return StateTargetUIDMatched
	}
	return "captured target UID reference; current object unverified"
}
func (c *composer) newEdge(source *networkpath.Source, kind, relation string, from, to *Endpoint) Edge {
	edge := Edge{Type: kind, Relation: relation, State: "configured reference", From: *from, To: *to, Source: *source,
		CapturedAt: source.CapturedAt, SourceTimeKind: "Local API-object capture; object modification time unreported", Coverage: "source query coverage unreported"}
	for _, coverage := range c.review.Coverage {
		if coverage.GVR == source.Identity.GVR && coverage.Namespace == source.Identity.Namespace && (coverage.Name == "" || coverage.Name == source.Identity.Name) {
			edge.Coverage = coverage.State + ": " + coverage.Detail
			if coverage.State != inspect.ObservationComplete {
				edge.Gaps = append(edge.Gaps, "Source query incomplete: "+coverage.State)
			}
		}
	}
	edge.Source.Owners = append([]networkpath.OwnerReference(nil), source.Owners...)
	if edge.Coverage == "source query coverage unreported" {
		edge.Gaps = append(edge.Gaps, "Source query coverage unreported")
		c.review.gap("Some source queries have unreported coverage; no complete graph claim")
	}
	return edge
}
func (c *composer) add(edge *Edge) {
	data, _ := json.Marshal([]any{edge.Type, edge.Relation, edge.Source.Identity, edge.From, edge.To})
	key := string(data)
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	if len(c.review.Edges) >= MaxEdges {
		c.review.Omitted++
		return
	}
	if len(edge.Gaps) > 0 {
		c.review.gap("Some edge source/target identities, conditions or query windows are unverified; inspect typed edge evidence")
	}
	c.review.Edges = append(c.review.Edges, *edge)
}
func (r *Review) gap(detail string) {
	detail = logstream.SafeText(detail)
	if len(r.Gaps) < MaxGaps && !slices.Contains(r.Gaps, detail) {
		r.Gaps = append(r.Gaps, detail)
	}
}

func (c *composer) configuration(snapshot *networkpath.Snapshot) {
	service := &snapshot.Service.Source
	if snapshot.Service.Type == "ExternalName" {
		if snapshot.Service.ExternalName == "" {
			c.review.gap("ExternalName DNS target unreported; no alias binding inferred")
		} else {
			edge := c.newEdge(service, TypeConfiguration, "Declared ExternalName DNS alias", &Endpoint{Identity: service.Identity},
				&Endpoint{DeclaredAlias: snapshot.Service.ExternalName})
			edge.State = "declared alias; runtime resolution unobserved"
			edge.Gaps = append(edge.Gaps, "Declared DNS alias is not a resolved endpoint, current API UID or observed request path")
			c.add(&edge)
		}
	}
	if len(snapshot.Service.Selector) > 0 {
		selector := labels.SelectorFromSet(snapshot.Service.Selector)
		for i := range snapshot.Pods {
			pod := &snapshot.Pods[i]
			if c.inGroup(&pod.Source.Identity) && selector.Matches(labels.Set(pod.Labels)) {
				edge := c.newEdge(service, TypeConfiguration, "Service selector matches obtained Pod",
					&Endpoint{Identity: service.Identity}, &Endpoint{Identity: pod.Source.Identity})
				edge.State = "selection; ownership and traffic unproven"
				c.add(&edge)
			}
		}
	}
	for i := range snapshot.Items {
		item := &snapshot.Items[i]
		if !c.inGroup(&item.Source.Identity) {
			continue
		}
		if item.Source.Kind == "EndpointSlice" {
			edge := c.newEdge(&item.Source, TypeConfiguration, "Service-name label association", &Endpoint{Identity: service.Identity}, &Endpoint{Identity: item.Source.Identity})
			edge.Gaps = append(edge.Gaps, "Label association alone is not controlling ownership or dataplane evidence")
			c.add(&edge)
		}
		for _, id := range item.Related {
			if !c.inGroup(&id) {
				c.review.gap("Cross-namespace reference remains in Network evidence; excluded from this native namespace group")
				continue
			}
			var relation string
			switch item.Source.Kind {
			case "EndpointSlice":
				relation = "Endpoint target reference"
			case "Ingress", "HTTPRoute":
				if item.State == inspect.ObservationUnknown {
					c.review.gap("Unsupported backend identity remains unverified; no Service edge inferred")
					continue
				}
				relation = "Configured backend/parent reference"
			case "NetworkPolicy":
				relation = "Selecting policy candidate; authorization unproven"
			default:
				continue
			}
			edge := c.newEdge(&item.Source, TypeConfiguration, relation, &Endpoint{Identity: item.Source.Identity}, &Endpoint{Identity: id})
			edge.State = c.targetState(&id)
			edge.Facts = append([]networkpath.Fact(nil), item.Facts...)
			edge.Gaps = append(edge.Gaps, item.Gaps...)
			if edge.State != StateTargetUIDMatched {
				edge.Gaps = append(edge.Gaps, "Captured reference does not prove the current target object")
			}
			c.add(&edge)
		}
	}
}

func (c *composer) traffic(window *networkpath.FlowWindow) {
	if window == nil {
		c.review.gap("Flow observation not requested; missing traffic is not a missing dependency")
		return
	}
	c.review.Flows = &FlowCoverage{StartedAt: window.StartedAt, CapturedAt: window.CapturedAt, FirstReportedAt: window.FirstReportedAt,
		LastReportedAt: window.LastReportedAt, CoverageKnown: window.Status.CoverageKnown, Lost: window.Status.Lost, Evicted: window.Evicted,
		ProjectedOmitted: window.ProjectedOmitted, DropGroupsOmitted: window.DropGroupsOmitted, Phase: window.Status.Phase, Error: window.Status.Error,
		Detail: window.Status.CoverageError + " " + window.Status.LossDetail}
	c.review.Flows.Nodes = append([]hubble.Node(nil), window.Status.Nodes[:min(32, len(window.Status.Nodes))]...)
	c.review.Flows.Connected, c.review.Flows.Unavailable = window.Status.Connected, window.Status.Unavailable
	c.review.Flows.NodesOmitted = max(0, len(window.Status.Nodes)-32)
	c.review.gap("Flow window is filtered and may have discontinuities; only retained individually selected reports are represented")
	if c.review.Flows.NodesOmitted > 0 {
		c.review.gap("Additional reported nodes omitted; node coverage incomplete")
	}
	if !window.Status.CoverageKnown || window.Status.Lost > 0 || window.Evicted > 0 || window.ProjectedOmitted > 0 || window.DropGroupsOmitted > 0 {
		c.review.gap("Selected flow window is partial/unknown; loss, eviction, filters and omitted reports prevent complete traffic conclusions")
	}
	selected := 0
	for i := range window.Items {
		item := &window.Items[i]
		if !item.Pinned || item.Source.Kind != "Hubble Relay flow" {
			continue
		}
		selected++
		if item.Source.Identity.Context != c.review.Group.Service.Context || !sameReportedCluster(item.EndpointKeys) {
			c.review.gap("Reported cross-cluster/unparseable identity excluded; no configured identity mapping or federation scope applied")
			continue
		}
		edge := c.newEdge(&item.Source, TypeTraffic, "Explicitly pinned reported conversation",
			&Endpoint{ReportedKey: item.EndpointKeys[0]}, &Endpoint{ReportedKey: item.EndpointKeys[1]})
		edge.State = "reported peers; Service/Pod binding unverified"
		edge.SourceAt = time.Time{}
		edge.SourceTimeKind = "Reported flow API timestamp; unavailable when zero"
		edge.Facts = append([]networkpath.Fact(nil), item.Facts...)
		for _, fact := range item.Facts {
			if fact.Name == "Reported flow time" {
				edge.SourceAt, _ = time.Parse(time.RFC3339Nano, fact.Value)
			}
		}
		edge.Coverage = "captured Hubble window; inspect loss/node/filter/eviction evidence"
		edge.Gaps = []string{"Reported names/IPs never identify a current Service/Pod UID or causal dependency"}
		if edge.SourceAt.IsZero() {
			edge.Gaps = append(edge.Gaps, "API flow timestamp unavailable")
		}
		// Record identity is authored by the local store, not a Kubernetes API UID.
		edge.Relation += " · " + networkpath.ItemKey(item)
		c.add(&edge)
	}
	if selected == 0 {
		c.review.gap("No individually pinned flow reports; p/t/P in Flows explicitly selects reported evidence")
	}
}
func sameReportedCluster(keys [2]string) bool {
	var peers [2]struct{ Cluster string }
	for i, key := range keys {
		if key == "" || json.Unmarshal([]byte(key), &peers[i]) != nil {
			return false
		}
	}
	return peers[0].Cluster == peers[1].Cluster
}

func (r *Review) Items() []networkpath.Item {
	items := make([]networkpath.Item, 0, len(r.Edges))
	for i := range r.Edges {
		edge := &r.Edges[i]

		related := []inspect.ResourceIdentity{}
		for _, endpoint := range []Endpoint{edge.From, edge.To} {
			if endpoint.Identity.UID != "" {
				related = append(related, endpoint.Identity)
			}
		}
		summary := endpointText(&edge.From) + " → " + endpointText(&edge.To)
		items = append(items, networkpath.Item{Source: edge.Source, Group: GroupEdges, State: edgeTypeLabel(edge.Type), Summary: summary, Related: related,
			Gaps: append([]string(nil), edge.Gaps...),
			Facts: []networkpath.Fact{{Name: "Relation", Value: edge.Relation},
				{Name: "Captured locally", Value: edge.CapturedAt.UTC().Format(time.RFC3339Nano) + " · " + strings.SplitN(edge.Coverage, ":", 2)[0]},
				{Name: "Edge identity", Value: edgeKey(edge)}, {Name: "Boundary", Value: "Source reference/report; no causal dependency or health proof"}}})
	}
	return items
}
func edgeKey(edge *Edge) string {
	data, _ := json.Marshal([]any{edge.Type, edge.Relation, edge.Source.Identity, edge.From, edge.To})
	return string(data)
}
func endpointText(endpoint *Endpoint) string {
	if endpoint.Identity.UID != "" {
		return endpoint.Identity.Namespace + "/" + endpoint.Identity.Name
	}
	if endpoint.DeclaredAlias != "" {
		return "DNS alias " + endpoint.DeclaredAlias
	}
	return "reported peer (API UID unknown)"
}
func (r *Review) Summary() string {
	kinds := make(map[string]int)
	for i := range r.Edges {
		kinds[r.Edges[i].Type]++
	}
	return fmt.Sprintf("%d config · %d owners · %d flow\n%d gaps · %d omitted · no health proof",
		kinds[TypeConfiguration], kinds[TypeOwnership], kinds[TypeTraffic], len(r.Gaps), r.Omitted)
}
func (r *Review) Evidence() string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "Typed edge evidence unavailable"
	}
	boundary := "Explicit captured group; references/reports do not establish causal dependencies, health or continuous traffic history.\n"
	return boundary + strings.Join(r.Gaps, "\n") + "\n\n" + string(data)
}

func (r *Review) EvidenceFor(index int) string {
	if index < 0 || index >= len(r.Edges) {
		return r.Evidence()
	}
	value := struct {
		Group                 networkpath.Scope
		StartedAt, CapturedAt time.Time
		Edge                  *Edge
		Coverage              []networkpath.Coverage
		Flows                 *FlowCoverage
		Gaps                  []string
		Omitted               int
	}{r.Group, r.StartedAt, r.CapturedAt, &r.Edges[index], r.Coverage, r.Flows, r.Gaps, r.Omitted}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "Typed edge evidence unavailable"
	}
	return "Reference/report only; no causal dependency, health or continuous-history proof.\n" + string(data)
}

func edgeTypeLabel(value string) string {
	switch value {
	case TypeConfiguration:
		return "config"
	case TypeOwnership:
		return "owner ref"
	case TypeTraffic:
		return "flow report"
	}
	return value
}

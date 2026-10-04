// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package networkpath composes explicitly scoped configuration and optional
// observed flows. A configured link never establishes reachability or policy authorization.
package networkpath

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/hubble"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/logstream"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	MaxRouteNamespaces = 8
	MaxItems           = 512
	MaxRelated         = 128
	PageSize           = 64
	MaxPages           = 2
	CollectionTimeout  = 15 * time.Second
	QueryTimeout       = 2 * time.Second
	GroupDNS           = "DNS configuration"
	GroupBackends      = "Backend evidence"
	GroupRoutes        = "Configured entrypoints"
	GroupPolicies      = "Policy candidates"
	GroupFlows         = "Observed flows"
	StateConfigured    = "configured"
	StateCandidate     = "candidate"
	StateTruncated     = "truncated"
	StateAbsent        = "absent"
	StateCanceled      = "canceled"
	ServiceGVR         = "v1/services"
	PodGVR             = "v1/pods"
	NativeAPI          = "v1"
	KindService        = "Service"
	KindHTTPRoute      = "HTTPRoute"
	PinSource          = "source"
	PinDestination     = "destination"
	PinConversation    = "conversation"
	GatewayGroup       = "gateway.networking.k8s.io"
)

type Scope struct {
	Service         inspect.ResourceIdentity
	RouteNamespaces []string
}

// NormalizeScope retains one captured Service UID. Additional namespaces widen
// route reads only; Pod, EndpointSlice and policy reads stay in the Service namespace.
//
//nolint:gocritic // Capture an independent scope before bounded asynchronous work.
func NormalizeScope(scope Scope) (Scope, error) {
	if scope.Service.Context == "" || scope.Service.GVR != ServiceGVR || scope.Service.UID == "" ||
		len(validation.IsDNS1123Label(scope.Service.Namespace)) != 0 || len(validation.IsDNS1123Label(scope.Service.Name)) != 0 {
		return Scope{}, fmt.Errorf("select a native Service with captured context, namespace, name and UID")
	}
	namespaces := []string{scope.Service.Namespace}
	for _, ns := range scope.RouteNamespaces {
		ns = strings.TrimSpace(ns)
		if len(validation.IsDNS1123Label(ns)) != 0 {
			return Scope{}, fmt.Errorf("route namespace %q must be an explicit namespace", ns)
		}
		if !slices.Contains(namespaces, ns) {
			namespaces = append(namespaces, ns)
		}
	}
	if len(namespaces) > MaxRouteNamespaces {
		return Scope{}, fmt.Errorf("at most %d explicit route namespaces are supported", MaxRouteNamespaces)
	}
	scope.RouteNamespaces = namespaces
	return scope, nil
}

type Source struct {
	Identity              inspect.ResourceIdentity
	Kind, ResourceVersion string
	CapturedAt            time.Time
	Owners                []OwnerReference
	OwnerOmitted          int
}

type OwnerReference struct {
	GVR, Kind, APIVersion, Name, UID string
	Controller, ControllerReported   bool
}

type Fact struct{ Name, Value string }

// Item holds a safe typed projection, never a raw spec, env, TLS key, header or Secret.
type Item struct {
	Source                Source
	Group, State, Summary string
	Facts                 []Fact
	Related               []inspect.ResourceIdentity
	Gaps                  []string
	EndpointKeys          [2]string
	ConversationKey       string
	Pinned                bool
}

type Port struct {
	Name, Protocol, Target string
	Number                 int64
}

type Service struct {
	Source                  Source
	Type, ExternalName      string
	ClusterIPs, ExternalIPs []string
	Ports                   []Port
	Selector                map[string]string `json:"-"`
}

type Pod struct {
	Source       Source
	IPs          []string
	Ready, Phase string
	Labels       map[string]string `json:"-"`
}

type Coverage struct {
	GVR, Namespace, Name, State, Detail string
	CapturedAt                          time.Time
}

type Snapshot struct {
	Scope                 Scope
	StartedAt, CapturedAt time.Time
	Service               Service
	Pods                  []Pod
	Items                 []Item
	Coverage              []Coverage
	Omitted               int
	Flows                 *FlowWindow
}

type FlowWindow struct {
	StartedAt, CapturedAt, FirstReportedAt, LastReportedAt time.Time
	Status                                                 hubble.Status
	Evicted                                                uint64
	Count, ProjectedOmitted, DropGroupsOmitted             int
	Items                                                  []Item
	Pins                                                   []FlowPin
}

type FlowPin struct {
	Kind, Key, Context string
	CreatedAt          time.Time
}

func safe(value string) string {
	runes := []rune(logstream.SafeText(value))
	if len(runes) > 2048 {
		runes = append(runes[:2047], '…')
	}
	return string(runes)
}

func (s *Snapshot) add(item *Item) {
	if len(s.Items) >= MaxItems {
		s.Omitted++
		return
	}
	s.Items = append(s.Items, *item)
}

func (item *Item) fact(name, value string) {
	if len(item.Facts) >= 64 {
		item.gap("Additional fact fields omitted; retained evidence is incomplete")
		return
	}
	if len([]rune(logstream.SafeText(value))) > 2048 {
		item.gap("Fact text truncated at 2048 characters; retained evidence is incomplete")
	}
	item.Facts = append(item.Facts, Fact{safe(name), safe(value)})
}

func (item *Item) gap(detail string) {
	if len(item.Gaps) < 16 && !slices.Contains(item.Gaps, safe(detail)) {
		item.Gaps = append(item.Gaps, safe(detail))
	}
}

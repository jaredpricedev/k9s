// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package networkpath

import (
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func source(object *unstructured.Unstructured, gvr, contextName string, at time.Time) Source {
	return Source{Identity: inspect.ResourceIdentity{Context: contextName, GVR: gvr,
		Namespace: object.GetNamespace(), Name: object.GetName(), UID: string(object.GetUID())},
		Kind: object.GetKind(), ResourceVersion: object.GetResourceVersion(), CapturedAt: at}
}

func scalar(object map[string]any, path ...string) string {
	v, found, _ := unstructured.NestedFieldNoCopy(object, path...)
	if !found {
		return "unknown / not reported"
	}
	switch v.(type) {
	case string, int64, bool:
		return safe(fmt.Sprint(v))
	}
	return "unknown / malformed value"
}

func text(object map[string]any, path ...string) string {
	v, _, _ := unstructured.NestedString(object, path...)
	return safe(v)
}

func stringsAt(object map[string]any, path ...string) []string {
	v, _, _ := unstructured.NestedStringSlice(object, path...)
	result := make([]string, 0, min(32, len(v)))
	for _, value := range v[:min(32, len(v))] {
		result = append(result, safe(value))
	}
	return result
}

func (s *Snapshot) projectService(object *unstructured.Unstructured, at time.Time) {
	s.Service = Service{Source: source(object, ServiceGVR, s.Scope.Service.Context, at),
		Type: text(object.Object, "spec", "type"), ExternalName: text(object.Object, "spec", "externalName"),
		ClusterIPs: stringsAt(object.Object, "spec", "clusterIPs"), ExternalIPs: stringsAt(object.Object, "spec", "externalIPs")}
	if len(s.Service.ClusterIPs) == 0 {
		if ip := text(object.Object, "spec", "clusterIP"); ip != "" {
			s.Service.ClusterIPs = []string{ip}
		}
	}
	s.Service.Selector, _, _ = unstructured.NestedStringMap(object.Object, "spec", "selector")
	if len(s.Service.Selector) > 32 {
		s.Service.Selector = nil
		s.Coverage = append(s.Coverage, Coverage{GVR: ServiceGVR, Namespace: object.GetNamespace(), Name: object.GetName(),
			State: StateTruncated, Detail: "Selector exceeds 32 fields; selector dropped as a whole and no broad Pod list will be attempted", CapturedAt: at})
	}
	ports, _, _ := unstructured.NestedSlice(object.Object, "spec", "ports")
	for _, raw := range ports[:min(32, len(ports))] {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		number, _, _ := unstructured.NestedInt64(p, "port")
		s.Service.Ports = append(s.Service.Ports, Port{Name: text(p, "name"), Protocol: scalar(p, "protocol"), Number: number, Target: scalar(p, "targetPort")})
	}
	if len(ports) > 32 {
		s.Omitted += len(ports) - 32
	}
	item := Item{Source: s.Service.Source, Group: GroupDNS, State: StateConfigured, Summary: "Service name is configured; DNS resolution has not been tested"}
	item.fact("Relative Service DNS name", object.GetName()+"."+object.GetNamespace()+".svc")
	item.fact("Cluster DNS suffix", "Unknown until supported resolver/Pod search-domain evidence is obtained; no cluster.local assumption")
	item.fact("Service type", s.Service.Type)
	item.fact("Cluster IPs", strings.Join(s.Service.ClusterIPs, ", "))
	if s.Service.ExternalName != "" {
		item.fact("ExternalName CNAME target", s.Service.ExternalName)
	}
	item.fact("Connectivity", "Configuration only; no DNS lookup, endpoint probe, TLS handshake or policy evaluation performed")
	s.add(&item)
}

func (s *Snapshot) projectPod(object *unstructured.Unstructured, at time.Time) {
	p := Pod{Source: source(object, PodGVR, s.Scope.Service.Context, at), Ready: "unknown / condition not reported",
		Phase: scalar(object.Object, "status", "phase"), Labels: object.GetLabels()}
	if ip := text(object.Object, "status", "podIP"); ip != "" {
		p.IPs = []string{ip}
	}
	ips, _, _ := unstructured.NestedSlice(object.Object, "status", "podIPs")
	for _, raw := range ips[:min(8, len(ips))] {
		if value, ok := raw.(map[string]any); ok {
			if ip := text(value, "ip"); ip != "" {
				p.IPs = append(p.IPs, ip)
			}
		}
	}
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	for _, raw := range conditions[:min(32, len(conditions))] {
		if c, ok := raw.(map[string]any); ok && text(c, "type") == "Ready" {
			p.Ready = scalar(c, "status")
		}
	}
	if len(s.Pods) >= MaxRelated {
		s.Omitted++
		return
	}
	s.Pods = append(s.Pods, p)
	item := Item{Source: p.Source, Group: GroupDNS, State: StateConfigured, Summary: "Pod resolver configuration; runtime resolver file and DNS availability are unobserved"}
	item.fact("dnsPolicy", scalar(object.Object, "spec", "dnsPolicy"))
	item.fact("hostNetwork", scalar(object.Object, "spec", "hostNetwork"))
	item.fact("Authored nameservers", strings.Join(stringsAt(object.Object, "spec", "dnsConfig", "nameservers"), ", "))
	item.fact("Authored search domains", strings.Join(stringsAt(object.Object, "spec", "dnsConfig", "searches"), ", "))
	options, _, _ := unstructured.NestedSlice(object.Object, "spec", "dnsConfig", "options")
	for _, raw := range options[:min(16, len(options))] {
		if option, ok := raw.(map[string]any); ok {
			item.fact("Resolver option "+text(option, "name"), scalar(option, "value"))
		}
	}
	item.fact("Pod Ready condition", p.Ready)
	item.fact("Boundary", "API configuration does not provide kubelet-generated /etc/resolv.conf, DNS response or dataplane connectivity")
	s.add(&item)
}

func (s *Snapshot) projectEndpointSlice(object *unstructured.Unstructured, at time.Time) {
	association := "Service-name label association; Service controlling UID not reported"
	for _, owner := range object.GetOwnerReferences() {
		if owner.Kind == KindService && owner.APIVersion == NativeAPI && owner.Controller != nil && *owner.Controller {
			if string(owner.UID) != s.Scope.Service.UID {
				s.Coverage = append(s.Coverage, Coverage{GVR: "discovery.k8s.io/v1/endpointslices", Namespace: object.GetNamespace(), Name: object.GetName(),
					State: inspect.ObservationUnknown, Detail: "EndpointSlice controlling Service UID differs; stale association excluded", CapturedAt: at})
				return
			}
			association = "Controlling Service UID matches captured source"
		}
	}
	base := Item{Source: source(object, "discovery.k8s.io/v1/endpointslices", s.Scope.Service.Context, at), Group: GroupBackends, State: StateConfigured}
	base.fact("Association", association)
	base.fact("Address type", scalar(object.Object, "addressType"))
	ports, _, _ := unstructured.NestedSlice(object.Object, "ports")
	for _, raw := range ports[:min(32, len(ports))] {
		if p, ok := raw.(map[string]any); ok {
			base.fact("Endpoint port "+text(p, "name"), scalar(p, "port")+"/"+scalar(p, "protocol"))
		}
	}
	endpoints, _, _ := unstructured.NestedSlice(object.Object, "endpoints")
	if len(endpoints) == 0 {
		base.State = inspect.ObservationUnknown
		base.Summary = "Current EndpointSlice reports no endpoints; see query coverage"
		s.add(&base)
	}
	for _, raw := range endpoints[:min(MaxRelated, len(endpoints))] {
		e, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		item := base
		item.Facts = append([]Fact(nil), base.Facts...)
		if ready, found, _ := unstructured.NestedBool(e, "conditions", "ready"); found {
			item.State = fmt.Sprintf("ready %t", ready)
		} else {
			item.State = "ready ?"
			item.gap("Endpoint ready condition not reported")
		}
		item.Summary = strings.Join(stringsAt(e, "addresses"), ", ") + " · ready " + scalar(e, "conditions", "ready")
		item.fact("Addresses", strings.Join(stringsAt(e, "addresses"), ", "))
		for _, field := range []string{"ready", "serving", "terminating"} {
			item.fact("Endpoint "+field, scalar(e, "conditions", field))
		}
		item.fact("Node", scalar(e, "nodeName"))
		ref, _, _ := unstructured.NestedMap(e, "targetRef")
		if text(ref, "kind") == "Pod" && text(ref, "apiVersion") == NativeAPI && text(ref, "namespace") == s.Scope.Service.Namespace && text(ref, "uid") != "" {
			item.Related = []inspect.ResourceIdentity{{Context: s.Scope.Service.Context, GVR: PodGVR, Namespace: text(ref, "namespace"),
				Name: text(ref, "name"), UID: text(ref, "uid")}}
			item.fact("Target Pod reference", text(ref, "name")+" UID="+text(ref, "uid")+"; current Pod identity/readiness requires matching API evidence")
		} else {
			item.fact("Target identity", "Unknown or unsupported; an IP/name does not establish a current Pod UID")
		}
		item.fact("Boundary", "Reported endpoint conditions and ports do not prove a successful connection")
		s.add(&item)
	}
	if len(endpoints) > MaxRelated {
		s.Omitted += len(endpoints) - MaxRelated
	}
}

# Native dependency evidence review

Select a native Service and run `:dependency-review`, or use `d` / tab 8 Edges inside Network review. This creates an explicit captured Service/namespace group and uses the same bounded native snapshot. It adds no API discovery, background observation, provider request or mutation. The native configuration collector remains scoped and deadline-bound; Edges only composes retained typed values.

Edges label configuration, owner references and explicitly pinned reported traffic separately. Declared ExternalName DNS aliases, Service selector matches, EndpointSlice label/target references, native backend/parent references and selecting policy candidates remain source evidence. Native metadata ownership does not establish causal dependence or health. Current owner/target identity is verified only when that exact context/GVR/namespace/name/UID is present in the obtained snapshot. Unsupported and cluster-scoped owner kinds, Secret ownership targets and cross-namespace references are excluded with gaps. The Network tabs retain their wider explicit route evidence.

Use `p` / `t` / `P` in Flows to explicitly select reported endpoints/conversations for Edges. A flow name, IP, DNS alias or forwarded verdict never becomes a current Pod/Service UID or a proven dependency. Reported cross-cluster peers are excluded; this slice has no configured identity mapping or fleet scope. It does not start a federation scan. Missing traffic, quiet sections, filters, Relay denial, loss, eviction and omitted projections remain gaps.

Each retained edge includes source identity, relationship type, captured endpoint/reference UIDs, local capture time, API flow time when reported, query/observation coverage and uncertainty. Local API-object capture is distinct from an unavailable object modification time. Enter inspects the captured supporting source; `b` opens an available captured target reference through native UID guards; `v` shows the selected typed edge with captured group/window/coverage. Flow peers without API UIDs offer retained evidence only. Query, tab, selected edge and scroll survive dialog return, failed refresh and resize. Background API connectivity changes retain the explicit task.

The composition caps 256 edges and 64 distinct gap explanations; native snapshot limits and owner-reference omissions remain visible. Supported native owner references are projected from at most 16 metadata references per source. Unsupported/Secret owner names and UIDs are excluded. No raw specs, env, credential, TLS key, header or Secret content is introduced.

## Proposed operator tasks and public support

These are tasks chosen for the user's authorized native toolkit implementation, not an operator demand or usability study:

| Proposed task | Source evidence and design implication |
| --- | --- |
| Inspect one Service's declared backend/ownership references without a noisy cluster-wide map | [K9s #3899](https://github.com/derailed/k9s/issues/3899), a closed request for selected application namespaces, describes repeated switching and all-namespace noise. Use an explicit bounded group. It does not measure dependency-map demand. |
| Explain why a visible endpoint declaration and traffic behavior disagree | [Cilium #49077](https://github.com/cilium/cilium/issues/49077), a September 2026 community report, describes a surviving ready EndpointSlice with a reported missing dataplane backend. Preserve configuration and observed reports separately; this report is not a reproduced or proven bug in k9+. |
| Preserve external aliases and uncertain peer identities when opening supporting evidence | [Hubble UI #1152](https://github.com/cilium/hubble-ui/issues/1152) reports missing ExternalName/FQDN destinations, while [#1051](https://github.com/cilium/hubble-ui/issues/1051) describes indistinguishable external `world` nodes. Keep declared aliases and reported peer fields without inventing a Service/CIDRGroup identity mapping. This slice does not implement a CiliumCIDRGroup adapter. |
| Review retained selected traffic without treating absent flows as missing dependencies | The source reports above support inspecting underlying fields alongside references. Explicit pinning, source times and window gaps keep the hypothesis reviewable. No continuous-history or completeness claim follows. |

Primary issue bodies were retrieved through the configured GitHub connector on 2026-10-04. These reports support concrete review tasks and identity/coverage requirements; they do not establish prevalence, current upstream status beyond the retrieved records, comparative app quality, measured scan times or demand for a broad map. No Reddit/Stack Overflow research is claimed here.

The broader application map, configured cross-cluster identity/fleet/history providers and usability/demand gates remain open. Expanding beyond the native evidence slice requires concrete operator tasks and measured usefulness; no implementation date is promised for that broader scope.

// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package storage

import (
	"fmt"
	"sort"
	"strings"
)

func (s *Snapshot) join() {
	if s.Scope.Identity.Name != "" && !s.selectionSeen {
		s.Gaps = append(s.Gaps, "Captured selection is not in the visible source page; current selected-resource evidence is incomplete")
	}
	claims := make(map[string]bool)
	if s.Scope.PodName != "" {
		for i := range s.Pods {
			for _, name := range s.Pods[i].Claims {
				claims[s.Pods[i].Identity.Namespace+"/"+name] = true
			}
		}
		s.PVCs = dropRows(s.PVCs, func(p *PVC) bool { return !claims[p.Identity.Namespace+"/"+p.Identity.Name] })
	} else if s.Scope.Kind == "PersistentVolume" {
		s.PVCs = dropRows(s.PVCs, func(p *PVC) bool { return p.Volume != s.Scope.PVName })
	} else if s.Scope.ClassName != "" {
		s.PVCs = dropRows(s.PVCs, func(p *PVC) bool { return p.Class == nil || *p.Class != s.Scope.ClassName })
	}
	for i := range s.PVCs {
		claims[s.PVCs[i].Identity.Namespace+"/"+s.PVCs[i].Identity.Name] = true
	}
	if s.Scope.PodName == "" {
		s.Pods = dropRows(s.Pods, func(p *Pod) bool {
			for _, name := range p.Claims {
				if claims[p.Identity.Namespace+"/"+name] {
					return false
				}
			}
			return true
		})
	}
	volumes, classes, drivers, nodes, uids := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	if s.Scope.PVName != "" {
		volumes[s.Scope.PVName] = true
	}
	if s.Scope.ClassName != "" {
		classes[s.Scope.ClassName] = true
	}
	for i := range s.PVCs {
		p := &s.PVCs[i]
		uids[p.Identity.UID] = true
		if p.Volume != "" {
			volumes[p.Volume] = true
		}
		if p.Class != nil && *p.Class != "" {
			classes[*p.Class] = true
		}
	}
	s.PVs = dropRows(s.PVs, func(p *PV) bool { return !volumes[p.Identity.Name] })
	for i := range s.PVs {
		p := &s.PVs[i]
		uids[p.Identity.UID] = true
		if p.Class != "" {
			classes[p.Class] = true
		}
		if p.Driver != "" {
			drivers[p.Driver] = true
		}
	}
	s.Classes = dropRows(s.Classes, func(c *Class) bool { return !classes[c.Identity.Name] })
	for i := range s.Classes {
		if name := s.Classes[i].Provisioner; name != "" {
			drivers[name] = true
		}
		uids[s.Classes[i].Identity.UID] = true
	}
	for i := range s.Pods {
		p := &s.Pods[i]
		uids[p.Identity.UID] = true
		if p.Node != "" {
			nodes[p.Node] = true
		}
	}
	s.Drivers = dropRows(s.Drivers, func(d *Driver) bool { return !drivers[d.Identity.Name] })
	s.CSINodes = dropRows(s.CSINodes, func(n *CSINode) bool { return !nodes[n.Identity.Name] })
	s.Attachments = dropRows(s.Attachments, func(a *Attachment) bool { return !volumes[a.Volume] })
	s.Events = dropRows(s.Events, func(e *Event) bool { return e.About.UID == "" || !uids[string(e.About.UID)] })
	s.checkReferences(claims, volumes, classes)
	sort.SliceStable(s.PVCs, func(i, j int) bool { return s.PVCs[i].Identity.Name < s.PVCs[j].Identity.Name })
	sort.SliceStable(s.Events, func(i, j int) bool { return s.Events[i].At.After(s.Events[j].At) })
}
func (s *Snapshot) checkReferences(claims, volumes, classes map[string]bool) {
	for name := range claims {
		namespace, claim, _ := strings.Cut(name, "/")
		if s.Claim(namespace, claim) == nil {
			s.Gaps = append(s.Gaps, "Referenced PVC "+name+" is not in the visible claim page; existence/binding is unknown")
		}
	}
	for name := range volumes {
		if s.Volume(name) == nil {
			s.Gaps = append(s.Gaps, "Referenced PV "+name+" is not in the visible volume page; binding/topology is unknown")
		}
	}
	for name := range classes {
		if s.Class(name) == nil {
			s.Gaps = append(s.Gaps, "Referenced StorageClass "+name+" is not in the visible class page; provisioner/expansion configuration is unknown")
		}
	}
	for i := range s.PVCs {
		pvc := &s.PVCs[i]
		if pv := s.Volume(pvc.Volume); pv != nil && !bindingVerified(pvc, pv) {
			s.Gaps = append(s.Gaps, fmt.Sprintf("PVC %s -> PV %s claim namespace/name/UID is not verified", pvc.Identity.Name, pv.Identity.Name))
		}
	}
	sort.Strings(s.Gaps)
}
func (s *Snapshot) Claim(namespace, name string) *PVC {
	for i := range s.PVCs {
		if s.PVCs[i].Identity.Name == name && s.PVCs[i].Identity.Namespace == namespace {
			return &s.PVCs[i]
		}
	}
	return nil
}
func (s *Snapshot) Volume(name string) *PV {
	for i := range s.PVs {
		if s.PVs[i].Identity.Name == name {
			return &s.PVs[i]
		}
	}
	return nil
}
func (s *Snapshot) Class(name string) *Class {
	for i := range s.Classes {
		if s.Classes[i].Identity.Name == name {
			return &s.Classes[i]
		}
	}
	return nil
}
func (s *Snapshot) Driver(name string) *Driver {
	for i := range s.Drivers {
		if s.Drivers[i].Identity.Name == name {
			return &s.Drivers[i]
		}
	}
	return nil
}
func bindingVerified(pvc *PVC, pv *PV) bool {
	return pvc.Identity.UID != "" && pv.Identity.UID != "" && pv.Claim != nil && string(pv.Claim.UID) == pvc.Identity.UID &&
		pv.Claim.Name == pvc.Identity.Name && pv.Claim.Namespace == pvc.Identity.Namespace && pvc.Volume == pv.Identity.Name
}

func dropRows[T any](rows []T, drop func(*T) bool) []T {
	kept := 0
	for i := range rows {
		if !drop(&rows[i]) {
			rows[kept] = rows[i]
			kept++
		}
	}
	clear(rows[kept:])
	return rows[:kept]
}

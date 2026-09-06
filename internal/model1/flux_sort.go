// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model1

import "sort"

// sortFluxStatus keeps problems above healthy resources in the unified Flux view.
func (r *RowEvents) sortFluxStatus(index int, asc bool) {
	if r == nil {
		return
	}
	sort.SliceStable(r.events, func(i, j int) bool {
		one, two := r.events[i].Row, r.events[j].Row
		if !asc {
			one, two = two, one
		}
		first, second := one.Fields[index], two.Fields[index]
		if a, b := fluxStatusPriority(first), fluxStatusPriority(second); a != b {
			return a < b
		}
		return Less(false, false, false, one.ID, two.ID, first, second)
	})
	r.reindex()
}

func fluxStatusPriority(status string) int {
	switch status {
	case "Failed":
		return 0
	case "Restricted":
		return 1
	case "Reconciling":
		return 3
	case "Pending":
		return 4
	case "Suspended":
		return 5
	case "Ready":
		return 6
	default:
		return 2 // Unknown and future unrecognized states need attention.
	}
}

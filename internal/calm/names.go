// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import "strings"

// ShortName is the one short name of a Pod on every page: the last
// dash-separated word of its name, which is what tells it from the other
// Pods of its workload ("hx4mt" for "gitlab-webservice-6f9c7d5b84-hx4mt").
// The ordinal of a StatefulSet Pod keeps the word before it ("gitaly-0").
// A name stays whole when it has no such word, when the word is shorter
// than three characters and so says too little ("web-c"), or when a
// sibling ends in the same one: a short name never stands for two things.
// The same Pod is then "hx4mt" in :slow, on the Pod comparison, in :probe
// and in the sentence at the top of each.
func ShortName(name string, siblings []string) string {
	cut := strings.LastIndex(name, "-")
	if cut <= 0 || cut == len(name)-1 {
		return name
	}
	if strings.Trim(name[cut+1:], "0123456789") == "" {
		// A bare ordinal names nothing: the word before it is kept.
		cut = strings.LastIndex(name[:cut], "-")
		if cut <= 0 {
			return name
		}
	}
	tail := name[cut:]
	if len(tail) < 4 {
		return name
	}
	for _, other := range siblings {
		if other != name && strings.HasSuffix(other, tail) {
			return name
		}
	}
	return tail[1:]
}

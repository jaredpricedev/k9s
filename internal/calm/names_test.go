// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShortNameIsTheWordThatTellsAPodFromItsSiblings(t *testing.T) {
	pods := []string{"gitlab-webservice-default-6f9c7d5b84-hx4mt", "gitlab-webservice-default-6f9c7d5b84-2bq7k"}
	for name, c := range map[string]struct {
		name     string
		siblings []string
		want     string
	}{
		"the last word of a generated name":        {pods[0], pods, "hx4mt"},
		"without siblings":                         {pods[0], nil, "hx4mt"},
		"an ordinal keeps the word before it":      {"gitlab-gitaly-0", []string{"gitlab-gitaly-0", "gitlab-gitaly-1"}, "gitaly-0"},
		"an ordinal with one word stays whole":     {"postgresql-0", nil, "postgresql-0"},
		"no dash":                                  {"standalone", nil, "standalone"},
		"a word too short to say anything":         {"web-c", []string{"web-a", "web-c"}, "web-c"},
		"three characters are a word":              {"web-p03", nil, "p03"},
		"a trailing dash":                          {"odd-", nil, "odd-"},
		"a leading dash":                           {"-odd", nil, "-odd"},
		"a sibling with the same word stays whole": {"web-a-x1", []string{"web-a-x1", "web-b-x1"}, "web-a-x1"},
		"a sibling with the same ordinal":          {"web-a-0", []string{"web-a-0", "db-a-0"}, "web-a-0"},
		"the name itself is not its own sibling":   {"api-7d9-x2x9q", []string{"api-7d9-x2x9q"}, "x2x9q"},
		"a sibling that only ends alike":           {"api-x2x9q", []string{"api-x2x9q", "api-zx2x9q"}, "x2x9q"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, ShortName(c.name, c.siblings))
		})
	}
}

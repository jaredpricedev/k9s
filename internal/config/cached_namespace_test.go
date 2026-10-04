// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package config

import (
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/data"
)

func TestCachedNamespaceUsesOnlyLoadedContext(t *testing.T) {
	// No kube settings/connection exists: a lazy activation would panic.
	c := &Config{K9s: &K9s{activeContextName: "unloaded-context"}}
	if got := c.CachedNamespace(); got != client.DefaultNamespace {
		t.Fatalf("unloaded namespace = %q", got)
	}
	active := data.NewContext()
	active.Namespace.Active = "retained-namespace"
	c.K9s.setActiveConfig(&data.Config{Context: active})
	if got := c.CachedNamespace(); got != "retained-namespace" {
		t.Fatalf("retained namespace = %q", got)
	}
}

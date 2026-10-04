// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package config

import (
	"errors"

	"github.com/derailed/k9s/internal/client"
)

// ReplaceSessionConnection commits a prepared replacement for the same named
// destination. It preserves the loaded UI/workspace configuration and never
// activates a kubeconfig current-context. Even a same-name swap advances the
// revision so callbacks captured before credential renewal cannot publish.
func (c *Config) ReplaceSessionConnection(conn client.Connection, name, namespace string, revision uint64) error {
	if conn == nil || conn.Config() == nil || conn.Config().Flags() == nil ||
		conn.Config().Flags().Context == nil || *conn.Config().Flags().Context != name {
		return errors.New("replacement session does not match captured context")
	}
	if c.ActiveContextName() != name || c.ActiveNamespace() != namespace || c.DestinationRevision() != revision {
		return errors.New("destination changed before session refresh completed")
	}
	c.SetConnection(conn)
	c.settings = conn.Config()
	c.K9s.mx.Lock()
	c.K9s.ks = conn.Config()
	c.K9s.mx.Unlock()
	c.destinationRevision.Add(1)
	return nil
}

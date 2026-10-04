// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"

	"github.com/derailed/k9s/internal/client"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Pin existing client handles before starting asynchronous reads. APIClient itself
// is mutable across context switches; these handles keep the original transport.
type pinnedInspectionConnection struct {
	client.Connection
	dynamicClient dynamic.Interface
	typedClient   kubernetes.Interface
}

func (c pinnedInspectionConnection) DynDial() (dynamic.Interface, error) { return c.dynamicClient, nil }
func (c pinnedInspectionConnection) Dial() (kubernetes.Interface, error) { return c.typedClient, nil }
func pinInspectionConnection(conn client.Connection) (client.Connection, error) {
	if conn == nil {
		return nil, fmt.Errorf("select a configured Kubernetes context first; :connection can diagnose an unavailable session")
	}
	dyn, err := conn.DynDial()
	if err != nil {
		return nil, err
	}
	if dyn == nil {
		return nil, fmt.Errorf("Kubernetes resource client is unavailable; use :connection to check this destination")
	}
	typed, err := conn.Dial()
	if err != nil {
		return nil, err
	}
	if typed == nil {
		return nil, fmt.Errorf("Kubernetes client is unavailable; use :connection to check this destination")
	}
	return pinnedInspectionConnection{Connection: conn, dynamicClient: dyn, typedClient: typed}, nil
}

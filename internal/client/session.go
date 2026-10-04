// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package client

import (
	"errors"
	"log/slog"

	"github.com/derailed/k9s/internal/slogs"
	"k8s.io/apimachinery/pkg/util/cache"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// NewSessionConnection constructs fresh browsing handles from an already
// checked, private named-context config. It performs no discovery or API reads;
// optional metrics discovery cannot prevent a healthy browsing session.
func NewSessionConnection(config *Config) (*APIClient, error) {
	if config == nil {
		return nil, errors.New("no configured session")
	}
	cfg, err := config.RESTConfig()
	if err != nil {
		return nil, err
	}
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, err
	}
	typed, err := kubernetes.NewForConfigAndClient(cfg, httpClient)
	if err != nil {
		httpClient.CloseIdleConnections()
		return nil, err
	}
	dyn, err := dynamic.NewForConfigAndClient(cfg, httpClient)
	if err != nil {
		httpClient.CloseIdleConnections()
		return nil, err
	}
	return &APIClient{config: config, client: typed, dClient: dyn,
		cache: cache.NewLRUExpireCache(cacheSize), connOK: true,
		log: slog.Default().With(slogs.Subsys, "client"), sessionHTTP: httpClient}, nil
}

// CloseSession releases idle transports from an unused or replaced session.
// Active request cancellation belongs to its captured collectors and watches.
func (a *APIClient) CloseSession() {
	if a != nil && a.sessionHTTP != nil {
		a.sessionHTTP.CloseIdleConnections()
	}
}

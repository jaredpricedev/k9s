// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package hubble

import (
	"context"

	observer "github.com/cilium/cilium/api/v1/observer"
	"google.golang.org/grpc"
)

// ProbeReadiness performs one unary read with the configured verified transport.
// The caller owns the deadline; no flow stream, retry, or port-forward is started.
//
//nolint:gocritic // Copy immutable connection configuration for this bounded request.
func ProbeReadiness(ctx context.Context, config Config) (string, error) {
	credentials, err := config.Credentials()
	if err != nil {
		return "", err
	}
	connection, err := grpc.NewClient(config.Address, grpc.WithTransportCredentials(credentials))
	if err != nil {
		return "", err
	}
	defer connection.Close()
	response, err := observer.NewObserverClient(connection).ServerStatus(ctx, &observer.ServerStatusRequest{})
	if err != nil {
		return "", err
	}
	return Clean(response.GetVersion()), nil
}

// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package client

import (
	"testing"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
)

func TestSnapshotConfigFlagsRetainsOriginalDestinationAndAuthentication(t *testing.T) {
	flags := genericclioptions.NewConfigFlags(false)
	*flags.Context, *flags.Namespace = "original-cluster", "original-namespace"
	*flags.CertFile, *flags.KeyFile = "/original/client.crt", "/original/client.key"
	*flags.ImpersonateUID = "original-user"
	*flags.ImpersonateGroup = []string{"original-group"}
	*flags.ImpersonateUserExtra = []string{"purpose=original"}
	flags.WrapConfigFn = func(cfg *rest.Config) *rest.Config {
		cfg.UserAgent = "original-transport"
		return cfg
	}
	snapshot := SnapshotConfigFlags(flags)
	*flags.Context, *flags.Namespace = "replacement-cluster", "replacement-namespace"
	*flags.CertFile, *flags.KeyFile = "/replacement/client.crt", "/replacement/client.key"
	*flags.ImpersonateUID = "replacement-user"
	(*flags.ImpersonateGroup)[0] = "replacement-group"
	(*flags.ImpersonateUserExtra)[0] = "purpose=replacement"
	if *snapshot.Context != "original-cluster" || *snapshot.Namespace != "original-namespace" ||
		*snapshot.CertFile != "/original/client.crt" || *snapshot.KeyFile != "/original/client.key" ||
		*snapshot.ImpersonateUID != "original-user" || (*snapshot.ImpersonateGroup)[0] != "original-group" ||
		(*snapshot.ImpersonateUserExtra)[0] != "purpose=original" {
		t.Fatal("captured destination or authentication flags followed a later mutation")
	}
	if snapshot.WrapConfigFn(&rest.Config{}).UserAgent != "original-transport" {
		t.Fatal("captured transport customization was lost")
	}
}

func TestConfigSnapshotPinsContextWithoutLoadingKubeconfig(t *testing.T) {
	flags := genericclioptions.NewConfigFlags(false)
	*flags.KubeConfig = "/missing/kubeconfig"
	*flags.Context = "original"
	*flags.ImpersonateGroup = []string{"operators"}
	snapshot := NewConfig(flags).Snapshot("captured")
	*flags.Context = "replacement"
	(*flags.ImpersonateGroup)[0] = "replacement"
	if *snapshot.Flags().Context != "captured" || (*snapshot.Flags().ImpersonateGroup)[0] != "operators" {
		t.Fatal("snapshot followed mutable session flags")
	}
}

func TestConfigSnapshotKeepsCommittedActorAndDiagnosticsReload(t *testing.T) {
	const capturedContext = "committed-context"
	const originalHost = "https://original.invalid"
	flags := genericclioptions.NewConfigFlags(false)
	*flags.KubeConfig = "/missing/committed-kubeconfig"
	*flags.Context = capturedContext
	cfg := NewConfig(flags)
	cfg.PrepareSessionREST(&rest.Config{Host: originalHost, BearerToken: "original-fixture-token"})
	snapshot := cfg.Snapshot(capturedContext)
	cfg.PrepareSessionREST(&rest.Config{Host: "https://replacement.invalid", BearerToken: "replacement-fixture-token"})
	actor, err := snapshot.RESTConfig()
	if err != nil {
		t.Fatalf("same-context snapshot reloaded missing kubeconfig: %v", err)
	}
	if actor.Host != originalHost || actor.BearerToken != "original-fixture-token" {
		t.Fatal("background actor followed a later session replacement")
	}
	actor.Host = "https://caller-mutation.invalid"
	actor, err = snapshot.RESTConfig()
	if err != nil || actor.Host != originalHost {
		t.Fatal("returned REST config changed the captured actor")
	}
	if _, err = cfg.Snapshot("different-context").RESTConfig(); err == nil {
		t.Fatal("another context inherited the committed session actor")
	}
	diagnostics, err := cfg.PinnedDiagnosticConfig(capturedContext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = diagnostics.RESTConfig(); err == nil {
		t.Fatal("explicit diagnostics did not reload its independent config")
	}
}

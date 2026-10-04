// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestGuardedSourceIdentityRejectsChangesAndBoundsTraversal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte("name: original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := fingerprintOperationSource(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(path, []byte("name: replaced\n"), 0600); writeErr != nil {
		t.Fatal(writeErr)
	}
	after, err := fingerprintOperationSource(t.Context(), dir)
	if err != nil || before == after || strings.Contains(after, "replaced") {
		t.Fatal(before, after, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fingerprintOperationSource(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := fingerprintOperationSource(t.Context(), t.TempDir()); err == nil {
		t.Fatal("empty source accepted")
	}
	if err := os.WriteFile(path, make([]byte, 16*1024*1024+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := fingerprintOperationSource(t.Context(), dir); err == nil {
		t.Fatal("unbounded source accepted")
	}
}

func TestGuardedCLISnapshotPinsEveryDestinationOverrideAndProtectsCredentials(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config")
	raw := clientcmdapi.NewConfig()
	raw.CurrentContext = guardedTestOtherContext
	raw.Clusters[guardedTestSelectedContext] = &clientcmdapi.Cluster{Server: "https://old.example", InsecureSkipTLSVerify: true}
	raw.Clusters[guardedTestOtherContext] = &clientcmdapi.Cluster{Server: "https://unrelated.example"}
	raw.AuthInfos[guardedTestSelectedContext] = &clientcmdapi.AuthInfo{Token: "original-token"}
	raw.AuthInfos[guardedTestOtherContext] = &clientcmdapi.AuthInfo{Token: "unrelated-token"}
	raw.Contexts[guardedTestSelectedContext] = &clientcmdapi.Context{Cluster: guardedTestSelectedContext, AuthInfo: guardedTestSelectedContext, Namespace: "source-ns"}
	raw.Contexts[guardedTestOtherContext] = &clientcmdapi.Context{Cluster: guardedTestOtherContext, AuthInfo: guardedTestOtherContext}
	if err := clientcmd.WriteToFile(*raw, configPath); err != nil {
		t.Fatal(err)
	}
	flags := genericclioptions.NewConfigFlags(false)
	flags.KubeConfig = &configPath
	//nolint:gosec // Dummy credential verifies configuration isolation.
	contextName, server, token, tlsName, as := guardedTestSelectedContext, "https://override.example", "override-token", "server-name", "engineer"
	flags.Context, flags.APIServer, flags.BearerToken, flags.TLSServerName, flags.Impersonate = &contextName, &server, &token, &tlsName, &as
	flags.ImpersonateGroup = &[]string{"team-a", "team-b"}
	snapshotFlags := client.SnapshotConfigFlags(flags)
	//nolint:gosec // Dummy credential verifies later flags cannot retarget the operation.
	contextName, server, token = guardedTestOtherContext, "https://later.example", "later-token"
	snapshotPath, cleanup, err := cliDestinationSnapshot(snapshotFlags, guardedTestSelectedContext, "reviewed-ns")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, err := os.Stat(snapshotPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credentials were not private", info, err)
	}
	snapshot, err := clientcmd.LoadFromFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CurrentContext != guardedTestSelectedContext || len(snapshot.Contexts) != 1 || len(snapshot.Clusters) != 1 || len(snapshot.AuthInfos) != 1 {
		t.Fatal("destination was not isolated", snapshot)
	}
	cluster, identity := snapshot.Clusters[guardedTestSelectedContext], snapshot.AuthInfos[guardedTestSelectedContext]
	if cluster.Server != "https://override.example" || cluster.TLSServerName != tlsName || !cluster.InsecureSkipTLSVerify || identity.Token != "override-token" || identity.Impersonate != as || len(identity.ImpersonateGroups) != 2 || snapshot.Contexts[guardedTestSelectedContext].Namespace != "reviewed-ns" {
		t.Fatal("overrides not captured")
	}
	cleanup()
	if _, err := os.Stat(snapshotPath); !os.IsNotExist(err) {
		t.Fatal("temporary credentials survived cleanup")
	}
}

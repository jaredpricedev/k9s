// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const guardedTestReviewedServer = "https://reviewed.example"

type guardedDestinationConnection struct {
	client.Connection
	config      *client.Config
	destination *rest.Config
}

func (c guardedDestinationConnection) Config() *client.Config { return c.config }
func (c guardedDestinationConnection) RestConfig() (*rest.Config, error) {
	return rest.CopyConfig(c.destination), nil
}

func TestGuardedProviderAndPluginUseCommittedActorDespiteAnEditedKubeconfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	raw := clientcmdapi.NewConfig()
	raw.CurrentContext = guardedTestSelectedContext
	raw.Clusters[guardedTestSelectedContext] = &clientcmdapi.Cluster{Server: guardedTestReviewedServer}
	raw.AuthInfos[guardedTestSelectedContext] = &clientcmdapi.AuthInfo{}
	raw.Contexts[guardedTestSelectedContext] = &clientcmdapi.Context{Cluster: guardedTestSelectedContext, AuthInfo: guardedTestSelectedContext}
	if err := clientcmd.WriteToFile(*raw, path); err != nil {
		t.Fatal(err)
	}
	flags := genericclioptions.NewConfigFlags(false)
	flags.KubeConfig = &path
	committed, err := flags.ToRESTConfig()
	if err != nil {
		t.Fatal(err)
	}
	actor := guardedDestinationConnection{config: client.NewConfig(flags), destination: committed}
	raw.Clusters[guardedTestSelectedContext].Server = "https://replaced.example"
	if writeErr := clientcmd.WriteToFile(*raw, path); writeErr != nil {
		t.Fatal(writeErr)
	}
	invocation := &pluginInvocation{plugin: config.Plugin{Command: nativeKubectlCommand},
		actorREST: actor.RestConfig, contextName: guardedTestSelectedContext, namespace: "reviewed-ns"}
	destination, err := invocation.resolveDestination()
	if err != nil {
		t.Fatal(err)
	}
	factory := pinnedOperationFactory{connection: actor}
	if freezeErr := freezeHelmOperationDestination(factory); freezeErr != nil {
		t.Fatal(freezeErr)
	}
	helmDestination, err := flags.ToRESTConfig()
	if err != nil || helmDestination.Host != guardedTestReviewedServer {
		t.Fatal("Helm operation followed an edited kubeconfig", err)
	}
	snapshotPath, cleanup, err := writeCLIDestination(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	snapshot, err := clientcmd.LoadFromFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Clusters[guardedTestSelectedContext].Server != guardedTestReviewedServer {
		t.Fatal("operation followed an edited kubeconfig")
	}
	cleanup()
	if _, statErr := os.Stat(snapshotPath); !os.IsNotExist(statErr) {
		t.Fatal("temporary credentials were retained", statErr)
	}
}

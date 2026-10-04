// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/ui"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

func TestExplicitPluginsRemainDiscoverableWithFailedCoarseConnectionHealth(t *testing.T) {
	app, _, connection := sessionAppFixture(t)
	require.False(t, connection.ConnectionOK(), "fixture must expose the coarse false-health state")
	oldPath := config.AppPluginsFile
	config.AppPluginsFile = filepath.Join(t.TempDir(), "plugins.yaml")
	t.Cleanup(func() { config.AppPluginsFile = oldPath })
	content := "plugins:\n  owned:\n    shortCut: Shift-U\n    description: Owned local fixture\n" +
		"    scopes: [all]\n    command: /bin/true\n    background: true\n"
	require.NoError(t, os.WriteFile(config.AppPluginsFile, []byte(content), 0600))
	runner := &capturedPluginRunner{app: app, path: guardedTestPluginPath, env: Env{}}
	actions := ui.NewKeyActions()
	require.NoError(t, pluginActions(runner, actions))
	action, ok := actions.Get(ui.KeyShiftU)
	require.True(t, ok, "static plugin discovery was blocked by unrelated connection health")
	require.True(t, action.Opts.Plugin)
}

func TestLocalPluginOwnsDecoratedResourceTable(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	_, err := app.Config.ActivateContext("ct-1-1")
	require.NoError(t, err)
	browser := NewBrowser(client.PodGVR).(*Browser)
	require.NoError(t, browser.Table.Init(context.WithValue(t.Context(), internal.KeyApp, app)))
	browser.meta = &metav1.APIResource{Kind: "Pod"}
	pod := &Pod{ResourceViewer: browser}
	app.Content.Push(pod)
	invocation := &pluginInvocation{runner: browser, contextName: app.Config.ActiveContextName(),
		revision: app.Config.DestinationRevision(), path: browser.GetSelectedItem()}
	require.True(t, invocation.current(), "embedded runner lost ownership of its decorated Pod page")
	replacement := NewBrowser(client.PodGVR).(*Browser)
	require.NoError(t, replacement.Table.Init(context.WithValue(t.Context(), internal.KeyApp, app)))
	replacement.meta = &metav1.APIResource{Kind: "Pod"}
	app.Content.Push(replacement)
	require.False(t, invocation.current(), "another resource table inherited the captured plugin")
	app.Content.Pop()
	require.True(t, invocation.current(), "returning to the captured table did not restore ownership")
}

const (
	debugFixtureCommand = "debug"
	debugFixturePod     = "pod/api"
	debugFixtureCopy    = "--copy-to=investigation-copy"
	debugFixturePipe    = "kubectl --tls-server-name api.example debug pod/api --copy-to=investigation-copy"
)

func TestKubectlDebugGlobalFlagArity(t *testing.T) {
	for _, flag := range []string{
		"--context", "--kubeconfig", "--namespace", "-n", "--as", "--as-uid", "--as-group", "--as-user-extra",
		"--token", "--server", "-s", "--user", "--cluster", "--cache-dir", "--tls-server-name", "--request-timeout",
		"--certificate-authority", "--client-certificate", "--client-key", "--username", "--password",
		"--kuberc", "--profile", "--profile-output", "--v", "-v", "--vmodule", "--log-flush-frequency",
	} {
		t.Run(flag, func(t *testing.T) {
			for _, args := range [][]string{
				{flag, "fixture-value", debugFixtureCommand, debugFixturePod},
				{flag + "=fixture-value", debugFixtureCommand, debugFixturePod},
			} {
				require.True(t, kubectlDebugCommand(nativeKubectlCommand, args), "global flag hid a debug write: %v", args)
			}
			require.False(t, kubectlDebugCommand(nativeKubectlCommand, []string{flag, debugFixtureCommand, "logs", debugFixturePod}),
				"global flag value was mistaken for a native subcommand")
		})
	}
	for _, args := range [][]string{
		{"-napps", debugFixtureCommand, debugFixturePod},
		{"-v9", "--tls_server_name=api.example", debugFixtureCommand, debugFixturePod},
		{"--insecure-skip-tls-verify", "--disable-compression=false", "--warnings-as-errors", debugFixtureCommand, debugFixturePod},
		{"--match-server-version=false", "--cache-dir", "/tmp/debug cache", debugFixtureCommand, debugFixturePod},
		{"-it", debugFixtureCommand, debugFixturePod},
		{"-i", debugFixtureCommand, debugFixturePod},
		{"--stdin", debugFixtureCommand, debugFixturePod},
		{"-t", debugFixtureCommand, debugFixturePod},
		{"--tty", debugFixtureCommand, debugFixturePod},
		{"--keep_labels", "--keep-init-containers", "--arguments-only", debugFixtureCommand, debugFixturePod},
		{"--image=busybox", debugFixtureCommand, debugFixturePod},
		{"--image", "busybox", debugFixtureCommand, debugFixturePod},
		{"--stdin=true", debugFixtureCommand, debugFixturePod},
	} {
		require.True(t, kubectlDebugCommand("/usr/local/bin/kubectl.exe", args), "native debug command was missed: %v", args)
	}
	require.False(t, kubectlDebugCommand(nativeKubectlCommand, []string{"--context=fixture", "logs", debugFixturePod, "-c", debugFixtureCommand}))
	require.False(t, kubectlDebugCommand(nativeKubectlCommand, []string{"--tail=4", "logs", debugFixturePod, "-c", debugFixtureCommand}))
	require.False(t, kubectlDebugCommand(nativeKubectlCommand, []string{"--", debugFixtureCommand, debugFixturePod}))
	require.False(t, kubectlDebugCommand("helm", []string{"template", "--debug"}))
}

func TestPluginDebugStagesCannotBypassReadOnly(t *testing.T) {
	runner := &capturedPluginRunner{app: NewApp(mock.NewMockConfig(t)), path: guardedTestPluginPath, env: Env{}}
	runner.app.Config.K9s.ReadOnly = true
	for _, plugin := range []config.Plugin{
		{Command: nativeKubectlCommand, Args: []string{"--as-uid", "fixture-uid", debugFixtureCommand, debugFixturePod}},
		{Command: nativeKubectlCommand, Args: []string{"-i", debugFixtureCommand, debugFixturePod}},
		{Command: "echo", Pipes: []string{"kubectl --stdin debug pod/api"}},
		{Command: "echo", Args: []string{"input"}, Pipes: []string{"cat", debugFixturePipe}},
		{Command: "echo", Pipes: []string{`"/opt/native tools/kubectl.exe" --kuberc=/tmp/preferences debug pod/api`}},
	} {
		_, err := capturePluginInvocation(runner, &plugin)
		require.Error(t, err, "pipeline debug write was accepted in read-only mode")
		require.False(t, plugin.Dangerous, "configured plugin was mutated")
	}
	read := config.Plugin{Command: "echo", Pipes: []string{"kubectl --tls-server-name api.example logs pod/api -c debug"}}
	_, err := capturePluginInvocation(runner, &read)
	require.NoError(t, err, "read command's container name was mistaken for a debug write")
	commands := pluginDebugCommands(nativeKubectlCommand, []string{debugFixtureCommand, debugFixturePod}, []string{debugFixturePipe})
	require.Len(t, commands, 2, "every native debug stage must be inspected")
	require.Equal(t, []string{debugFixturePod, debugFixtureCopy}, commands[1], "global flags were retained as subcommand flags")
	commands = pluginDebugCommands(nativeKubectlCommand, []string{"--copy-to", "investigation-copy", debugFixtureCommand, debugFixturePod}, nil)
	require.Equal(t, [][]string{{"--copy-to", "investigation-copy", debugFixturePod}}, commands, "leading debug flags were dropped")
}

func TestPluginNativePipelinePinsDefaultDestination(t *testing.T) {
	for _, plugin := range []config.Plugin{
		{Command: nativeKubectlCommand},
		{Command: "echo", Pipes: []string{"cat", "kubectl --context=explicit get pods"}},
		{Command: "echo", Pipes: []string{`"/opt/native tools/helm.exe" list`}},
	} {
		source := &rest.Config{Host: "https://captured.example", BearerToken: "captured-token", Impersonate: rest.ImpersonationConfig{UserName: "captured-engineer"}}
		calls := 0
		invocation := &pluginInvocation{plugin: plugin, contextName: localSessionTestContext, namespace: localSessionTestNamespace,
			actorREST: func() (*rest.Config, error) { calls++; return source, nil }}
		destination, err := invocation.resolveDestination()
		require.NoError(t, err)
		require.NotNil(t, destination, "native pipeline default destination was left mutable")
		require.Equal(t, 1, calls)
		source.Host, source.BearerToken, source.Impersonate.UserName = "https://changed.example", "changed-token", "changed-engineer"
		require.Equal(t, "https://captured.example", destination.Clusters[localSessionTestContext].Server)
		require.Equal(t, "captured-token", destination.AuthInfos[localSessionTestContext].Token)
		require.Equal(t, "captured-engineer", destination.AuthInfos[localSessionTestContext].Impersonate)
		require.Equal(t, localSessionTestNamespace, destination.Contexts[localSessionTestContext].Namespace)
	}
	invocation := &pluginInvocation{plugin: config.Plugin{Command: "echo", Pipes: []string{"cat", "echo kubectl"}},
		actorREST: func() (*rest.Config, error) {
			t.Fatal("unrelated pipeline resolved Kubernetes credentials")
			return nil, nil
		}}
	destination, err := invocation.resolveDestination()
	require.NoError(t, err)
	require.Nil(t, destination)
}

func TestPluginDebugAuthorizesEveryPipelineStage(t *testing.T) {
	for _, denyCreate := range []bool{false, true} {
		t.Run(map[bool]string{false: "all allowed", true: "copy denied"}[denyCreate], func(t *testing.T) {
			typed := kubefake.NewSimpleClientset()
			var reviews []authv1.ResourceAttributes
			typed.PrependReactor("create", "selfsubjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				review := action.(ktesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
				attributes := review.Spec.ResourceAttributes
				reviews = append(reviews, *attributes)
				return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{
					Allowed: !denyCreate || attributes.Verb != client.CreateVerb}}, nil
			})
			invocation := &pluginInvocation{plugin: config.Plugin{Command: nativeKubectlCommand,
				Pipes: []string{debugFixturePipe, "kubectl --stdin --copy-to investigation-copy debug pod/api"}},
				contextName: localSessionTestContext, namespace: localSessionTestNamespace, session: &operationSession{typed: typed}}
			target := SelectedResourceTarget{Context: localSessionTestContext, GVR: client.PodGVR, Namespace: localSessionTestNamespace, Name: localSessionTestName}
			// An argument after -- belongs to the container command, not kubectl.
			args := []string{debugFixtureCommand, debugFixturePod, "--", "echo", debugFixtureCopy}
			err := invocation.authorizeDebugWrite(t.Context(), &target, args)
			if denyCreate {
				require.Error(t, err, "a later stage's denied creation was ignored")
			} else {
				require.NoError(t, err)
			}
			expected := 3
			if denyCreate {
				expected = 2
			}
			require.Len(t, reviews, expected)
			require.Equal(t, client.PatchVerb, reviews[0].Verb)
			require.Equal(t, "ephemeralcontainers", reviews[0].Subresource)
			require.Equal(t, localSessionTestName, reviews[0].Name)
			require.Equal(t, client.CreateVerb, reviews[1].Verb)
			require.Equal(t, "pods", reviews[1].Resource)
			require.Equal(t, localSessionTestNamespace, reviews[1].Namespace)
			require.Empty(t, reviews[1].Name)
			if !denyCreate {
				require.Equal(t, client.CreateVerb, reviews[2].Verb, "separated leading copy-to flag did not request creation")
				require.Empty(t, reviews[2].Subresource)
			}
		})
	}
}

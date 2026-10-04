// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/provider"
	"github.com/stretchr/testify/require"
)

func sourceProfileFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(sourceTestDirectory(t), "source.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	return path
}

//nolint:gocritic // The test receives the same captured input value as the provider contract.
func successCommand(in provider.Input, data string) provider.Result {
	return provider.Result{State: provider.Succeeded, Scope: in.Scope, Source: "/tools/" + in.Executable, Stdout: []byte(data)}
}

func TestExplicitSourceProfilesRenderBoundedNamedInputsAndRetainProvenance(t *testing.T) {
	for _, kind := range []string{"file", "kustomize", "helm", "git"} {
		t.Run(kind, func(t *testing.T) {
			dir := sourceTestDirectory(t)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(sourceTestManifest), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte("resources: [manifest.yaml]\n"), 0600))
			path := dir
			extra := ""
			if kind == "file" {
				path = filepath.Join(dir, "manifest.yaml")
			}
			if kind == "helm" {
				extra = "release: checkout\nnamespace: team-a\n"
			}
			if kind == "git" {
				extra = "revision: refs/tags/release-2\nmanifest: manifest.yaml\n"
			}
			profile := sourceProfileFixture(t, "name: release-source\nprovider: "+kind+"\npath: "+path+"\n"+extra)
			calls := []provider.Input{}
			run := func(_ context.Context, in provider.Input) provider.Result {
				calls = append(calls, in)
				if strings.Contains(strings.Join(in.Args, " "), "rev-parse") {
					return successCommand(in, strings.Repeat("a", 40)+"\n")
				}
				if strings.Contains(strings.Join(in.Args, " "), "version") {
					return successCommand(in, kind+" version fixture-v1\n")
				}
				return successCommand(in, sourceTestManifest+"---\napiVersion: v1\nkind: Secret\nmetadata: {name: credential}\ndata: {token: private-fixture-credential}\n")
			}
			scope := provider.Scope{Context: "pinned", Namespace: "team-a", Revision: 42}
			source, err := LoadSourceProfile(context.Background(), profile, scope, run)
			require.NoError(t, err)
			require.Equal(t, "release-source", source.Identity.Name)
			require.Equal(t, kind, source.Identity.Provider)
			require.Equal(t, profile, source.Identity.Path)
			require.NotEmpty(t, source.Identity.ProfileSHA256)
			require.NotEmpty(t, source.Identity.SHA256)
			if kind == "file" {
				require.Empty(t, calls)
				return
			}
			require.NotEmpty(t, source.Identity.RendererVersion)
			require.Equal(t, "/tools/"+kind, source.Identity.Renderer)
			require.Equal(t, calls[len(calls)-1].Args, source.Identity.Options)
			for _, call := range calls {
				require.Equal(t, scope, call.Scope)
				require.Equal(t, dir, call.Dir)
				require.LessOrEqual(t, call.Limits.StdoutBytes, int64(MaxSourceBytes))
				require.Greater(t, call.Limits.Timeout, time.Duration(0))
			}
			if kind == "git" {
				require.Equal(t, "refs/tags/release-2", source.Identity.RequestedRevision)
				require.Equal(t, strings.Repeat("a", 40), source.Identity.Revision)
				require.Contains(t, source.Identity.Options, strings.Repeat("a", 40)+":manifest.yaml")
			}
			require.True(t, source.Objects[1].SecretExcluded)
			encoded, err := json.Marshal(source)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), sourceTestToken)
		})
	}
}

func TestConfiguredSourcesRejectExternalReferencesAndNeverInvokeUnrequestedRenderer(t *testing.T) {
	dir := sourceTestDirectory(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte("resources: [github.com/example/repo//base]\n"), 0600))
	run := func(context.Context, provider.Input) provider.Result {
		t.Fatal("invalid source invoked executable")
		return provider.Result{}
	}
	profile := sourceProfileFixture(t, "name: release\nprovider: kustomize\npath: "+dir+"\n")
	_, err := LoadSourceProfile(context.Background(), profile, provider.Scope{}, run)
	require.ErrorContains(t, err, "local input")
	for _, data := range []string{
		"name: release\nprovider: exec\npath: /tmp\n",
		"name: release\nprovider: file\npath: /tmp/file\nargs: [apply]\n",
		"name: release\nprovider: git\npath: " + dir + "\nrevision: main\nmanifest: ../secret.yaml\n",
		"name: release\nprovider: helm\npath: " + dir + "\nrelease: checkout\n",
	} {
		_, err := LoadSourceProfile(context.Background(), sourceProfileFixture(t, data), provider.Scope{}, run)
		require.Error(t, err)
	}
}

func TestKustomizeRejectsHostLikeAndQueryReferencesBeforeAnyCommand(t *testing.T) {
	dir := sourceTestDirectory(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "github.com", "example", "repo"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(sourceTestManifest), 0600))
	for _, reference := range []string{"github.com/example/repo", "github.com/example/repo?ref=manifest.yaml", "git@example.test:repo", "https://example.test/base"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte("resources: ['"+reference+"']\n"), 0600))
		profile := sourceProfileFixture(t, "name: local\nprovider: kustomize\npath: "+dir+"\n")
		_, err := LoadSourceProfile(t.Context(), profile, provider.Scope{}, func(context.Context, provider.Input) provider.Result {
			t.Fatal("implicit network reference invoked an executable")
			return provider.Result{}
		})
		require.ErrorContains(t, err, "network")
	}
}

func TestRendererFailureLimitsCancellationAndChangedInputsCannotReplaceSource(t *testing.T) {
	dir := sourceTestDirectory(t)
	file := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(file, []byte(sourceTestManifest), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte("resources: [manifest.yaml]\n"), 0600))
	profile := sourceProfileFixture(t, "name: release\nprovider: kustomize\npath: "+dir+"\n")
	for _, state := range []provider.ExecutionState{provider.Failed, provider.OutputLimit, provider.Canceled, provider.TimedOut} {
		source, err := LoadSourceProfile(context.Background(), profile, provider.Scope{}, func(context.Context, provider.Input) provider.Result {
			return provider.Result{State: state, Err: os.ErrPermission, Stderr: []byte(sourceTestToken)}
		})
		require.Error(t, err)
		require.NotContains(t, err.Error(), sourceTestToken)
		require.Empty(t, source.Objects)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := LoadSourceProfile(ctx, profile, provider.Scope{}, func(context.Context, provider.Input) provider.Result {
		t.Fatal("canceled source invoked renderer")
		return provider.Result{}
	})
	require.Error(t, err)
	calls := 0
	_, err = LoadSourceProfile(context.Background(), profile, provider.Scope{}, func(_ context.Context, in provider.Input) provider.Result {
		calls++
		if calls == 1 {
			return successCommand(in, "kustomize-v1")
		}
		require.NoError(t, os.WriteFile(file, []byte(sourceTestManifest+"spec: {replicas: 2}\n"), 0600))
		return successCommand(in, sourceTestManifest)
	})
	require.ErrorContains(t, err, "inputs changed")
	_, err = sourceCommand(context.Background(), func(context.Context, provider.Input) provider.Result {
		return provider.Result{State: provider.Succeeded, Stdout: []byte(strings.Repeat("x", 10))}
	}, provider.Scope{}, "helm", dir, []string{"version"}, 9)
	require.ErrorContains(t, err, "limit")
}

func TestGitRevisionIsResolvedOnceAndNeverInterpolatedOrCheckedOut(t *testing.T) {
	dir := sourceTestDirectory(t)
	revision := "main$(touch /tmp/never)"
	profile := sourceProfileFixture(t, "name: exact\nprovider: git\npath: "+dir+"\nrevision: '"+revision+"'\nmanifest: application.yaml\n")
	calls := []provider.Input{}
	source, err := LoadSourceProfile(context.Background(), profile, provider.Scope{}, func(_ context.Context, in provider.Input) provider.Result {
		calls = append(calls, in)
		switch {
		case strings.Contains(strings.Join(in.Args, " "), "rev-parse"):
			require.Contains(t, in.Args, revision+"^{commit}")
			require.Contains(t, in.Args, "--end-of-options")
			return successCommand(in, strings.Repeat("b", 40))
		case strings.Contains(strings.Join(in.Args, " "), "version"):
			return successCommand(in, "git fixture")
		default:
			return successCommand(in, sourceTestManifest)
		}
	})
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("b", 40), source.Identity.Revision)
	require.Len(t, calls, 3)
	for _, call := range calls {
		for _, arg := range call.Args {
			require.NotEqual(t, "checkout", arg)
			require.NotEqual(t, "fetch", arg)
			require.NotEqual(t, "clone", arg)
		}
		require.Equal(t, "git", call.Executable)
	}
	missing := func(context.Context, provider.Input) provider.Result {
		return provider.Result{State: provider.Failed, Err: errors.New("missing executable")}
	}
	_, err = LoadSourceProfile(context.Background(), profile, provider.Scope{}, missing)
	require.Error(t, err)
}

func TestGitProviderReadsExactCommittedBlobWithoutChangingCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable unavailable")
	}
	dir := sourceTestDirectory(t)
	invoke := func(args ...string) []byte {
		t.Helper()
		result := provider.Run(t.Context(), provider.Input{ProviderID: "git-fixture", Executable: "git", Dir: dir, Args: args})
		require.Equal(t, provider.Succeeded, result.State, "%v", result.Err)
		return result.Stdout
	}
	invoke("init", "--quiet")
	file := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(file, []byte(sourceTestManifest+"spec: {replicas: 1}\n"), 0600))
	invoke("add", "manifest.yaml")
	invoke("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "--no-gpg-sign", "--no-verify", "-m", "fixture")
	commit := strings.TrimSpace(string(invoke("rev-parse", "HEAD")))
	require.NoError(t, os.WriteFile(file, []byte(sourceTestManifest+"spec: {replicas: 9}\n"), 0600))
	profile := sourceProfileFixture(t, "name: committed\nprovider: git\npath: "+dir+"\nrevision: HEAD\nmanifest: manifest.yaml\n")
	source, err := LoadSourceProfile(t.Context(), profile, provider.Scope{}, nil)
	require.NoError(t, err)
	require.Equal(t, commit, source.Identity.Revision)
	require.EqualValues(t, 1, source.Objects[0].Object["spec"].(map[string]any)["replicas"])
	unchanged, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Contains(t, string(unchanged), "replicas: 9")
	require.Equal(t, commit, strings.TrimSpace(string(invoke("rev-parse", "HEAD"))))
}

func TestRendererRejectsEmptyVersionScopeMismatchAndCanceledSuccess(t *testing.T) {
	dir := sourceTestDirectory(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(sourceTestManifest), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte("resources: [manifest.yaml]\n"), 0600))
	profile := sourceProfileFixture(t, "name: release\nprovider: kustomize\npath: "+dir+"\n")
	for _, version := range []string{"", " \n\t", "\x1b[0m"} {
		calls := 0
		_, err := LoadSourceProfile(t.Context(), profile, provider.Scope{}, func(_ context.Context, in provider.Input) provider.Result {
			calls++
			return successCommand(in, version)
		})
		require.ErrorContains(t, err, "version")
		require.Equal(t, 1, calls, "an unavailable version must stop before rendering")
	}
	_, err := LoadSourceProfile(t.Context(), profile, provider.Scope{Context: "pinned"}, func(_ context.Context, in provider.Input) provider.Result {
		result := successCommand(in, "version-v1")
		result.Scope.Context = "other"
		return result
	})
	require.ErrorContains(t, err, "scope")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err = LoadSourceProfile(ctx, profile, provider.Scope{}, func(_ context.Context, in provider.Input) provider.Result {
		cancel()
		return successCommand(in, "version-v1")
	})
	require.ErrorIs(t, err, context.Canceled)
	calls := 0
	_, err = LoadSourceProfile(t.Context(), profile, provider.Scope{}, func(_ context.Context, in provider.Input) provider.Result {
		calls++
		result := successCommand(in, "version-v1")
		if calls > 1 {
			result = successCommand(in, sourceTestManifest)
			result.Source = "/other/kustomize"
		}
		return result
	})
	require.ErrorContains(t, err, "executable identity changed")
}

func TestKustomizeExplicitRootIncludesSiblingBasesAndRejectsEscapingTargets(t *testing.T) {
	root := sourceTestDirectory(t)
	overlay := filepath.Join(root, "overlays", "production")
	base := filepath.Join(root, "base")
	require.NoError(t, os.MkdirAll(overlay, 0700))
	require.NoError(t, os.MkdirAll(base, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(overlay, "kustomization.yaml"), []byte("resources: [../../base]\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "kustomization.yaml"), []byte("resources: [manifest.yaml]\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "manifest.yaml"), []byte(sourceTestManifest), 0600))
	calls := 0
	run := func(_ context.Context, in provider.Input) provider.Result {
		calls++
		if calls == 1 {
			return successCommand(in, "kustomize-v1")
		}
		require.Equal(t, []string{"build", overlay, "--load-restrictor", "LoadRestrictionsRootOnly"}, in.Args)
		return successCommand(in, sourceTestManifest)
	}
	source, err := LoadSourceProfile(t.Context(), sourceProfileFixture(t, "name: overlay\nprovider: kustomize\npath: "+overlay+"\nroot: "+root+"\n"), provider.Scope{}, run)
	require.NoError(t, err)
	require.Equal(t, root, source.Identity.InputPath)
	require.NotEmpty(t, source.Identity.InputSHA256)
	require.Equal(t, 2, calls)
	calls = 0
	_, err = LoadSourceProfile(t.Context(), sourceProfileFixture(t, "name: overlay\nprovider: kustomize\npath: "+overlay+"\nroot: "+base+"\n"), provider.Scope{}, run)
	require.ErrorContains(t, err, "input root")
	require.Zero(t, calls)
	calls = 0
	_, err = LoadSourceProfile(t.Context(), sourceProfileFixture(t, "name: overlay\nprovider: kustomize\npath: "+overlay+"\n"), provider.Scope{}, run)
	require.ErrorContains(t, err, "configured local directory")
	require.Zero(t, calls)
}

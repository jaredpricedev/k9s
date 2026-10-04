// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/provider"
	"github.com/stretchr/testify/require"
)

type retainedSourceFixture struct {
	source                   Source
	profile, input, manifest string
	values                   string
	calls                    int
}

func makeRetainedSourceFixture(t *testing.T, kind string) *retainedSourceFixture {
	t.Helper()
	fixture := &retainedSourceFixture{input: sourceTestDirectory(t)}
	fixture.manifest = filepath.Join(fixture.input, sourceTestFilename)
	require.NoError(t, os.WriteFile(fixture.manifest, []byte(sourceTestManifest), 0600))
	path, extra := fixture.input, ""
	switch kind {
	case sourceProviderFile:
		path = fixture.manifest
	case sourceProviderKustomize:
		require.NoError(t, os.WriteFile(filepath.Join(fixture.input, "kustomization.yaml"), []byte("resources: [manifest.yaml]\n"), 0600))
	case sourceProviderHelm:
		fixture.values = sourceTestFile(t, "replicas: 1\n")
		extra = "release: checkout\nnamespace: team-a\nvalues: [" + fixture.values + "]\n"
	case sourceProviderGit:
		extra = "revision: refs/heads/main\nmanifest: manifest.yaml\n"
	default:
		t.Fatal("unsupported retained source fixture provider")
	}
	fixture.profile = sourceProfileFixture(t, "name: retained\nprovider: "+kind+"\npath: "+path+"\n"+extra)
	source, err := LoadSourceProfile(t.Context(), fixture.profile, provider.Scope{}, func(_ context.Context, in provider.Input) provider.Result {
		fixture.calls++
		switch {
		case strings.Contains(strings.Join(in.Args, " "), "rev-parse"):
			return successCommand(in, strings.Repeat("a", 40))
		case strings.Contains(strings.Join(in.Args, " "), "version"):
			return successCommand(in, kind+" fixture-v1")
		default:
			return successCommand(in, sourceTestManifest)
		}
	})
	require.NoError(t, err)
	fixture.source = source
	return fixture
}

func TestRetainedSourceLocalContentAndProfileFingerprints(t *testing.T) {
	t.Run("direct file", func(t *testing.T) {
		path := sourceTestFile(t, sourceTestManifest)
		source, err := LoadSource(t.Context(), path)
		require.NoError(t, err)
		require.NoError(t, VerifyRetainedSource(t.Context(), &source.Identity))
		require.NoError(t, os.WriteFile(path, []byte(sourceTestManifest+"# "+sourceTestToken+"\n"), 0600))
		err = VerifyRetainedSource(t.Context(), &source.Identity)
		require.ErrorContains(t, err, "contents changed")
		require.NotContains(t, err.Error(), sourceTestToken)
	})
	t.Run("configured manifest", func(t *testing.T) {
		fixture := makeRetainedSourceFixture(t, sourceProviderFile)
		require.NoError(t, VerifyRetainedSource(t.Context(), &fixture.source.Identity))
		require.NoError(t, os.WriteFile(fixture.manifest, []byte(sourceTestManifest+"spec: {replicas: 2}\n"), 0600))
		require.ErrorContains(t, VerifyRetainedSource(t.Context(), &fixture.source.Identity), "configured manifest changed")
		require.Zero(t, fixture.calls)
	})
	t.Run("configured profile", func(t *testing.T) {
		fixture := makeRetainedSourceFixture(t, sourceProviderFile)
		original, err := os.ReadFile(fixture.profile)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(fixture.profile, append(original, []byte("# "+sourceTestToken+"\n")...), 0600))
		err = VerifyRetainedSource(t.Context(), &fixture.source.Identity)
		require.ErrorContains(t, err, "profile changed")
		require.NotContains(t, err.Error(), sourceTestToken)
	})
}

func TestRetainedSourceChecksKustomizeInputsAndSeparateHelmValuesWithoutRendering(t *testing.T) {
	for _, kind := range []string{sourceProviderKustomize, sourceProviderHelm} {
		t.Run(kind, func(t *testing.T) {
			fixture := makeRetainedSourceFixture(t, kind)
			calls := fixture.calls
			t.Setenv("PATH", sourceTestDirectory(t))
			require.NoError(t, VerifyRetainedSource(t.Context(), &fixture.source.Identity))
			path := fixture.manifest
			if kind == sourceProviderHelm {
				path = fixture.values
			}
			require.NoError(t, os.WriteFile(path, []byte("changed: "+sourceTestToken+"\n"), 0600))
			err := VerifyRetainedSource(t.Context(), &fixture.source.Identity)
			require.ErrorContains(t, err, "renderer inputs changed")
			require.NotContains(t, err.Error(), sourceTestToken)
			require.Equal(t, calls, fixture.calls)
		})
	}
}

func TestRetainedSourceRendererDirectoryMembershipChangesInvalidateReview(t *testing.T) {
	for _, mutation := range []string{"added", "removed", "renamed"} {
		t.Run(mutation, func(t *testing.T) {
			fixture := makeRetainedSourceFixture(t, sourceProviderHelm)
			switch mutation {
			case "added":
				require.NoError(t, os.WriteFile(filepath.Join(fixture.input, "additional.yaml"), []byte("extra: input\n"), 0600))
			case "removed":
				require.NoError(t, os.Remove(fixture.manifest))
			case "renamed":
				require.NoError(t, os.Rename(fixture.manifest, filepath.Join(fixture.input, "other.yaml")))
			}
			require.ErrorContains(t, VerifyRetainedSource(t.Context(), &fixture.source.Identity), "renderer inputs changed")
		})
	}
}

func TestRetainedSourceProfileCannotBeReboundToAnotherNameProviderOrInput(t *testing.T) {
	fixture := makeRetainedSourceFixture(t, sourceProviderFile)
	for _, mutation := range []string{"name", "provider", "input path", "input fingerprint", "output fingerprint"} {
		t.Run(mutation, func(t *testing.T) {
			identity := fixture.source.Identity
			switch mutation {
			case "name":
				identity.Name = "another-profile"
			case "provider":
				identity.Provider = sourceProviderHelm
			case "input path":
				identity.InputPath = fixture.profile
			case "input fingerprint":
				identity.InputSHA256 = strings.Repeat("b", 64)
			case "output fingerprint":
				identity.SHA256 = strings.Repeat("b", 64)
			}
			require.Error(t, VerifyRetainedSource(t.Context(), &identity))
		})
	}
	fixture = makeRetainedSourceFixture(t, sourceProviderKustomize)
	identity := fixture.source.Identity
	identity.InputPath = filepath.Dir(fixture.input)
	require.ErrorContains(t, VerifyRetainedSource(t.Context(), &identity), "input root differs")
}

func TestRetainedSourceKustomizeExplicitRootIncludesSiblingInputs(t *testing.T) {
	root := sourceTestDirectory(t)
	overlay := filepath.Join(root, "overlay")
	base := filepath.Join(root, "base")
	require.NoError(t, os.MkdirAll(overlay, 0700))
	require.NoError(t, os.MkdirAll(base, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(overlay, "kustomization.yaml"), []byte("resources: [../base]\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "kustomization.yaml"), []byte("resources: [manifest.yaml]\n"), 0600))
	manifest := filepath.Join(base, sourceTestFilename)
	require.NoError(t, os.WriteFile(manifest, []byte(sourceTestManifest), 0600))
	profile := sourceProfileFixture(t, "name: sibling\nprovider: kustomize\npath: "+overlay+"\nroot: "+root+"\n")
	source, err := LoadSourceProfile(t.Context(), profile, provider.Scope{}, func(_ context.Context, in provider.Input) provider.Result {
		if strings.Contains(strings.Join(in.Args, " "), "version") {
			return successCommand(in, "kustomize-v1")
		}
		return successCommand(in, sourceTestManifest)
	})
	require.NoError(t, err)
	require.NoError(t, VerifyRetainedSource(t.Context(), &source.Identity))
	require.NoError(t, os.WriteFile(manifest, []byte(sourceTestManifest+"spec: {replicas: 2}\n"), 0600))
	require.ErrorContains(t, VerifyRetainedSource(t.Context(), &source.Identity), "renderer inputs changed")
}

func TestRetainedSourceCancellationAndFileBoundsFailClosed(t *testing.T) {
	fixture := makeRetainedSourceFixture(t, sourceProviderKustomize)
	for _, deadline := range []bool{false, true} {
		var ctx context.Context
		var cancel context.CancelFunc
		want := context.Canceled
		if deadline {
			ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			want = context.DeadlineExceeded
		} else {
			ctx, cancel = context.WithCancel(t.Context())
			cancel()
		}
		err := VerifyRetainedSource(ctx, &fixture.source.Identity)
		cancel()
		require.ErrorIs(t, err, want)
	}
	require.NoError(t, os.WriteFile(fixture.manifest, []byte(strings.Repeat("x", MaxSourceBytes+1)), 0600))
	require.ErrorContains(t, VerifyRetainedSource(t.Context(), &fixture.source.Identity), "1MiB limit")
	require.Equal(t, 2, fixture.calls)
}

func TestRetainedSourceRejectsFileProfileAndDirectorySymlinkReplacement(t *testing.T) {
	if runtime.GOOS == sourceTestWindows {
		t.Skip("Windows symlink fixtures require additional privileges")
	}
	for _, replacement := range []string{"manifest", "profile", "input directory"} {
		t.Run(replacement, func(t *testing.T) {
			fixture := makeRetainedSourceFixture(t, sourceProviderKustomize)
			path := fixture.manifest
			switch replacement {
			case "profile":
				path = fixture.profile
			case "input directory":
				path = fixture.input
			}
			saved := path + "-retained"
			require.NoError(t, os.Rename(path, saved))
			require.NoError(t, os.Symlink(saved, path))
			err := VerifyRetainedSource(t.Context(), &fixture.source.Identity)
			require.Error(t, err)
			require.Contains(t, err.Error(), "symlink")
			require.Equal(t, 2, fixture.calls)
		})
	}
}

func TestRetainedSourceGitPinsExactCommitWithoutCommandsOrBranchTipSubstitution(t *testing.T) {
	fixture := makeRetainedSourceFixture(t, sourceProviderGit)
	identity := fixture.source.Identity
	bin := sourceTestDirectory(t)
	t.Setenv("PATH", bin)
	// A retained Git source is its already captured committed payload, not the
	// current checkout, mutable branch tip, or repository metadata.
	require.NoError(t, os.Mkdir(filepath.Join(fixture.input, ".git"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(fixture.input, ".git", "HEAD"), []byte(strings.Repeat("b", 40)), 0600))
	require.NoError(t, os.WriteFile(fixture.manifest, []byte(sourceTestManifest+"spec: {replicas: 99}\n"), 0600))
	require.NoError(t, VerifyRetainedSource(t.Context(), &identity))
	require.Equal(t, fixture.source.Identity, identity)
	require.Equal(t, 3, fixture.calls)
	for _, mutation := range []string{"short commit", "uppercase commit", "nonhex commit", "input commit", "requested revision"} {
		t.Run(mutation, func(t *testing.T) {
			changed := identity
			switch mutation {
			case "short commit":
				changed.Revision = "main"
			case "uppercase commit":
				changed.Revision = strings.Repeat("A", 40)
			case "nonhex commit":
				changed.Revision = strings.Repeat("z", 40)
			case "input commit":
				changed.InputSHA256 = strings.Repeat("b", 40)
			case "requested revision":
				changed.RequestedRevision = "HEAD"
			}
			require.ErrorContains(t, VerifyRetainedSource(t.Context(), &changed), "Git commit/source identity unavailable")
		})
	}
	identity.Revision = strings.Repeat("c", 64)
	identity.InputSHA256 = identity.Revision
	require.NoError(t, VerifyRetainedSource(t.Context(), &identity), "full SHA-256 Git object identities are retained exactly")
}

func TestRetainedSourceMalformedIdentityDoesNotReadSource(t *testing.T) {
	for _, identity := range []*SourceIdentity{
		nil,
		{Path: sourceTestToken},
		{Path: sourceTestToken, SHA256: strings.Repeat("z", 64)},
		{Path: sourceTestToken, SHA256: strings.Repeat("A", 64)},
		{Path: sourceTestToken, Provider: sourceProviderFile, SHA256: strings.Repeat("a", 64)},
	} {
		err := VerifyRetainedSource(t.Context(), identity)
		require.Error(t, err)
		require.NotErrorIs(t, err, os.ErrNotExist)
		require.NotContains(t, err.Error(), sourceTestToken)
	}
}

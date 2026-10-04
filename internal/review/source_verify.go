// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
)

// VerifyRetainedSource checks local fingerprints without running a renderer or
// Git command. A Git plan retains its exact immutable commit and payload; a
// branch's current tip is not substituted for the reviewed commit. This check
// does not claim filesystem or multi-resource transaction isolation.
func VerifyRetainedSource(parent context.Context, identity *SourceIdentity) error {
	if identity == nil || !validRetainedDigest(identity.SHA256, sha256.Size*2) || identity.Path == "" {
		return errors.New("captured source identity unavailable; select and review a source")
	}
	captured := *identity
	identity = &captured
	ctx, cancel := context.WithTimeout(parent, ReadTimeout)
	defer cancel()
	if identity.Provider == "" {
		observed, err := LoadSource(ctx, identity.Path)
		if err != nil {
			return err
		}
		if observed.Identity.SHA256 != identity.SHA256 {
			return errors.New("source contents changed; reload and review a new change set")
		}
		if observed.Identity.Path != identity.Path {
			return errors.New("captured source path differs; select and review the source")
		}
		return nil
	}
	if !validRetainedDigest(identity.ProfileSHA256, sha256.Size*2) {
		return errors.New("captured source profile fingerprint unavailable; select and review the source")
	}
	profile, err := openSourceFile(ctx, identity.Path)
	if err != nil {
		return err
	}
	defer profile.close()
	if profile.path != identity.Path {
		return errors.New("captured source profile path differs; select and review the source")
	}
	data, err := readSourceBytes(ctx, profile.file)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != identity.ProfileSHA256 {
		return errors.New("source profile changed; reload and review a new change set")
	}
	spec, err := decodeSourceSpec(ctx, data)
	if err != nil {
		return err
	}
	if spec.Provider != identity.Provider || spec.Name != identity.Name {
		return errors.New("captured profile provider/name differs; select and review the source")
	}
	if err := verifyConfiguredInput(ctx, identity, &spec, filepath.Dir(profile.path)); err != nil {
		return err
	}
	return profile.checkStable(ctx)
}

func verifyConfiguredInput(ctx context.Context, identity *SourceIdentity, spec *SourceSpec, base string) error {
	path, err := configuredPath(base, spec.Path)
	if err != nil {
		return err
	}
	if spec.Provider == sourceProviderFile {
		if path != identity.InputPath {
			return errors.New("captured source input path differs; select and review the source")
		}
		observed, loadErr := LoadSource(ctx, path)
		if loadErr != nil {
			return loadErr
		}
		if observed.Identity.SHA256 != identity.InputSHA256 || observed.Identity.SHA256 != identity.SHA256 {
			return errors.New("configured manifest changed; reload and review a new change set")
		}
		return nil
	}
	root, err := configuredInputRoot(ctx, spec, base, path)
	if err != nil {
		return err
	}
	if root != identity.InputPath {
		return errors.New("captured renderer input root differs; select and review the source")
	}
	directory, err := openConfiguredDirectory(ctx, path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if spec.Provider == sourceProviderGit {
		if !(validRetainedDigest(identity.Revision, 40) || validRetainedDigest(identity.Revision, sha256.Size*2)) ||
			identity.InputSHA256 != identity.Revision || identity.RequestedRevision != spec.Revision || !validGitManifest(spec.Manifest) {
			return errors.New("captured Git commit/source identity unavailable; reload and review the source")
		}
		return ctx.Err()
	}
	plan, err := configureRendererPlan(ctx, spec, base, path, root)
	if err != nil {
		return err
	}
	if plan.inputHash == "" || plan.inputHash != identity.InputSHA256 {
		return errors.New("renderer inputs changed; explicitly render and review a new change set")
	}
	return ctx.Err()
}

func validRetainedDigest(value string, size int) bool {
	if len(value) != size || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

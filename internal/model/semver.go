// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package model

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// SemVer represents a semantic version, retaining prerelease and build metadata.
type SemVer struct {
	Major, Minor, Patch int
	version             string
}

// NewSemVer returns a new semantic version.
func NewSemVer(version string) *SemVer {
	v := &SemVer{}
	normalized := NormalizeVersion(version)
	if !semver.IsValid(normalized) {
		return v
	}
	v.version = normalized
	core := strings.TrimPrefix(semver.Canonical(normalized), "v")
	core, _, _ = strings.Cut(core, "-")
	parts := strings.Split(core, ".")
	v.Major, _ = strconv.Atoi(parts[0])
	v.Minor, _ = strconv.Atoi(parts[1])
	v.Patch, _ = strconv.Atoi(parts[2])
	return v
}

// String returns the version with its original prerelease and build metadata.
func (v *SemVer) String() string {
	if v.version != "" {
		return v.version
	}
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// NormalizeVersion ensures the version starts with a v.
func NormalizeVersion(version string) string {
	if version == "" || version[0] == 'v' {
		return version
	}
	return "v" + version
}

// IsCurrent asserts if at or beyond the latest release in semantic version order.
func (v *SemVer) IsCurrent(latest *SemVer) bool {
	return semver.Compare(v.String(), latest.String()) >= 0
}

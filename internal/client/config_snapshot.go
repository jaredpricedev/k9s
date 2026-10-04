// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package client

import (
	"slices"

	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// SnapshotConfigFlags copies flag values without copying ConfigFlags' internal
// mutexes or mutable client/discovery caches. Asynchronous operations retain the
// original context, authentication selectors and transport customization.
func SnapshotConfigFlags(f *genericclioptions.ConfigFlags) *genericclioptions.ConfigFlags {
	snapshot := genericclioptions.NewConfigFlags(false)
	if f == nil {
		return snapshot
	}
	snapshot.CacheDir, snapshot.KubeConfig = copyConfigValue(f.CacheDir), copyConfigValue(f.KubeConfig)
	snapshot.ClusterName, snapshot.AuthInfoName = copyConfigValue(f.ClusterName), copyConfigValue(f.AuthInfoName)
	snapshot.Context, snapshot.Namespace = copyConfigValue(f.Context), copyConfigValue(f.Namespace)
	snapshot.APIServer, snapshot.TLSServerName = copyConfigValue(f.APIServer), copyConfigValue(f.TLSServerName)
	snapshot.Insecure, snapshot.DisableCompression = copyConfigValue(f.Insecure), copyConfigValue(f.DisableCompression)
	snapshot.CertFile, snapshot.KeyFile, snapshot.CAFile = copyConfigValue(f.CertFile), copyConfigValue(f.KeyFile), copyConfigValue(f.CAFile)
	snapshot.BearerToken, snapshot.Impersonate, snapshot.ImpersonateUID = copyConfigValue(f.BearerToken), copyConfigValue(f.Impersonate), copyConfigValue(f.ImpersonateUID)
	snapshot.ImpersonateGroup, snapshot.ImpersonateUserExtra = copyConfigSlice(f.ImpersonateGroup), copyConfigSlice(f.ImpersonateUserExtra)
	snapshot.Username, snapshot.Password, snapshot.Timeout = copyConfigValue(f.Username), copyConfigValue(f.Password), copyConfigValue(f.Timeout)
	snapshot.WrapConfigFn = f.WrapConfigFn
	return snapshot
}

func copyConfigValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	snapshot := *value
	return &snapshot
}

func copyConfigSlice(value *[]string) *[]string {
	if value == nil {
		return nil
	}
	snapshot := slices.Clone(*value)
	return &snapshot
}

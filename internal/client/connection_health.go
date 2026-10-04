// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package client

import (
	"errors"

	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// PinnedDiagnosticConfig captures the configured actor's flags on the navigation
// goroutine, with a fresh kubeconfig loader and an explicit context override.
// Reloading this private config neither changes kubeconfig's current-context nor
// mutates any of the running connection's cached transports.
func (c *Config) PinnedDiagnosticConfig(contextName string) (*Config, error) {
	if c == nil || c.flags == nil || contextName == "" {
		return nil, errors.New("no configured context for connection diagnostics")
	}
	f := c.flags
	flags := genericclioptions.NewConfigFlags(false)
	flags.Context = &contextName
	flags.KubeConfig = diagnosticFlagCopy(f.KubeConfig)
	flags.CacheDir = diagnosticFlagCopy(f.CacheDir)
	flags.ClusterName = diagnosticFlagCopy(f.ClusterName)
	flags.AuthInfoName = diagnosticFlagCopy(f.AuthInfoName)
	flags.Namespace = diagnosticFlagCopy(f.Namespace)
	flags.APIServer = diagnosticFlagCopy(f.APIServer)
	flags.TLSServerName = diagnosticFlagCopy(f.TLSServerName)
	flags.Insecure = diagnosticFlagCopy(f.Insecure)
	flags.CertFile = diagnosticFlagCopy(f.CertFile)
	flags.KeyFile = diagnosticFlagCopy(f.KeyFile)
	flags.CAFile = diagnosticFlagCopy(f.CAFile)
	flags.BearerToken = diagnosticFlagCopy(f.BearerToken)
	flags.Impersonate = diagnosticFlagCopy(f.Impersonate)
	flags.ImpersonateUID = diagnosticFlagCopy(f.ImpersonateUID)
	flags.ImpersonateGroup = diagnosticSliceCopy(f.ImpersonateGroup)
	flags.ImpersonateUserExtra = diagnosticSliceCopy(f.ImpersonateUserExtra)
	flags.Username = diagnosticFlagCopy(f.Username)
	flags.Password = diagnosticFlagCopy(f.Password)
	if flags.Password != nil && *flags.Password == "" {
		// An empty deprecated password flag must not start an interactive prompt.
		flags.Password = nil
	}
	flags.Timeout = diagnosticFlagCopy(f.Timeout)
	flags.DisableCompression = diagnosticFlagCopy(f.DisableCompression)
	return &Config{flags: flags, proxy: c.proxy}, nil
}

func diagnosticFlagCopy[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func diagnosticSliceCopy(value *[]string) *[]string {
	if value == nil {
		return nil
	}
	cloned := append([]string(nil), (*value)...)
	return &cloned
}

// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package cmd

import (
	"bytes"
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/stretchr/testify/require"
)

func TestInformationalOutputWithoutColor(t *testing.T) {
	t.Setenv("K9PLUS_CONFIG_DIR", t.TempDir())
	for _, location := range []*string{&config.AppConfigDir, &config.AppSkinsDir, &config.AppBenchmarksDir, &config.AppDumpsDir, &config.AppContextsDir, &config.AppConfigFile, &config.AppLogFile, &config.AppViewsFile, &config.AppJumpsFile, &config.AppAliasesFile, &config.AppPluginsFile, &config.AppHotKeysFile} {
		previous := *location
		t.Cleanup(func() { *location = previous })
	}
	for _, noColor := range []string{"", "1"} {
		t.Run("NO_COLOR="+noColor, func(t *testing.T) {
			t.Setenv("NO_COLOR", noColor)
			t.Setenv("TERM", "dumb")
			var buffer bytes.Buffer
			previous := out
			out = &buffer
			t.Cleanup(func() { out = previous })
			printVersion(false)
			require.NotContains(t, buffer.String(), "\x1b[")
			require.Contains(t, buffer.String(), "Version")
			buffer.Reset()
			require.NoError(t, printInfo(nil, nil))
			require.NotContains(t, buffer.String(), "\x1b[")
			require.Contains(t, buffer.String(), "Config")
		})
	}
}

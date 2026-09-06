<!-- Modified for k9+; see NOTICE. -->

<!-- Copyright 2026 the k9+ contributors. SPDX-License-Identifier: Apache-2.0 -->

# Moving from k9s to k9+

The display name is **k9+**. The portable executable, release package and storage
name are **k9plus**. Both apps can be installed together. k9+ does not automatically
read or migrate k9s settings, plugins or environment variables.

## Find the directories

Run `k9s info` and `k9plus info` to see each application's exact paths on your
platform. With default XDG locations on Linux:

| Contents | k9s | k9+ |
| --- | --- | --- |
| Global configuration, skins and plugins | `~/.config/k9s/` | `~/.config/k9plus/` |
| Context settings | `~/.local/share/k9s/clusters/` | `~/.local/share/k9plus/clusters/` |
| Logs, dumps and benchmark results | `~/.local/state/k9s/` | `~/.local/state/k9plus/` |

macOS, Windows and custom XDG environments can use different base directories;
`info` is authoritative. The log filename is `k9plus.log`. Setting
`K9PLUS_CONFIG_DIR` puts configuration, skins, clusters, dumps and benchmarks
under that directory. `K9PLUS_LOGS_DIR` independently controls the log directory.

## Copy settings deliberately

1. Close both applications and keep a backup of any existing destination files.
2. Copy the desired `config.yaml`, `aliases.yaml`, `hotkeys.yaml`, `views.yaml`,
   `jumps.yaml`, `skins/`, and selected context settings from the paths reported
   by `k9s info` into the corresponding paths reported by `k9plus info`.
3. Keep the YAML root **`k9s:`**. Skin keys, plugin formats and schemas are compatible.
   Check explicit absolute paths such as `screenDumpDir`, plus hotkey commands
   that launch `k9s`, if you want them to use the new application or its storage.
4. Review and copy plugins you want enabled. k9+ discovers plugins only in its
   own configuration and XDG plugin locations. Native Flux `Shift-R`/`Shift-T`
   take precedence over copied legacy Flux plugins; optional CLI actions use
   separate shortcuts. See [plugin setup](../plugins/README.md).
5. Launch `k9plus` and check `?`, `:flux`, and your context settings.

Copying settings is optional. Starting fresh leaves both apps independent.
Pointing the two apps at the same explicit directory intentionally shares files
and defeats that isolation.

## Environment variables

Replace the old prefix in your shell configuration for the settings you want
to carry over. k9+ ignores the `K9S_*` counterparts of these variables.

| k9+ variable | Purpose |
| --- | --- |
| `K9PLUS_CONFIG_DIR` | Configuration root |
| `K9PLUS_LOGS_DIR` | Log directory |
| `K9PLUS_SKIN` | Skin selection |
| `K9PLUS_EDITOR` | External editor; otherwise `KUBE_EDITOR` / `EDITOR` |
| `K9PLUS_DEFAULT_PF_ADDRESS` | Default port-forward bind address |
| `K9PLUS_FEATURE_GATE_NODE_SHELL` | Node-shell feature gate |
| `K9PLUS_CLIPBOARD` | Clipboard mode |
| `K9PLUS_OSC52_MAX` | OSC52 encoded payload limit |

`KUBECONFIG`, standard Kubernetes authentication, and shared `EDITOR`/XDG
variables retain their normal meanings. Node-shell pods use the `k9plus-shell-`
prefix. Existing Kubernetes annotation conventions such as
`k9scli.io/auto-port-forwards` are retained for interoperability.

## Maintaining releases

The repository is currently `jaredpricedev/k9s`; the rebrand does not rename it.
Release checks use that repository, and an empty release history is handled
normally. Build metadata defaults to `v0.1.0-dev`. Before your first public
release choose an unused version tag: inherited upstream tags may already use
`v0.1.0`. Release automation packages `k9plus` and does not publish into upstream
Homebrew or container destinations.

The internal Go module path remains `github.com/derailed/k9s` to preserve imports
and facilitate upstream updates. Build from this fork's checkout. Changing the
module path or repository name later is a separate migration.

Read [licensing](licensing.md) before distributing a release. The source rebrand
and license inventory do not constitute trademark clearance of the name.

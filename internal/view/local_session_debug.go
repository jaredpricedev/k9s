// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	goflag "flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/derailed/k9s/internal/client"
	"github.com/google/shlex"
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"
)

const localCommandArgsEnd = "--"

// Native debug creates a Pod or patches ephemeralcontainers even when a plugin
// omitted its Dangerous declaration. Endpoint-only forwarding remains separate.
func kubectlDebugCommand(command string, args []string) bool {
	_, debug := kubectlDebugArgs(command, args)
	return debug
}

func kubectlDebugArgs(command string, args []string) ([]string, bool) {
	binary := filepath.Base(command)
	if binary != nativeKubectlCommand && binary != "kubectl.exe" {
		return nil, false
	}
	flags := kubectlCommandFlags()
	var leading []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == localCommandArgsEnd {
			return nil, false
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			if arg != "debug" {
				return nil, false
			}
			return append(leading, args[index+1:]...), true
		}
		known, consume := kubectlLeadingFlag(flags, arg)
		if !known {
			leading = append(leading, arg)
		}
		if consume {
			index++
			if index >= len(args) {
				return nil, false
			}
			if !known {
				leading = append(leading, args[index])
			}
		}
	}
	return nil, false
}

// Cobra permits subcommand flags before the verb. Unknown long flags consume a
// separate value; bundled short flags do not. Retain these for debug-mode RBAC.
func kubectlLeadingFlag(flags *pflag.FlagSet, arg string) (known, consume bool) {
	if strings.HasPrefix(arg, localCommandArgsEnd) {
		name, _, attached := strings.Cut(arg[2:], "=")
		flag := flags.Lookup(name)
		required := flag == nil && !kubectlDebugBooleanFlag(name) || flag != nil && flag.NoOptDefVal == ""
		return flag != nil, !attached && required
	}
	flag := flags.ShorthandLookup(arg[1:2])
	required := flag == nil && !kubectlDebugBooleanFlag(arg[1:2]) || flag != nil && flag.NoOptDefVal == ""
	return flag != nil, len(arg) == 2 && required
}

func kubectlDebugBooleanFlag(name string) bool {
	switch strings.ReplaceAll(name, "_", "-") {
	case "stdin", "i", "tty", "t", "quiet", "q", "attach", "arguments-only", "replace", "same-node", "share-processes",
		"keep-labels", "keep-annotations", "keep-liveness", "keep-readiness", "keep-startup", "keep-init-containers":
		return true
	}
	return false
}

// Inspect only flag arity: do not load kubeconfig, validate credentials, or set
// shared klog values while identifying the first native subcommand.
func kubectlCommandFlags() *pflag.FlagSet {
	metadata := pflag.NewFlagSet("kubectl", pflag.ContinueOnError)
	metadata.SetNormalizeFunc(cliflag.WordSepNormalizeFunc)
	genericclioptions.NewConfigFlags(true).WithDeprecatedPasswordFlag().AddFlags(metadata)
	logging := goflag.NewFlagSet("logging", goflag.ContinueOnError)
	klog.InitFlags(logging)
	metadata.AddGoFlagSet(logging)
	for _, name := range []string{"profile", "profile-output", "kuberc", "log-flush-frequency"} {
		metadata.String(name, "", "")
	}
	for _, name := range []string{"warnings-as-errors", "match-server-version"} {
		metadata.Bool(name, false, "")
	}
	metadata.BoolP("help", "h", false, "")
	flags := pflag.NewFlagSet("kubectl", pflag.ContinueOnError)
	flags.SetNormalizeFunc(cliflag.WordSepNormalizeFunc)
	flags.SetInterspersed(false)
	flags.SetOutput(io.Discard)
	metadata.VisitAll(func(flag *pflag.Flag) {
		flags.StringP(flag.Name, flag.Shorthand, "", "")
		flags.Lookup(flag.Name).NoOptDefVal = flag.NoOptDefVal
	})
	return flags
}

type pluginCommandStage struct {
	command string
	args    []string
}

func pluginCommandStages(command string, args, pipes []string) []pluginCommandStage {
	stages := []pluginCommandStage{{command: command, args: args}}
	for _, pipe := range pipes {
		tokens, err := shlex.Split(pipe)
		// execute rejects malformed stages before starting any child.
		if err == nil && len(tokens) > 0 {
			stages = append(stages, pluginCommandStage{command: tokens[0], args: tokens[1:]})
		}
	}
	return stages
}

func pluginDebugCommands(command string, args, pipes []string) [][]string {
	var commands [][]string
	for _, stage := range pluginCommandStages(command, args, pipes) {
		if debugArgs, debug := kubectlDebugArgs(stage.command, stage.args); debug {
			commands = append(commands, debugArgs)
		}
	}
	return commands
}

func pluginUsesNativeDestination(command string, pipes []string) bool {
	for _, stage := range pluginCommandStages(command, nil, pipes) {
		switch filepath.Base(stage.command) {
		case nativeKubectlCommand, "kubectl.exe", "helm", "helm.exe":
			return true
		}
	}
	return false
}

func (i *pluginInvocation) guardDebugWrite() error {
	i.plugin.Dangerous, i.debugWrite = true, true
	if i.runner.App().Config.IsReadOnly() {
		return fmt.Errorf("debug-container creation is unavailable in read-only mode; endpoint-only port-forward access is separate")
	}
	if !i.current() {
		return fmt.Errorf("debug action destination or selection changed; reopen it")
	}
	if i.session == nil {
		if viewer, ok := i.runner.(ResourceViewer); ok {
			if err := checkOperationTarget(&i.target); err != nil {
				return err
			}
			session, err := captureOperation(viewer)
			if err != nil {
				return err
			}
			i.session = session
		}
	}
	return nil
}

func (i *pluginInvocation) authorizeDebugWrite(ctx context.Context, target *SelectedResourceTarget, args []string) error {
	for _, command := range pluginDebugCommands(i.plugin.Command, args, i.plugin.Pipes) {
		if err := i.authorizeDebugCommand(ctx, target, command); err != nil {
			return err
		}
	}
	return nil
}

func (i *pluginInvocation) authorizeDebugCommand(ctx context.Context, target *SelectedResourceTarget, args []string) error {
	copyPod := false
	for _, arg := range args {
		if arg == localCommandArgsEnd {
			break
		}
		if arg == "--copy-to" || strings.HasPrefix(arg, "--copy-to=") {
			copyPod = true
			break
		}
	}
	if target.GVR != nil && target.GVR.GVR() == client.PodGVR.GVR() && !copyPod {
		return i.session.authorize(ctx, target, "ephemeralcontainers", client.PatchVerb)
	}
	created := SelectedResourceTarget{Context: i.contextName, GVR: client.PodGVR, Namespace: i.namespace}
	return i.session.authorize(ctx, &created, "", client.CreateVerb)
}

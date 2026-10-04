// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/k9s/internal/ui/dialog"
	"github.com/fatih/color"
	"github.com/google/shlex"
	v1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	shellCheck    = `command -v bash >/dev/null && exec bash || exec sh`
	winShellCheck = `where powershell >nul 2>&1 && powershell || cmd`
	bannerFmt     = "<<k9+-Shell>> Pod: %s | Container: %s \n"
	outputPrefix  = "[output]"
)

var errExternalOperationOutcome = errors.New("external command may have applied remote effects")
var commandTerminalSlot = make(chan struct{}, 1)

var editorEnvVars = []string{"K9PLUS_EDITOR", "KUBE_EDITOR", "EDITOR"}

type shellOpts struct {
	clear, background bool
	pipes             []string
	binary            string
	banner            string
	args              []string
	ctx               context.Context
	timeout           time.Duration
	env               []string
	terminalOwned     bool
}

func runK(a *App, opts *shellOpts) error {
	bin, err := exec.LookPath(nativeKubectlCommand)
	if errors.Is(err, exec.ErrDot) {
		return fmt.Errorf("kubectl command must not be in the current working directory: %w", err)
	}
	if err != nil {
		return fmt.Errorf("kubectl command is not in your path: %w", err)
	}
	args := []string{opts.args[0]}
	if u, err := a.Conn().Config().ImpersonateUser(); err == nil {
		args = append(args, "--as", u)
	}
	if g, err := a.Conn().Config().ImpersonateGroups(); err == nil {
		args = append(args, "--as-group", g)
	}
	if isInsecure := a.Conn().Config().Flags().Insecure; isInsecure != nil && *isInsecure {
		args = append(args, "--insecure-skip-tls-verify")
	}
	args = append(args, "--context", a.Config.K9s.ActiveContextName())
	if cfg := a.Conn().Config().Flags().KubeConfig; cfg != nil && *cfg != "" {
		args = append(args, "--kubeconfig", *cfg)
	}
	if len(args) > 0 {
		opts.args = append(args, opts.args[1:]...)
	}
	opts.binary = bin

	suspended, errChan, stChan := run(a, opts)
	if !suspended {
		return fmt.Errorf("unable to run command")
	}
	for v := range stChan {
		slog.Debug("stdout", slogs.Line, v)
	}
	var errs error
	for e := range errChan {
		errs = errors.Join(errs, e)
	}

	return errs
}

func run(a *App, opts *shellOpts) (ok bool, errC chan error, outC chan string) {
	if opts.ctx == nil && a != nil {
		opts.ctx = a.sessionContext()
	}
	errChan := make(chan error, 1)
	statusChan := make(chan string, 1)

	if opts.background {
		go func() {
			if err := execute(opts, statusChan); err != nil {
				errChan <- err
			}
			close(errChan)
		}()
		return true, errChan, statusChan
	}
	ctx := opts.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, maxOperationDeadline)
	defer cancel()
	releaseTerminal, err := acquireCommandTerminal(ctx)
	if err != nil {
		errChan <- err
		close(errChan)
		close(statusChan)
		return false, errChan, statusChan
	}
	defer releaseTerminal()
	opts.ctx, opts.terminalOwned = ctx, true

	a.Halt()
	defer a.Resume()

	return a.Suspend(func() {
		completed := make(chan error, 1)
		go func() { completed <- execute(opts, statusChan) }()
		if err := <-completed; err != nil {
			errChan <- err
			a.Flash().Errf("Exec failed: %s", err)
		}
		close(errChan)
	}), errChan, statusChan
}

func edit(a *App, opts *shellOpts) bool {
	var (
		bin string
		err error
	)
	for _, e := range editorEnvVars {
		env := os.Getenv(e)
		if env == "" {
			continue
		}

		// There may be situations where the user sets the editor as the binary
		// followed by some arguments (e.g. "code -w" to make it work with vscode)
		//
		// In such cases, the actual binary is only the first token
		envTokens, shlexErr := shlex.Split(env)
		if shlexErr != nil || len(envTokens) == 0 {
			continue
		}

		if bin, err = exec.LookPath(envTokens[0]); err == nil {
			// Make sure the path is at the end (this allows running editors
			// with custom options)
			if len(envTokens) > 1 {
				originalArgs := opts.args
				opts.args = envTokens[1:]
				opts.args = append(opts.args, originalArgs...)
			}

			break
		}
	}
	if bin == "" {
		a.Flash().Errf("You must set at least one of those env vars: %s", strings.Join(editorEnvVars, "|"))
		return false
	}
	opts.binary, opts.background = bin, false

	suspended, errChan, _ := run(a, opts)
	if !suspended {
		a.Flash().Errf("edit command failed")
	}
	status := true
	for e := range errChan {
		a.Flash().Err(e)
		status = false
	}

	return status
}

func shellQuote(s string) string {
	if !strings.ContainsAny(s, " \t\n\"'\\") {
		return s
	}
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func execute(opts *shellOpts, statusChan chan<- string) (result error) {
	if opts.clear {
		clearScreen()
	}
	parent := opts.ctx
	if parent == nil {
		parent = context.Background()
	}
	timeout := opts.timeout
	if timeout <= 0 {
		timeout = maxOperationDeadline
	}
	deadlineCtx, deadlineCancel := context.WithTimeout(parent, boundedOperationTimeout(timeout))
	defer deadlineCancel()
	ctx, cancel := signal.NotifyContext(deadlineCtx, terminationSignals()...)
	defer func() {
		cancel()
		if !opts.background {
			clearScreen()
		}
	}()
	defer close(statusChan)
	if !opts.background && !opts.terminalOwned {
		releaseTerminal, acquireErr := acquireCommandTerminal(ctx)
		if acquireErr != nil {
			return acquireErr
		}
		defer releaseTerminal()
	}
	restoreTerminal, err := commandTerminalLease(opts.background)
	if err != nil {
		return fmt.Errorf("terminal ownership is unavailable: %w", err)
	}
	defer func() { result = errors.Join(result, restoreTerminal()) }()

	cmds := make([]*exec.Cmd, 0, 1)
	cmd := exec.CommandContext(ctx, opts.binary, opts.args...)
	cmd.Env = opts.env
	configureCommandCancellation(cmd, opts.background)
	slog.Debug("Exec command", slogs.Bin, opts.binary)

	if env := os.Getenv("K9PLUS_EDITOR"); env != "" {
		// There may be situations where the user sets the editor as the binary
		// followed by some arguments (e.g. "code -w" to make it work with vscode)
		//
		// In such cases, the actual binary is only the first token
		if binTokens, parseErr := shlex.Split(env); parseErr == nil && len(binTokens) > 0 {
			if bin, lookupErr := exec.LookPath(binTokens[0]); lookupErr == nil {
				binTokens[0] = bin
				for i := range binTokens {
					binTokens[i] = shellQuote(binTokens[i])
				}
				if cmd.Env == nil {
					cmd.Env = os.Environ()
				}
				cmd.Env = append(cmd.Env, fmt.Sprintf("KUBE_EDITOR=%s", strings.Join(binTokens, " ")))
			}
		}
	}

	cmds = append(cmds, cmd)

	for _, p := range opts.pipes {
		if len(cmds) >= 8 {
			return errors.New("command pipelines are limited to 8 stages")
		}
		tokens, parseErr := shlex.Split(p)
		if parseErr != nil || len(tokens) == 0 {
			return errors.New("configured pipeline stage is invalid; review it before running the action")
		}
		cmd := exec.CommandContext(ctx, tokens[0], tokens[1:]...)
		cmd.Env = opts.env
		configureCommandCancellation(cmd, opts.background)
		slog.Debug("Exec pipeline", slogs.Bin, tokens[0])
		cmds = append(cmds, cmd)
	}

	err = pipe(ctx, opts, statusChan, cmds...)
	if ctx.Err() != nil {
		return errors.Join(err, ctx.Err())
	}
	if err != nil {
		// Arguments and output can contain plugin inputs or Secret values. Keep
		// those out of logs and retained operation errors.
		return fmt.Errorf("external command failed; inspect its destination before retrying: %w", err)
	}

	return nil
}

// A canceled waiter cannot start another foreground handoff later. This also
// covers legacy interactive callers that can run outside the event dispatcher.
func acquireCommandTerminal(ctx context.Context) (func(), error) {
	select {
	case commandTerminalSlot <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-commandTerminalSlot
			return nil, err
		}
		return func() { <-commandTerminalSlot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func clearScreen() {
	fmt.Print("\033[H\033[2J")
}

const (
	k9sShell           = "k9plus-shell"
	k9sShellRetryCount = 50
	k9sShellRetryDelay = 2 * time.Second
)

func launchNodeShell(v model.Igniter, a *App, node string) {
	if err := nukeK9sShell(a); err != nil {
		a.Flash().Errf("Cleaning node shell failed: %s", err)
		return
	}

	msg := fmt.Sprintf("Launching node shell on %s...", node)
	d := a.Styles.Dialog()
	dialog.ShowPrompt(&d, a.Content.Pages, "Launching", msg, func(ctx context.Context) {
		err := launchShellPod(ctx, a, node)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				a.Flash().Errf("Launching node shell failed: %s", err)
			}
			return
		}

		go launchPodShell(v, a)
	}, func() {
		if err := nukeK9sShell(a); err != nil {
			a.Flash().Errf("Cleaning node shell failed: %s", err)
			return
		}
	})
}

func launchPodShell(v model.Igniter, a *App) {
	if a.Config.K9s.ShellPod == nil {
		slog.Error("Shell pod not configured!")
		return
	}

	defer func() {
		if err := nukeK9sShell(a); err != nil {
			a.Flash().Errf("Launching node shell failed: %s", err)
			return
		}
	}()

	v.Stop()
	defer v.Start()

	ns := a.Config.K9s.ShellPod.Namespace
	if err := sshIn(a, client.FQN(ns, k9sShellPodName()), k9sShell); err != nil {
		a.Flash().Errf("Launching node shell failed: %s", err)
	}
}

func sshIn(a *App, fqn, co string) error {
	cfg := a.Config.K9s.ShellPod
	platform, err := getPodOS(a.factory, fqn)
	if err != nil {
		slog.Warn("os detect failed", slogs.Error, err)
	}

	args := buildShellArgs("exec", fqn, co, a.Conn().Config().Flags())
	args = append(args, "--")
	if len(cfg.Command) > 0 {
		args = append(args, cfg.Command...)
		args = append(args, cfg.Args...)
	} else {
		if platform == windowsOS {
			args = append(args, "--", "cmd", "/c", winShellCheck)
		}
		args = append(args, "sh", "-c", shellCheck)
	}
	slog.Debug("Running command with args", slogs.Args, args)

	c := color.New(color.BgGreen).Add(color.FgBlack).Add(color.Bold)
	err = runK(a, &shellOpts{
		clear:  true,
		banner: c.Sprintf(bannerFmt, fqn, co),
		args:   args},
	)
	if err != nil {
		return fmt.Errorf("shell exec failed: %w", err)
	}

	return nil
}

func nukeK9sShell(a *App) error {
	ct, err := a.Config.K9s.ActiveContext()
	if err != nil {
		return err
	}
	if !ct.FeatureGates.NodeShell || a.Config.K9s.ShellPod == nil {
		return nil
	}

	ns := a.Config.K9s.ShellPod.Namespace
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	dial, err := a.Conn().Dial()
	if err != nil {
		return err
	}

	err = dial.CoreV1().Pods(ns).Delete(ctx, k9sShellPodName(), metav1.DeleteOptions{})
	if kerrors.IsNotFound(err) {
		return nil
	}

	return err
}

func launchShellPod(ctx context.Context, a *App, node string) error {
	var (
		spo  = a.Config.K9s.ShellPod
		spec = k9sShellPod(node, spo)
	)

	dial, err := a.Conn().Dial()
	if err != nil {
		return err
	}

	conn := dial.CoreV1().Pods(spo.Namespace)
	if _, err = conn.Create(ctx, spec, metav1.CreateOptions{}); err != nil {
		return err
	}

	for i := range k9sShellRetryCount {
		o, err := a.factory.Get(client.PodGVR, client.FQN(spo.Namespace, k9sShellPodName()), true, labels.Everything())
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(k9sShellRetryDelay):
				continue
			}
		}

		var pod v1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(o.(*unstructured.Unstructured).Object, &pod); err != nil {
			return err
		}
		slog.Debug("Checking k9+ shell pod retries",
			slogs.Retry, i,
			slogs.PodPhase, pod.Status.Phase,
		)
		if pod.Status.Phase == v1.PodRunning {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(k9sShellRetryDelay):
		}
	}

	return fmt.Errorf("unable to launch shell pod on node %s", node)
}

func k9sShellPodName() string {
	return fmt.Sprintf("%s-%d", k9sShell, os.Getpid())
}

func k9sShellPod(node string, cfg *config.ShellPod) *v1.Pod {
	var grace int64
	var priv = true

	slog.Debug("Shell pod config", slogs.ShellPodCfg, cfg)
	c := v1.Container{
		Name:            k9sShell,
		Image:           cfg.Image,
		ImagePullPolicy: cfg.ImagePullPolicy,
		VolumeMounts: []v1.VolumeMount{
			{
				Name:      "root-vol",
				MountPath: "/host",
				ReadOnly:  true,
			},
		},
		Resources: asResource(cfg.Limits),
		Stdin:     true,
		TTY:       cfg.TTY,
		SecurityContext: &v1.SecurityContext{
			Privileged: &priv,
		},
	}
	v := []v1.Volume{
		{
			Name: "root-vol",
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: "/",
				},
			},
		},
	}
	if len(cfg.Command) != 0 {
		c.Command = cfg.Command
	}
	if len(cfg.Args) > 0 {
		c.Args = cfg.Args
	}
	if len(cfg.HostPathVolume) > 0 {
		for _, h := range cfg.HostPathVolume {
			c.VolumeMounts = append(c.VolumeMounts, v1.VolumeMount{
				Name:      h.Name,
				MountPath: h.MountPath,
				ReadOnly:  h.ReadOnly,
			})
			v = append(v, v1.Volume{
				Name: h.Name,
				VolumeSource: v1.VolumeSource{
					HostPath: &v1.HostPathVolumeSource{
						Path: h.HostPath,
					},
				},
			})
		}
	}
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      k9sShellPodName(),
			Namespace: cfg.Namespace,
			Labels:    cfg.Labels,
		},
		Spec: v1.PodSpec{
			NodeName:                      node,
			RestartPolicy:                 v1.RestartPolicyNever,
			HostPID:                       true,
			HostNetwork:                   true,
			ImagePullSecrets:              cfg.ImagePullSecrets,
			TerminationGracePeriodSeconds: &grace,
			Volumes:                       v,
			Containers:                    []v1.Container{c},
			Tolerations: []v1.Toleration{
				{
					Operator: v1.TolerationOperator("Exists"),
				},
			},
		},
	}
}

func asResource(r config.Limits) v1.ResourceRequirements {
	return v1.ResourceRequirements{
		Limits: v1.ResourceList{
			v1.ResourceCPU:    resource.MustParse(r[v1.ResourceCPU]),
			v1.ResourceMemory: resource.MustParse(r[v1.ResourceMemory]),
		},
	}
}

func pipe(ctx context.Context, opts *shellOpts, statusChan chan<- string, cmds ...*exec.Cmd) error {
	if len(cmds) == 0 {
		return nil
	}

	if len(cmds) == 1 {
		return pipeSingle(ctx, opts, statusChan, cmds[0])
	}

	last := len(cmds) - 1
	output, diagnostic := new(commandCapture), new(commandCapture)
	var closers []io.Closer
	defer func() {
		for _, closer := range closers {
			_ = closer.Close()
		}
	}()
	for i := range cmds {
		if opts.background {
			cmds[i].Stderr = diagnostic
		} else {
			cmds[i].Stderr = os.Stderr
		}
		if i+1 < len(cmds) {
			r, w := io.Pipe()
			closers = append(closers, r, w)
			cmds[i].Stdout, cmds[i+1].Stdin = w, r
		}
	}
	if opts.background {
		cmds[last].Stdout = output
	} else {
		cmds[last].Stdout, cmds[0].Stdin = os.Stdout, os.Stdin
	}

	// Start the output reader first so a foreground pipeline's group remains
	// available while its producer starts. Background stages each retain their
	// own cancellable group. Never retry a stage after any child has started.
	started := make([]*exec.Cmd, 0, len(cmds))
	for i := last; i >= 0; i-- {
		cmd := cmds[i]
		if i != last {
			joinCommandGroup(cmd, cmds[last])
		}
		if err := cmd.Start(); err != nil {
			for _, child := range started {
				if child.Cancel != nil {
					_ = child.Cancel()
				} else {
					_ = child.Process.Kill()
				}
				_ = child.Wait()
			}
			if len(started) > 0 {
				return errors.Join(err, errExternalOperationOutcome)
			}
			return err
		}
		started = append(started, cmd)
		operationBeginWrite(ctx)
	}
	results := make(chan error, len(cmds))
	for i, cmd := range cmds {
		go func() {
			err := cmd.Wait()
			if i < last {
				_ = closers[2*i+1].Close()
			}
			if i > 0 {
				_ = closers[2*(i-1)].Close()
			}
			results <- err
		}()
	}
	var result error
	for range cmds {
		result = errors.Join(result, <-results)
	}
	if result == nil {
		if opts.background {
			statusChan <- outputPrefix + " " + output.String()
		}
		operationCommandCompleted(ctx)
	}
	if result != nil {
		result = errors.Join(result, errExternalOperationOutcome)
	}
	return result
}

type commandCapture struct {
	mu      sync.Mutex
	text    strings.Builder
	dropped int
}

func (c *commandCapture) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := min(len(data), max(0, 32*1024-c.text.Len()))
	c.text.Write(data[:n])
	c.dropped += len(data) - n
	return len(data), nil
}
func (c *commandCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	text := c.text.String()
	if c.dropped > 0 {
		text += fmt.Sprintf("\nOutput truncated: %d bytes omitted.\n", c.dropped)
	}
	return text
}

func pipeSingle(ctx context.Context, opts *shellOpts, statusChan chan<- string, cmd *exec.Cmd) error {
	if opts.background {
		output, diagnostic := new(commandCapture), new(commandCapture)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, output, diagnostic
		if err := cmd.Start(); err != nil {
			return err
		}
		operationBeginWrite(ctx)
		err := cmd.Wait()
		if err == nil {
			statusChan <- outputPrefix + " " + output.String()
			operationCommandCompleted(ctx)
		}
		if err != nil {
			return errors.Join(err, errExternalOperationOutcome)
		}
		return nil
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	_, _ = cmd.Stdout.Write([]byte(opts.banner))

	slog.Debug("Exec started")
	if err := cmd.Start(); err != nil {
		return err
	}
	operationBeginWrite(ctx)
	err := cmd.Wait()
	slog.Debug("Command exec done", slogs.Error, err)
	if err == nil {
		statusChan <- "External command exited successfully"
		operationCommandCompleted(ctx)
	}

	if err != nil {
		err = errors.Join(err, errExternalOperationOutcome)
	}

	return err
}

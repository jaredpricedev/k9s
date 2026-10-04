// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/ui/dialog"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// fingerprintOperationSource is metadata, not retained manifest contents. Bound
// directory traversal and reject nonregular entries so pipes/devices cannot
// make a source review or cancellation wait indefinitely.
func fingerprintOperationSource(ctx context.Context, source string) (string, error) {
	canonical, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", err
	}
	sourceInfo, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	rootPath := canonical
	if !sourceInfo.IsDir() {
		rootPath = filepath.Dir(canonical)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	hash := sha256.New()
	files, total := 0, int64(0)
	err = filepath.WalkDir(canonical, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("operation source must contain regular files only")
		}
		files++
		if files > 500 || total+info.Size() > 16*1024*1024 {
			return fmt.Errorf("operation source is limited to 500 files and 16 MiB")
		}
		relative, relativeErr := filepath.Rel(rootPath, path)
		if relativeErr != nil {
			return relativeErr
		}
		file, openErr := root.Open(relative)
		if openErr != nil {
			return openErr
		}
		fmt.Fprintf(hash, "%s\x00%d\x00%d\x00", path, info.Mode(), info.Size())
		copied, copyErr := io.Copy(hash, io.LimitReader(file, 16*1024*1024-total+1))
		closeErr := file.Close()
		total += copied
		if total > 16*1024*1024 {
			return fmt.Errorf("operation source grew beyond the 16 MiB limit")
		}
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return "", err
	}
	if files == 0 {
		return "", fmt.Errorf("operation source contains no files")
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}

// cliDestinationSnapshot serializes only the reviewed destination into a
// temporary 0600 kubeconfig. Every ConfigFlags override is resolved through the
// normal loader; bearer tokens/passwords/certificate data stay out of argv and
// receipts. External commands cannot silently use a later default context.
func cliDestinationConfig(flags *genericclioptions.ConfigFlags, contextName, namespace string) (*clientcmdapi.Config, error) {
	pinned := client.SnapshotConfigFlags(flags)
	pinned.Context = &contextName
	cfg, err := pinned.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	return cliDestinationFromREST(cfg, contextName, namespace)
}

// Ordinary operations use the committed browsing actor's resolved REST config,
// while explicit diagnostics may resolve a fresh configuration from flags.
func cliDestinationFromREST(cfg *rest.Config, contextName, namespace string) (*clientcmdapi.Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("external CLI destination is unavailable")
	}
	if cfg.Transport != nil || cfg.WrapTransport != nil || cfg.Dial != nil || len(cfg.NextProtos) > 0 {
		return nil, fmt.Errorf("external CLI cannot preserve custom HTTP transport policy; use a native operation")
	}
	raw := clientcmdapi.NewConfig()
	raw.CurrentContext = contextName
	cluster := &clientcmdapi.Cluster{
		Server: cfg.Host, TLSServerName: cfg.ServerName, InsecureSkipTLSVerify: cfg.Insecure,
		CertificateAuthority: cfg.CAFile, CertificateAuthorityData: slices.Clone(cfg.CAData), DisableCompression: cfg.DisableCompression,
	}
	if cfg.Proxy != nil {
		endpoint, err := url.Parse(cfg.Host)
		if err != nil {
			return nil, err
		}
		proxy, err := cfg.Proxy(&http.Request{URL: endpoint})
		if err != nil {
			return nil, err
		}
		if proxy != nil {
			cluster.ProxyURL = proxy.String()
		}
	}
	user := &clientcmdapi.AuthInfo{
		ClientCertificate: cfg.CertFile, ClientCertificateData: slices.Clone(cfg.CertData),
		ClientKey: cfg.KeyFile, ClientKeyData: slices.Clone(cfg.KeyData),
		Token: cfg.BearerToken, TokenFile: cfg.BearerTokenFile, Username: cfg.Username, Password: cfg.Password,
		Impersonate: cfg.Impersonate.UserName, ImpersonateUID: cfg.Impersonate.UID,
		ImpersonateGroups: slices.Clone(cfg.Impersonate.Groups),
	}
	if cfg.Impersonate.Extra != nil {
		user.ImpersonateUserExtra = make(map[string][]string, len(cfg.Impersonate.Extra))
		for key, values := range cfg.Impersonate.Extra {
			user.ImpersonateUserExtra[key] = slices.Clone(values)
		}
	}
	if cfg.ExecProvider != nil {
		user.Exec = cfg.ExecProvider.DeepCopy()
	}
	if cfg.AuthProvider != nil {
		user.AuthProvider = cfg.AuthProvider.DeepCopy()
	}
	raw.Clusters[contextName], raw.AuthInfos[contextName] = cluster, user
	raw.Contexts[contextName] = &clientcmdapi.Context{Cluster: contextName, AuthInfo: contextName, Namespace: namespace}
	return raw, nil
}

func cliDestinationSnapshot(flags *genericclioptions.ConfigFlags, contextName, namespace string) (path string, cleanup func(), err error) {
	raw, err := cliDestinationConfig(flags, contextName, namespace)
	if err != nil {
		return "", nil, err
	}
	return writeCLIDestination(raw)
}

func writeCLIDestination(raw *clientcmdapi.Config) (path string, cleanup func(), err error) {
	data, err := clientcmd.Write(*raw)
	if err != nil {
		return "", nil, err
	}
	file, err := os.CreateTemp("", "k9plus-cli-destination-*.yaml")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.Remove(file.Name()) }
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return file.Name(), cleanup, nil
}

func guardedKubectl(ctx context.Context, destination *clientcmdapi.Config, args []string, verifySource func() error) error {
	binary, err := exec.LookPath(nativeKubectlCommand)
	if err != nil {
		return fmt.Errorf("kubectl is unavailable: %w", err)
	}
	configPath, cleanup, err := writeCLIDestination(destination)
	if err != nil {
		return err
	}
	defer cleanup()
	remaining := 10 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		remaining = max(time.Nanosecond, time.Until(deadline))
	}
	argv := []string{"--kubeconfig", configPath, "--context", destination.CurrentContext, "--request-timeout", remaining.String()}
	argv = append(argv, args...)
	statuses := make(chan string, 1)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifySource(); err != nil {
		return err
	}
	return execute(&shellOpts{binary: binary, args: argv, background: true, ctx: ctx}, statuses)
}

func (d *Dir) fileOperation(action, source string) {
	session, err := captureOperationScreen(d)
	if err != nil {
		d.App().Flash().Err(err)
		return
	}
	if d.App().Conn() == nil || d.App().Conn().Config() == nil {
		d.App().Flash().Warn("CLI destination is unavailable")
		return
	}
	actor := d.App().Conn().Config()
	namespace := client.CleanseNamespace(d.App().Config.ActiveNamespace())
	d.App().Flash().Info("Reading source identity before confirmation...")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), boundedOperationTimeout(session.timeout))
		defer cancel()
		resolved, err := actor.RESTConfig()
		var destination *clientcmdapi.Config
		if err == nil {
			destination, err = cliDestinationFromREST(resolved, session.context, namespace)
		}
		var fingerprint string
		if err == nil {
			fingerprint, err = fingerprintOperationSource(ctx, source)
		}
		options := []string{"-f"}
		if err == nil && containsDir(source) {
			options = append(options, "-R")
		}
		if err == nil && isKustomized(source) {
			options = []string{"-k"}
		}
		args := append(append([]string{strings.ToLower(action)}, options...), source)
		session.dispatch(func() {
			if err != nil {
				d.App().Flash().Err(err)
				return
			}
			msg := fmt.Sprintf("%s resources from %s?\nContext: %s\nNamespace: %s\nFile identity: %s\nNative kubectl may affect "+
				"many resources and is not atomic. Individual resource UID preconditions are not supplied. Kustomize "+
				"references are resolved by kubectl.\nUse reviewed rendered state for per-resource evidence. "+
				"Cancellation does not undo accepted writes.", action, source, session.context, namespace, fingerprint)
			styles := d.App().Styles.Dialog()
			dialog.ShowConfirm(&styles, d.App().Content.Pages, "Confirm "+action, msg, func() {
				session.timeout = maxOperationDeadline
				target := SelectedResourceTarget{Context: session.context, GVR: d.GVR(), Name: source}
				session.submit(action+" local source", []SelectedResourceTarget{target}, func(ctx context.Context, _ SelectedResourceTarget) error {
					fmt.Fprintf(operationOutput(ctx), "File identity: %s\nExternal kubectl owns individual resource writes; UID preconditions and "+
						"atomicity were not supplied.\n", fingerprint)
					return guardedKubectl(ctx, destination, args, func() error {
						current, err := fingerprintOperationSource(ctx, source)
						if err != nil {
							return err
						}
						if current != fingerprint {
							return fmt.Errorf("operation source changed after confirmation; review again")
						}
						return nil
					})
				}, nil)
			}, func() {})
		})
	}()
}

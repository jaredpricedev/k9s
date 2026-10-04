// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/watch"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

type sessionRefreshResult struct {
	snapshot   connectionHealthSnapshot
	connection client.Connection
}

type sessionRefreshPrepare func(context.Context, connectionHealthRequest, *client.Config, error) sessionRefreshResult

type sessionReconnectState string

const (
	sessionReconnecting       sessionReconnectState = "RECONNECTING · same destination · up to 8s"
	sessionReconnectFailed    sessionReconnectState = "RECONNECT FAILED · existing session retained"
	sessionReconnectDiscarded sessionReconnectState = "DESTINATION CHANGED · reconnect discarded"
	sessionReconnected        sessionReconnectState = "RECONNECTED · same context · workspace retained"
)

func (d *connectionHealthDetails) renderHealth(stale bool) string {
	text := renderConnectionHealthHeadline(d.snapshot, d.previous, time.Now(), stale, string(d.sessionState))
	if d.sessionNotice != "" {
		text += "\n\n" + d.sessionNotice
	}
	return text
}

func (d *connectionHealthDetails) refreshSession() {
	if !d.current(d.generation) {
		d.Update(renderConnectionHealth(d.snapshot, d.previous, time.Now(), true) +
			"\n\nDestination changed. Reopen :connection before reconnecting.")
		return
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.generation++
	generation, request := d.generation, d.request
	ctx, cancel := context.WithTimeout(d.app.sessionContext(), connectionHealthDeadline)
	d.cancel = cancel
	d.stale = true
	d.sessionState = sessionReconnecting
	d.sessionNotice = "Checking this same destination before reconnecting… 8 second deadline."
	d.Update(d.renderHealth(true))
	var cfg *client.Config
	var pinErr error
	if conn := d.app.Conn(); conn != nil {
		cfg, pinErr = conn.Config().PinnedDiagnosticConfig(request.Context)
	} else {
		pinErr = errors.New("no configured connection")
	}
	prepare := d.prepareSession
	if prepare == nil {
		prepare = prepareSessionRefresh
	}
	go func() {
		defer cancel()
		result := boundedSessionRefresh(ctx, request, cfg, pinErr, prepare)
		if errors.Is(ctx.Err(), context.Canceled) || !d.app.IsRunning() {
			closePreparedSession(result.connection)
			return
		}
		d.app.QueueUpdateDraw(func() {
			if !d.current(generation) {
				closePreparedSession(result.connection)
				return
			}
			d.acceptSessionRefresh(result)
		})
	}()
}

func boundedSessionRefresh(ctx context.Context, request connectionHealthRequest, cfg *client.Config, pinErr error, prepare sessionRefreshPrepare) sessionRefreshResult {
	results := make(chan sessionRefreshResult)
	go func() {
		result := prepare(ctx, request, cfg, pinErr)
		select {
		case results <- result:
		case <-ctx.Done():
			closePreparedSession(result.connection)
		}
	}()
	select {
	case result := <-results:
		if ctx.Err() == nil {
			return result
		}
		closePreparedSession(result.connection)
	case <-ctx.Done():
	}
	return sessionRefreshResult{snapshot: connectionHealthFailure(request, classifyConnectionHealthError(ctx.Err()), time.Now())}
}

func prepareSessionRefresh(ctx context.Context, request connectionHealthRequest, cfg *client.Config, pinErr error) sessionRefreshResult {
	fail := func(err error) sessionRefreshResult {
		return sessionRefreshResult{snapshot: connectionHealthFailure(request, classifyConnectionHealthError(err), time.Now())}
	}
	if pinErr != nil {
		return fail(pinErr)
	}
	if cfg == nil {
		return fail(errors.New("no configured connection"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	restCfg, err := cfg.RESTConfig()
	if err != nil {
		return fail(err)
	}
	restCfg = rest.CopyConfig(restCfg)
	browsingTimeout := restCfg.Timeout
	restCfg.Timeout = connectionHealthDeadline
	if restCfg.ExecProvider != nil {
		restCfg.ExecProvider = restCfg.ExecProvider.DeepCopy()
		restCfg.ExecProvider.InteractiveMode = "Never"
	}
	// Pin both namespace and transport before any handle becomes visible. Later
	// kubeconfig edits cannot silently redirect this prepared browsing session.
	cfg.Flags().Namespace = &request.Namespace
	cfg.PrepareSessionREST(restCfg)
	conn, err := client.NewSessionConnection(cfg)
	if err != nil {
		return fail(err)
	}
	typed, err := conn.Dial()
	if err != nil {
		conn.CloseSession()
		return fail(err)
	}
	snapshot := collectConnectionHealthClients(ctx, request, typed)
	if connectionHealthComplete(snapshot) {
		check := connectionHealthCheck{Name: "API discovery", Source: "GET /api and /apis", ObservedAt: time.Now()}
		var core metav1.APIVersions
		var groups metav1.APIGroupList
		data, checkErr := typed.Discovery().RESTClient().Get().AbsPath("/api").Do(ctx).Raw()
		if checkErr == nil {
			checkErr = json.Unmarshal(data, &core)
			if checkErr == nil && len(core.Versions) == 0 {
				checkErr = errors.New("no discovery client versions")
			}
		}
		if checkErr == nil {
			data, checkErr = typed.Discovery().RESTClient().Get().AbsPath("/apis").Do(ctx).Raw()
			if checkErr == nil {
				checkErr = json.Unmarshal(data, &groups)
				if checkErr == nil && groups.Kind != "APIGroupList" {
					checkErr = errors.New("no discovery client group list")
				}
			}
		}
		check.State = classifyConnectionHealthError(checkErr)
		snapshot.Checks = append(snapshot.Checks, check)
	}
	if !connectionHealthComplete(snapshot) || ctx.Err() != nil {
		conn.CloseSession()
		return sessionRefreshResult{snapshot: snapshot}
	}
	// The setup deadline does not shorten long-lived browsing/watch transports.
	// Construct the committed handles from the same captured actor, preserving
	// its configured request timeout. Construction performs no additional reads.
	restCfg.Timeout = browsingTimeout
	cfg.PrepareSessionREST(restCfg)
	fresh, err := client.NewSessionConnection(cfg)
	conn.CloseSession()
	if err != nil {
		return fail(err)
	}
	return sessionRefreshResult{snapshot: snapshot, connection: fresh}
}

func closePreparedSession(conn client.Connection) {
	if closer, ok := conn.(interface{ CloseSession() }); ok {
		closer.CloseSession()
	}
}

// The dispatcher calls this only while the captured page and destination are
// still current. Retained facts keep their original observation identity/time.
//
//nolint:gocritic // Keep staged results immutable across worker and dispatcher ownership.
func (d *connectionHealthDetails) acceptSessionRefresh(result sessionRefreshResult) {
	d.cancel = nil
	d.acceptSnapshot(result.snapshot)
	if result.connection == nil {
		d.sessionState = sessionReconnectFailed
		d.sessionNotice = "Reconnect failed. Existing session and retained workspace kept; running watches may still be disconnected."
	} else if err := d.app.replaceSession(d.request, result.connection); err != nil {
		d.sessionState = sessionReconnectDiscarded
		closePreparedSession(result.connection)
		d.sessionNotice = "Destination changed. Reconnect was discarded; existing workspace kept."
	} else {
		d.sessionState = sessionReconnected
		d.request.Revision = d.app.Config.DestinationRevision()
		d.sessionNotice = "Session reconnected for this same context. Workspace and navigation retained; " +
			"reopen stopped log streams and port-forwards after checking resource identity."
	}
	d.stale = false
	d.Update(d.renderHealth(false))
}

func (a *App) replaceSession(request connectionHealthRequest, conn client.Connection) error {
	oldFactory, oldConnection := a.factory, a.Conn()
	pages := append([]model.Component(nil), a.Content.Peek()...)
	selections := make(map[*Browser]SelectedResourceTarget)
	for _, page := range pages {
		if viewer, ok := page.(TableViewer); ok {
			if table := viewer.GetTable(); table != nil && table.browser != nil {
				selections[table.browser] = table.browser.SelectedResource()
			}
		}
	}
	a.Halt()
	defer a.Resume()
	a.sessionMu.Lock()
	if err := a.Config.ReplaceSessionConnection(conn, request.Context, request.Namespace, request.Revision); err != nil {
		a.sessionMu.Unlock()
		return err
	}
	a.factory = watch.NewFactory(conn)
	dao.MetaAccess.InvalidateRequests()
	if a.command != nil {
		previous := a.command
		a.command = NewCommand(a)
		a.command.alias = dao.NewAlias(a.factory)
		if previous.alias != nil {
			for gvr, names := range previous.alias.ShortNames() {
				a.command.alias.Define(gvr, names...)
			}
		}
	}
	a.sessionMu.Unlock()
	for _, page := range pages {
		page.Stop()
	}
	a.Content.ClearPageResources()
	a.factory.Start(request.Namespace)
	client.ResetMetrics()
	atomic.StoreInt32(&a.conRetry, 0)
	for _, page := range pages {
		if viewer, ok := page.(TableViewer); ok {
			if table := viewer.GetTable(); table != nil && table.browser != nil {
				table.browser.rebindSession(selections[table.browser])
			}
		}
		if pulse, ok := page.(*Pulse); ok {
			pulse.rebindSession(request.Namespace)
		}
		if logs, ok := page.(*Log); ok {
			logs.sessionStale = true
		}
		if tree, ok := page.(*Xray); ok {
			tree.rebindSession()
		}
	}
	if a.clusterModel != nil {
		a.clusterModel.RebindFactory(a.factory)
	}
	if oldFactory != nil {
		oldFactory.Terminate()
	}
	closePreparedSession(oldConnection)
	a.statusIndicator().RefreshIdentity()
	if top := a.Content.Top(); top != nil {
		top.Start()
	}
	return nil
}

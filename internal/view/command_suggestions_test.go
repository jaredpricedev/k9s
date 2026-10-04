// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

func suggestionHarness(t *testing.T) (*commandSuggestions, *model.FishBuff, chan func()) {
	t.Helper()
	queue := make(chan func(), 8)
	buff := model.NewFishBuff(':', model.CommandBuffer)
	d := &commandSuggestions{dispatch: func(f func()) { queue <- f }, refresh: func() {
		if buff.IsActive() {
			buff.Notify(false)
		}
	}}
	buff.SetSuggestionFn(func(text string) sort.StringSlice {
		d.discover()
		data := d.catalog()
		return cmd.SuggestSubCommand(text, data.namespaces, data.contexts)
	})
	buff.AddListener(d)
	t.Cleanup(d.stop)
	return d, buff, queue
}

func queuedSuggestion(t *testing.T, queue <-chan func()) func() {
	t.Helper()
	select {
	case callback := <-queue:
		return callback
	case <-time.After(time.Second):
		t.Fatal("discovery did not reach UI dispatcher")
	}
	return nil
}

func TestCommandSuggestionsTypingDoesNotWaitForDiscovery(t *testing.T) {
	d, buff, queue := suggestionHarness(t)
	started, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	d.reset(context.Background(), commandSuggestionScope{context: "captured", namespace: "default"}, func(ctx context.Context) (commandSuggestionData, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return commandSuggestionData{}, ctx.Err()
		}
		return commandSuggestionData{namespaces: client.NamespaceNames{"production": {}}}, nil
	})
	buff.SetActive(true)
	<-started
	finished := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		for _, r := range "pods production" {
			buff.Add(r)
		}
		buff.Delete()
		finished <- time.Since(start)
	}()
	select {
	case elapsed := <-finished:
		t.Logf("14 additions and one deletion while discovery is blocked: %s", elapsed)
		assert.Less(t, elapsed, 100*time.Millisecond)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("command edits waited for namespace discovery")
	}
	assert.Empty(t, queue, "worker cannot mutate buffer before UI dispatch")
}

func TestCommandSuggestionsReplyUsesCurrentQueryOnUIDispatcher(t *testing.T) {
	d, buff, queue := suggestionHarness(t)
	release := make(chan struct{})
	d.reset(context.Background(), commandSuggestionScope{context: "captured", namespace: "default"}, func(context.Context) (commandSuggestionData, error) {
		<-release
		return commandSuggestionData{namespaces: client.NamespaceNames{"development": {}, "production": {}}}, nil
	})
	buff.SetText("pods dev", "", true)
	buff.SetActive(true)
	buff.SetText("pods prod", "", true)
	close(release)
	callback := queuedSuggestion(t, queue)
	assert.Empty(t, buff.GetSuggestion(), "worker must not change widgets outside UI dispatcher")
	callback()
	assert.Equal(t, "pods prod", buff.GetText())
	assert.Equal(t, "uction", buff.GetSuggestion(), "old query suffix must never replace the new query")
}

func TestCommandSuggestionsQueuedOldSessionReplyIsDiscarded(t *testing.T) {
	d, buff, queue := suggestionHarness(t)
	old := commandSuggestionScope{context: "old", namespace: "default"}
	d.reset(context.Background(), old, func(context.Context) (commandSuggestionData, error) {
		return commandSuggestionData{namespaces: client.NamespaceNames{"development": {}}}, nil
	})
	buff.SetText("pods d", "", true)
	buff.SetActive(true)
	oldReply := queuedSuggestion(t, queue)
	d.reset(context.Background(), commandSuggestionScope{context: "new", namespace: "default"}, func(context.Context) (commandSuggestionData, error) {
		return commandSuggestionData{namespaces: client.NamespaceNames{"destination": {}}}, nil
	})
	d.discover()
	queuedSuggestion(t, queue)()
	assert.Equal(t, "estination", buff.GetSuggestion())
	oldReply()
	assert.Equal(t, "estination", buff.GetSuggestion())
	assert.Contains(t, d.catalog().namespaces, "destination")
	assert.NotContains(t, d.catalog().namespaces, "development")
}

func TestCommandSuggestionsEscCancelsAndNextPromptCanRetry(t *testing.T) {
	d, buff, queue := suggestionHarness(t)
	started, canceled := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	d.reset(context.Background(), commandSuggestionScope{context: "ctx", namespace: "known"}, func(ctx context.Context) (commandSuggestionData, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			close(canceled)
			return commandSuggestionData{}, ctx.Err()
		}
		return commandSuggestionData{namespaces: client.NamespaceNames{"next": {}}}, nil
	})
	buff.SetActive(true)
	<-started
	buff.Reset() // Prompt's Esc path.
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Esc did not cancel discovery")
	}
	buff.SetActive(true)
	queuedSuggestion(t, queue)()
	assert.Contains(t, d.catalog().namespaces, "next")
	assert.EqualValues(t, 2, calls.Load())
}

func TestCommandSuggestionsDenialRetainsKnownNamespaceAndThrottlesRetries(t *testing.T) {
	d, buff, queue := suggestionHarness(t)
	var calls atomic.Int32
	d.reset(context.Background(), commandSuggestionScope{context: "ctx", namespace: "known-namespace"}, func(context.Context) (commandSuggestionData, error) {
		calls.Add(1)
		return commandSuggestionData{contexts: []string{"other-context"}}, errors.New("forbidden")
	})
	buff.SetText("pods known", "", true)
	buff.SetActive(true)
	queuedSuggestion(t, queue)()
	assert.Equal(t, "-namespace", buff.GetSuggestion())
	assert.Equal(t, []string{"other-context"}, d.catalog().contexts)
	for range 20 {
		buff.Notify(false)
	}
	assert.EqualValues(t, 1, calls.Load(), "denials must not produce a request on every edit")
	// Explicitly typed namespaces do not depend on enumeration.
	buff.SetText("pods private-known-namespace", "", true)
	assert.Equal(t, "private-known-namespace", mustNS(t, buff.GetText()))
}

func mustNS(t *testing.T, text string) string {
	t.Helper()
	ns, ok := cmd.NewInterpreter(text).NSArg()
	require.True(t, ok)
	return ns
}

func TestCommandSuggestionsStopCancelsBoundedWorker(t *testing.T) {
	d, buff, _ := suggestionHarness(t)
	deadline := make(chan time.Duration, 1)
	canceled := make(chan struct{})
	d.reset(context.Background(), commandSuggestionScope{context: "ctx", namespace: "known"}, func(ctx context.Context) (commandSuggestionData, error) {
		end, ok := ctx.Deadline()
		if !ok {
			deadline <- 0
		} else {
			deadline <- time.Until(end)
		}
		<-ctx.Done()
		close(canceled)
		return commandSuggestionData{}, ctx.Err()
	})
	buff.SetActive(true)
	assert.InDelta(t, float64(commandDiscoveryTimeout), float64(<-deadline), float64(time.Second))
	d.stop()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel discovery")
	}
}

func TestCommandSuggestionAliasesDoNotWaitForCatalogRefreshLock(t *testing.T) {
	c := &Command{}
	aliases := []string{"pods", "services"}
	c.suggestionCatalog.Store(&aliases)
	c.mx.Lock()
	defer c.mx.Unlock()
	assert.Equal(t, aliases, c.suggestionAliases())
}

func TestCommandSuggestionsSuccessCacheRefreshesOnlyAfterExpiry(t *testing.T) {
	d, buff, queue := suggestionHarness(t)
	var calls atomic.Int32
	d.reset(context.Background(), commandSuggestionScope{context: "ctx", namespace: "known"}, func(context.Context) (commandSuggestionData, error) {
		calls.Add(1)
		return commandSuggestionData{namespaces: client.NamespaceNames{"cached": {}}}, nil
	})
	buff.SetActive(true)
	queuedSuggestion(t, queue)()
	for range 20 {
		buff.Notify(false)
	}
	assert.EqualValues(t, 1, calls.Load())
	d.mx.Lock()
	d.expires = time.Now().Add(-time.Second)
	d.mx.Unlock()
	buff.Notify(false)
	queuedSuggestion(t, queue)()
	assert.EqualValues(t, 2, calls.Load())
}

func TestCommandSuggestionsLocalHistoryAndAliasesWorkWithoutConnection(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	app.command = &Command{}
	aliases := []string{"pods", "services"}
	app.command.suggestionCatalog.Store(&aliases)
	app.cmdHistory.Push("pods known-namespace")
	fn := app.suggestCommand()
	app.resetCommandSuggestions(context.Background())
	defer app.stopCommandSuggestions()
	assert.Equal(t, sort.StringSlice{"ods", "ods known-namespace"}, fn("p"))
	assert.Equal(t, sort.StringSlice{"pods known-namespace"}, fn(""))
}

func TestCommandCatalogLoaderUsesCapturedDestinationAndCancelsHTTP(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "captured destination", true: "canceled request"}[cancelRequest], func(t *testing.T) {
			requested := make(chan struct{}, 1)
			canceled := make(chan struct{}, 1)
			var wrongDestination atomic.Int32
			original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/namespaces" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				requested <- struct{}{}
				if cancelRequest {
					<-r.Context().Done()
					canceled <- struct{}{}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"kind":"NamespaceList","apiVersion":"v1","items":[{"metadata":{"name":"original-ns"}}]}`))
			}))
			defer original.Close()
			replacement := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { wrongDestination.Add(1) }))
			defer replacement.Close()
			flags := catalogConfigFlags(t, original.URL, replacement.URL)
			captured := client.NewConfig(flags).Snapshot("original")
			*flags.Context = "replacement"
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result := make(chan commandSuggestionData, 1)
			errResult := make(chan error, 1)
			go func() { data, err := commandCatalogLoader(captured)(ctx); result <- data; errResult <- err }()
			select {
			case <-requested:
			case <-time.After(time.Second):
				t.Fatal("captured cluster was not requested")
			}
			if cancelRequest {
				cancel()
				select {
				case <-canceled:
				case <-time.After(time.Second):
					t.Fatal("request did not cancel on its deadline/context")
				}
			}
			data, err := <-result, <-errResult
			assert.ElementsMatch(t, []string{"original", "replacement"}, data.contexts)
			if cancelRequest {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Contains(t, data.namespaces, "original-ns")
			}
			assert.Zero(t, wrongDestination.Load())
		})
	}
}

func catalogConfigFlags(t *testing.T, original, replacement string) *genericclioptions.ConfigFlags {
	t.Helper()
	cfg := api.NewConfig()
	cfg.CurrentContext = "original"
	cfg.Clusters["original"] = &api.Cluster{Server: original}
	cfg.Clusters["replacement"] = &api.Cluster{Server: replacement}
	cfg.AuthInfos["user"] = &api.AuthInfo{}
	cfg.Contexts["original"] = &api.Context{Cluster: "original", AuthInfo: "user"}
	cfg.Contexts["replacement"] = &api.Context{Cluster: "replacement", AuthInfo: "user"}
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, clientcmd.WriteToFile(*cfg, path))
	flags := genericclioptions.NewConfigFlags(false)
	flags.KubeConfig = &path
	*flags.Context = "original"
	return flags
}

func TestCommandDiscoverySimulationRemainsResponsiveToTypingResizeEscAndQuit(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	buff := app.CmdBuff()
	discovery := &commandSuggestions{dispatch: app.QueueUpdateDraw, refresh: func() {
		if buff.IsActive() {
			buff.Notify(false)
		}
	}}
	buff.AddListener(discovery)
	buff.SetSuggestionFn(func(text string) sort.StringSlice {
		discovery.discover()
		data := discovery.catalog()
		return cmd.SuggestSubCommand(text, data.namespaces, data.contexts)
	})
	app.Prompt().SetModel(buff)
	started, canceled := make(chan struct{}), make(chan struct{})
	discovery.reset(context.Background(), commandSuggestionScope{context: "ctx", namespace: "known"}, func(ctx context.Context) (commandSuggestionData, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return commandSuggestionData{}, ctx.Err()
	})
	defer discovery.stop()
	buff.SetActive(true)
	<-started
	screen := tcell.NewSimulationScreen("UTF-8")
	require.NoError(t, screen.Init())
	screen.SetSize(80, 24)
	app.SetScreen(screen).SetRoot(app.Prompt(), true).SetFocus(app.Prompt())
	observed := make(chan string, 1)
	resized := make(chan int, 1)
	var expectResize atomic.Bool
	app.SetAfterDrawFunc(func(tcell.Screen) {
		if expectResize.Load() {
			_, _, width, _ := app.Prompt().GetRect()
			if width == 60 {
				select {
				case resized <- width:
				default:
				}
			}
		}
	})
	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyF12 {
			observed <- buff.GetText()
			return nil
		}
		if event.Key() == tcell.KeyCtrlC {
			discovery.stop()
			app.Stop()
			return nil
		}
		return event
	})
	finished := make(chan error, 1)
	go func() { finished <- app.Application.Run() }()
	t.Cleanup(app.Stop)
	for _, r := range "pods k" {
		app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	app.QueueEvent(tcell.NewEventKey(tcell.KeyF12, 0, tcell.ModNone))
	select {
	case text := <-observed:
		assert.Equal(t, "pods k", text)
	case <-time.After(time.Second):
		t.Fatal("UI blocked while typing")
	}
	// Resize and inspect the resulting frame while the namespace API is blocked.
	expectResize.Store(true)
	screen.SetSize(60, 24)
	app.QueueEvent(tcell.NewEventResize(60, 24))
	select {
	case width := <-resized:
		assert.Equal(t, 60, width)
	case <-time.After(time.Second):
		t.Fatal("UI blocked while resizing")
	}
	app.QueueEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("UI blocked while canceling prompt")
	}
	assert.False(t, buff.IsActive())
	app.QueueEvent(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone))
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("UI blocked while quitting")
	}
}

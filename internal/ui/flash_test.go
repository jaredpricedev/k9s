// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package ui_test

import (
	"context"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/ui"
	"github.com/stretchr/testify/assert"
)

func TestFlash(t *testing.T) {
	uu := map[string]struct {
		l    model.FlashLevel
		i, e string
	}{
		"info": {l: model.FlashInfo, i: "hello", e: "😎 hello\n"},
		"warn": {l: model.FlashWarn, i: "hello", e: "😗 hello\n"},
		"err":  {l: model.FlashErr, i: "hello", e: "😡 hello\n"},
	}

	for k := range uu {
		u := uu[k]
		t.Run(k, func(t *testing.T) {
			a := ui.NewApp(mock.NewMockConfig(t), "test")
			f := ui.NewFlash(a)
			f.SetTestMode(true)
			updated := make(chan struct{}, 1)
			f.SetChangedFunc(func() { updated <- struct{}{} })
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				f.Watch(ctx, a.Flash().Channel())
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("flash watcher did not stop")
				}
			})

			a.Flash().SetMessage(u.l, u.i)
			// TextView signals changes after finishing its buffer write.
			select {
			case <-updated:
			case <-time.After(time.Second):
				t.Fatal("flash message was not rendered")
			}
			assert.Equal(t, u.e, f.GetText(false))
		})
	}
}

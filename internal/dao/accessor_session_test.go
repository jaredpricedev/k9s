// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package dao

import (
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/render"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

const sessionActorPath = "team/app"

type sessionAccessorFactory struct {
	Factory
	uid string
}

func (f sessionAccessorFactory) Get(*client.GVR, string, bool, labels.Selector) (runtime.Object, error) {
	object := new(unstructured.Unstructured)
	object.SetUID(types.UID(f.uid))
	return object, nil
}

func TestSessionAccessorsCannotRetargetAnotherFactory(t *testing.T) {
	first, err := AccessorFor(sessionAccessorFactory{uid: "old-actor"}, client.PodGVR)
	require.NoError(t, err)
	fresh, err := AccessorFor(sessionAccessorFactory{uid: "fresh-actor"}, client.PodGVR)
	require.NoError(t, err)
	first.Init(sessionAccessorFactory{uid: "late-old-actor"}, client.PodGVR)
	obj, err := fresh.Get(t.Context(), sessionActorPath)
	require.NoError(t, err)
	require.Equal(t, "fresh-actor", string(obj.(*render.PodWithMetrics).Raw.GetUID()))
	obj, err = first.Get(t.Context(), sessionActorPath)
	require.NoError(t, err)
	require.Equal(t, "late-old-actor", string(obj.(*render.PodWithMetrics).Raw.GetUID()))
}

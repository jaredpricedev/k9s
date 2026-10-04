// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"fmt"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/storage"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func (v *storageView) submitExpansion(plan *storage.ExpansionPlan) {
	if !v.active || !v.destinationCurrent() || v.app.Content.Top() != v || v.connection == nil {
		v.app.Flash().Warn("Storage owner/destination unavailable; reopen and preview expansion")
		return
	}
	generation, snapshot := v.generation, v.snapshot
	session := &operationSession{app: v.app, context: plan.PVC.Context, namespace: plan.PVC.Namespace,
		revision: v.destinationRevision, timeout: 10 * time.Second}
	var err error
	session.dynamic, err = v.connection.DynDial()
	if err != nil {
		v.app.Flash().Err(err)
		return
	}
	session.typed, err = v.connection.Dial()
	if err != nil {
		v.app.Flash().Err(err)
		return
	}
	session.stillCurrent = func() bool {
		return v.active && v.generation == generation && v.snapshot == snapshot && v.app.Content.Top() == v
	}
	target := SelectedResourceTarget{Context: plan.PVC.Context, GVR: client.PvcGVR,
		Namespace: plan.PVC.Namespace, Name: plan.PVC.Name, UID: types.UID(plan.PVC.UID)}
	session.submit("PVC expansion request", []SelectedResourceTarget{target},
		func(ctx context.Context, target SelectedResourceTarget) error {
			return session.expandPVC(ctx, &target, plan)
		}, nil)
}

func (s *operationSession) expandPVC(ctx context.Context, target *SelectedResourceTarget, plan *storage.ExpansionPlan) error {
	if err := checkOperationTarget(target); err != nil {
		return err
	}
	if target.GVR.String() != client.PvcGVR.String() || target.Context != plan.PVC.Context ||
		target.Namespace != plan.PVC.Namespace || target.Name != plan.PVC.Name || string(target.UID) != plan.PVC.UID {
		return fmt.Errorf("Expansion target differs from the reviewed PVC identity")
	}
	if err := s.authorize(ctx, target, "", client.GetVerb, client.PatchVerb); err != nil {
		return err
	}
	pvc, err := s.readTarget(ctx, target)
	if err != nil {
		return err
	}
	class, err := s.dynamic.Resource(client.ScGVR.GVR()).Get(ctx, plan.Class.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("Recheck StorageClass: %w", err)
	}
	pv, err := s.dynamic.Resource(client.PvGVR.GVR()).Get(ctx, plan.PV.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("Recheck PV binding: %w", err)
	}
	if checkErr := plan.Recheck(pvc, pv, class); checkErr != nil {
		return checkErr
	}
	patch, err := plan.Patch()
	if err != nil {
		return err
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	operationBeginWrite(ctx)
	_, err = s.resource(target).Patch(ctx, target.Name, types.JSONPatchType, patch,
		metav1.PatchOptions{FieldManager: "k9plus-storage"})
	if err == nil {
		operationAcceptWrite(ctx, fmt.Sprintf("PVC requested storage %s -> %s; controller/filesystem progress unconfirmed",
			plan.Previous.String(), plan.Requested.String()))
	}
	return err
}

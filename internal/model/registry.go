// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package model

import (
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/render"
	"github.com/derailed/k9s/internal/render/helm"
	"github.com/derailed/k9s/internal/xray"
)

// Registry tracks resources metadata.
// BOZO!! Break up deps and merge into single registrar.
var Registry = map[*client.GVR]ResourceMeta{
	client.FluxGVR: {
		DAO:         new(dao.FluxDashboard),
		NewDAO:      func() dao.Accessor { return new(dao.FluxDashboard) },
		Renderer:    &render.Flux{Unified: true},
		NewRenderer: func() model1.Renderer { return &render.Flux{Unified: true} },
	},
	// Custom...
	client.WkGVR: {
		DAO:         new(dao.Workload),
		NewDAO:      func() dao.Accessor { return new(dao.Workload) },
		Renderer:    new(render.Workload),
		NewRenderer: func() model1.Renderer { return new(render.Workload) },
	},
	client.RefGVR: {
		DAO:         new(dao.Reference),
		NewDAO:      func() dao.Accessor { return new(dao.Reference) },
		Renderer:    new(render.Reference),
		NewRenderer: func() model1.Renderer { return new(render.Reference) },
	},
	client.DirGVR: {
		DAO:         new(dao.Dir),
		NewDAO:      func() dao.Accessor { return new(dao.Dir) },
		Renderer:    new(render.Dir),
		NewRenderer: func() model1.Renderer { return new(render.Dir) },
	},
	client.PuGVR: {
		DAO:    new(dao.Pulse),
		NewDAO: func() dao.Accessor { return new(dao.Pulse) },
	},
	client.HmGVR: {
		DAO:         new(dao.HelmChart),
		NewDAO:      func() dao.Accessor { return new(dao.HelmChart) },
		Renderer:    new(helm.Chart),
		NewRenderer: func() model1.Renderer { return new(helm.Chart) },
	},
	client.HmhGVR: {
		DAO:         new(dao.HelmHistory),
		NewDAO:      func() dao.Accessor { return new(dao.HelmHistory) },
		Renderer:    new(helm.History),
		NewRenderer: func() model1.Renderer { return new(helm.History) },
	},
	client.CoGVR: {
		DAO:             new(dao.Container),
		NewDAO:          func() dao.Accessor { return new(dao.Container) },
		Renderer:        new(render.Container),
		NewRenderer:     func() model1.Renderer { return new(render.Container) },
		TreeRenderer:    new(xray.Container),
		NewTreeRenderer: func() TreeRenderer { return new(xray.Container) },
	},
	client.ScnGVR: {
		DAO:         new(dao.ImageScan),
		NewDAO:      func() dao.Accessor { return new(dao.ImageScan) },
		Renderer:    new(render.ImageScan),
		NewRenderer: func() model1.Renderer { return new(render.ImageScan) },
	},
	client.CtGVR: {
		DAO:         new(dao.Context),
		NewDAO:      func() dao.Accessor { return new(dao.Context) },
		Renderer:    new(render.Context),
		NewRenderer: func() model1.Renderer { return new(render.Context) },
	},
	client.SdGVR: {
		DAO:         new(dao.ScreenDump),
		NewDAO:      func() dao.Accessor { return new(dao.ScreenDump) },
		Renderer:    new(render.ScreenDump),
		NewRenderer: func() model1.Renderer { return new(render.ScreenDump) },
	},
	client.RbacGVR: {
		DAO:         new(dao.Rbac),
		NewDAO:      func() dao.Accessor { return new(dao.Rbac) },
		Renderer:    new(render.Rbac),
		NewRenderer: func() model1.Renderer { return new(render.Rbac) },
	},
	client.PolGVR: {
		DAO:         new(dao.Policy),
		NewDAO:      func() dao.Accessor { return new(dao.Policy) },
		Renderer:    new(render.Policy),
		NewRenderer: func() model1.Renderer { return new(render.Policy) },
	},
	client.UsrGVR: {
		DAO:         new(dao.Subject),
		NewDAO:      func() dao.Accessor { return new(dao.Subject) },
		Renderer:    new(render.Subject),
		NewRenderer: func() model1.Renderer { return new(render.Subject) },
	},
	client.GrpGVR: {
		DAO:         new(dao.Subject),
		NewDAO:      func() dao.Accessor { return new(dao.Subject) },
		Renderer:    new(render.Subject),
		NewRenderer: func() model1.Renderer { return new(render.Subject) },
	},
	client.PfGVR: {
		DAO:         new(dao.PortForward),
		NewDAO:      func() dao.Accessor { return new(dao.PortForward) },
		Renderer:    new(render.PortForward),
		NewRenderer: func() model1.Renderer { return new(render.PortForward) },
	},
	client.BeGVR: {
		DAO:         new(dao.Benchmark),
		NewDAO:      func() dao.Accessor { return new(dao.Benchmark) },
		Renderer:    new(render.Benchmark),
		NewRenderer: func() model1.Renderer { return new(render.Benchmark) },
	},
	client.AliGVR: {
		DAO:         new(dao.Alias),
		NewDAO:      func() dao.Accessor { return new(dao.Alias) },
		Renderer:    new(render.Alias),
		NewRenderer: func() model1.Renderer { return new(render.Alias) },
	},

	// Discovery...
	client.EpsGVR: {
		Renderer:    new(render.EndpointSlice),
		NewRenderer: func() model1.Renderer { return new(render.EndpointSlice) },
	},

	// Core...
	client.EpGVR: {
		Renderer:    new(render.Endpoints),
		NewRenderer: func() model1.Renderer { return new(render.Endpoints) },
	},
	client.PodGVR: {
		DAO:             new(dao.Pod),
		NewDAO:          func() dao.Accessor { return new(dao.Pod) },
		Renderer:        render.NewPod(),
		NewRenderer:     func() model1.Renderer { return render.NewPod() },
		TreeRenderer:    new(xray.Pod),
		NewTreeRenderer: func() TreeRenderer { return new(xray.Pod) },
	},
	client.NsGVR: {
		DAO:         new(dao.Namespace),
		NewDAO:      func() dao.Accessor { return new(dao.Namespace) },
		Renderer:    new(render.Namespace),
		NewRenderer: func() model1.Renderer { return new(render.Namespace) },
	},
	client.SecGVR: {
		DAO:         new(dao.Secret),
		NewDAO:      func() dao.Accessor { return new(dao.Secret) },
		Renderer:    new(render.Secret),
		NewRenderer: func() model1.Renderer { return new(render.Secret) },
	},
	client.CmGVR: {
		DAO:         new(dao.ConfigMap),
		NewDAO:      func() dao.Accessor { return new(dao.ConfigMap) },
		Renderer:    new(render.ConfigMap),
		NewRenderer: func() model1.Renderer { return new(render.ConfigMap) },
	},
	client.NodeGVR: {
		DAO:         new(dao.Node),
		NewDAO:      func() dao.Accessor { return new(dao.Node) },
		Renderer:    new(render.Node),
		NewRenderer: func() model1.Renderer { return new(render.Node) },
	},
	client.SvcGVR: {
		DAO:             new(dao.Service),
		NewDAO:          func() dao.Accessor { return new(dao.Service) },
		Renderer:        new(render.Service),
		NewRenderer:     func() model1.Renderer { return new(render.Service) },
		TreeRenderer:    new(xray.Service),
		NewTreeRenderer: func() TreeRenderer { return new(xray.Service) },
	},
	client.SaGVR: {
		Renderer:    new(render.ServiceAccount),
		NewRenderer: func() model1.Renderer { return new(render.ServiceAccount) },
	},
	client.PvGVR: {
		Renderer:    new(render.PersistentVolume),
		NewRenderer: func() model1.Renderer { return new(render.PersistentVolume) },
	},
	client.PvcGVR: {
		Renderer:    new(render.PersistentVolumeClaim),
		NewRenderer: func() model1.Renderer { return new(render.PersistentVolumeClaim) },
	},
	client.EvGVR: {
		DAO:         new(dao.Table),
		NewDAO:      func() dao.Accessor { return new(dao.Table) },
		Renderer:    new(render.Event),
		NewRenderer: func() model1.Renderer { return new(render.Event) },
	},

	// Apps...
	client.DpGVR: {
		DAO:             new(dao.Deployment),
		NewDAO:          func() dao.Accessor { return new(dao.Deployment) },
		Renderer:        new(render.Deployment),
		NewRenderer:     func() model1.Renderer { return new(render.Deployment) },
		TreeRenderer:    new(xray.Deployment),
		NewTreeRenderer: func() TreeRenderer { return new(xray.Deployment) },
	},
	client.RsGVR: {
		Renderer:        new(render.ReplicaSet),
		NewRenderer:     func() model1.Renderer { return new(render.ReplicaSet) },
		TreeRenderer:    new(xray.ReplicaSet),
		NewTreeRenderer: func() TreeRenderer { return new(xray.ReplicaSet) },
	},
	client.StsGVR: {
		DAO:             new(dao.StatefulSet),
		NewDAO:          func() dao.Accessor { return new(dao.StatefulSet) },
		Renderer:        new(render.StatefulSet),
		NewRenderer:     func() model1.Renderer { return new(render.StatefulSet) },
		TreeRenderer:    new(xray.StatefulSet),
		NewTreeRenderer: func() TreeRenderer { return new(xray.StatefulSet) },
	},
	client.DsGVR: {
		DAO:             new(dao.DaemonSet),
		NewDAO:          func() dao.Accessor { return new(dao.DaemonSet) },
		Renderer:        new(render.DaemonSet),
		NewRenderer:     func() model1.Renderer { return new(render.DaemonSet) },
		TreeRenderer:    new(xray.DaemonSet),
		NewTreeRenderer: func() TreeRenderer { return new(xray.DaemonSet) },
	},

	// Extensions...
	client.NpGVR: {
		Renderer:    &render.NetworkPolicy{},
		NewRenderer: func() model1.Renderer { return &render.NetworkPolicy{} },
	},

	// Batch...
	client.CjGVR: {
		DAO:         new(dao.CronJob),
		NewDAO:      func() dao.Accessor { return new(dao.CronJob) },
		Renderer:    new(render.CronJob),
		NewRenderer: func() model1.Renderer { return new(render.CronJob) },
	},
	client.JobGVR: {
		DAO:         new(dao.Job),
		NewDAO:      func() dao.Accessor { return new(dao.Job) },
		Renderer:    new(render.Job),
		NewRenderer: func() model1.Renderer { return new(render.Job) },
	},

	// CRDs...
	client.CrdGVR: {
		DAO:         new(dao.CustomResourceDefinition),
		NewDAO:      func() dao.Accessor { return new(dao.CustomResourceDefinition) },
		Renderer:    new(render.CustomResourceDefinition),
		NewRenderer: func() model1.Renderer { return new(render.CustomResourceDefinition) },
	},

	// Storage...
	client.ScGVR: {
		Renderer:    &render.StorageClass{},
		NewRenderer: func() model1.Renderer { return &render.StorageClass{} },
	},

	// Policy...
	client.PdbGVR: {
		Renderer:    &render.PodDisruptionBudget{},
		NewRenderer: func() model1.Renderer { return &render.PodDisruptionBudget{} },
	},

	// RBAC...
	client.CrGVR: {
		DAO:         new(dao.Rbac),
		NewDAO:      func() dao.Accessor { return new(dao.Rbac) },
		Renderer:    new(render.ClusterRole),
		NewRenderer: func() model1.Renderer { return new(render.ClusterRole) },
	},
	client.CrbGVR: {
		Renderer:    new(render.ClusterRoleBinding),
		NewRenderer: func() model1.Renderer { return new(render.ClusterRoleBinding) },
	},
	client.RoGVR: {
		Renderer:    new(render.Role),
		NewRenderer: func() model1.Renderer { return new(render.Role) },
	},
	client.RobGVR: {
		Renderer:    new(render.RoleBinding),
		NewRenderer: func() model1.Renderer { return new(render.RoleBinding) },
	},
}

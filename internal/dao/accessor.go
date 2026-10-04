// Modified for k9+; see NOTICE.
package dao

import (
	"log/slog"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/slogs"
)

var accessors = map[*client.GVR]func() Accessor{
	client.FluxGVR: func() Accessor { return new(FluxDashboard) },
	client.WkGVR:   func() Accessor { return new(Workload) },
	client.CtGVR:   func() Accessor { return new(Context) },
	client.CoGVR:   func() Accessor { return new(Container) },
	client.ScnGVR:  func() Accessor { return new(ImageScan) },
	client.SdGVR:   func() Accessor { return new(ScreenDump) },
	client.BeGVR:   func() Accessor { return new(Benchmark) },
	client.PfGVR:   func() Accessor { return new(PortForward) },
	client.DirGVR:  func() Accessor { return new(Dir) },

	client.SvcGVR:  func() Accessor { return new(Service) },
	client.PodGVR:  func() Accessor { return new(Pod) },
	client.NodeGVR: func() Accessor { return new(Node) },
	client.NsGVR:   func() Accessor { return new(Namespace) },
	client.CmGVR:   func() Accessor { return new(ConfigMap) },
	client.SecGVR:  func() Accessor { return new(Secret) },

	client.DpGVR:  func() Accessor { return new(Deployment) },
	client.DsGVR:  func() Accessor { return new(DaemonSet) },
	client.StsGVR: func() Accessor { return new(StatefulSet) },
	client.RsGVR:  func() Accessor { return new(ReplicaSet) },

	client.CjGVR:  func() Accessor { return new(CronJob) },
	client.JobGVR: func() Accessor { return new(Job) },

	client.HmGVR:  func() Accessor { return new(HelmChart) },
	client.HmhGVR: func() Accessor { return new(HelmHistory) },

	client.CrdGVR: func() Accessor { return new(CustomResourceDefinition) },
}

// AccessorFor returns a client accessor for a resource if registered.
// Otherwise it returns a generic accessor.
// Customize here for non resource types or types with metrics or logs.
func AccessorFor(f Factory, gvr *client.GVR) (Accessor, error) {
	makeAccessor, ok := accessors[gvr]
	var r Accessor
	if ok {
		r = makeAccessor()
	} else {
		r = new(Scaler)
		slog.Debug("No DAO registry entry. Using generics!", slogs.GVR, gvr)
	}
	r.Init(f, gvr)

	return r, nil
}

// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tcell/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

const securityReviewTitle = "Security declarations"
const securityNotDeclared = "not declared"

var errSecurityIdentityChanged = errors.New("selected object identity changed")

var securityReviewTabs = []string{"Facts", "Gaps", "Evidence"}

type securityReviewIdentity struct {
	Context, GVR, Namespace, Name, UID, ResourceVersion string
	CapturedAt                                          time.Time
}

type securityContainerFacts struct {
	Class, Name, Image, ImageID                                                                                                                         string
	Privileged, AllowPrivilegeEscalation, ReadOnlyRootFilesystem, RunAsNonRoot, RunAsUser, RunAsGroup, ProcMount, Seccomp, AppArmor, WindowsHostProcess string
	CapabilitiesAdd, CapabilitiesDrop                                                                                                                   []string
	CapabilitiesAddDeclared, CapabilitiesDropDeclared                                                                                                   bool
	Mounts                                                                                                                                              []string
}

// securityDeclarationSnapshot contains only explicitly allowlisted declarations.
// It intentionally cannot retain arbitrary object fields, annotations, or payloads.
type securityDeclarationSnapshot struct {
	Identity                                                                             securityReviewIdentity
	HostNetwork, HostPID, HostIPC, ServiceAccount, ServiceAccountAlias, AutomountSAToken string
	Volumes                                                                              []string
	Containers                                                                           []securityContainerFacts
	Coverage                                                                             []string
}

type securityReviewView struct {
	*Details
	target               SelectedResourceTarget
	connection           client.Connection
	loader               func(context.Context, SelectedResourceTarget) (securityDeclarationSnapshot, error)
	snapshot             securityDeclarationSnapshot
	status               string
	activeTab            int
	cancel               context.CancelFunc
	generation, revision uint64
	started              bool
}

func (c *Command) securityReviewCommand() {
	owner, ok := c.app.Content.Top().(actionOwner)
	if !ok {
		c.app.Flash().Warn("Select a Pod or native workload to review declared security fields")
		return
	}
	c.app.openSecurityReview(actionTarget(owner, c.app.Config.ActiveContextName()))
}

//nolint:gocritic // Keep the captured target value independent of subsequent navigation.
func (a *App) openSecurityReview(target SelectedResourceTarget) {
	if err := securityReviewTargetError(target); err != nil {
		a.Flash().Warn(err.Error())
		return
	}
	if target.Context != a.Config.ActiveContextName() {
		a.Flash().Warn("Context changed; reopen security review")
		return
	}
	connection, err := pinInspectionConnection(a.Conn())
	if err != nil {
		a.Flash().Err(err)
		return
	}
	v := &securityReviewView{Details: NewDetails(a, securityReviewTitle, target.Path(), contentInspection, true).Update("Reading declared security fields..."),
		target: target, connection: connection, revision: a.Config.DestinationRevision()}
	v.loader = func(ctx context.Context, target SelectedResourceTarget) (securityDeclarationSnapshot, error) {
		dyn, err := connection.DynDial()
		if err != nil {
			return securityDeclarationSnapshot{}, err
		}
		return loadSecurityDeclarations(ctx, dyn, target, time.Now())
	}
	if err := a.inject(v, false); err != nil {
		a.Flash().Err(err)
	}
}

//nolint:gocritic // Validate the immutable selection without taking ownership of it.
func securityReviewTargetError(target SelectedResourceTarget) error {
	if err := target.Err(); err != nil {
		return err
	}
	if !client.IsNamespaced(target.Namespace) {
		return fmt.Errorf("select a namespaced Pod or workload")
	}
	if target.UID == "" || len(target.UID) > 128 {
		return fmt.Errorf("resource UID unavailable; refresh the list and select it again")
	}
	switch target.GVR.String() {
	case client.PodGVR.String(), client.DpGVR.String(), client.StsGVR.String(), client.DsGVR.String(), client.JobGVR.String(), client.CjGVR.String():
		return nil
	default:
		return fmt.Errorf("select a native Pod, Deployment, StatefulSet, DaemonSet, Job or CronJob")
	}
}

func (*securityReviewView) CompactWorkspace() bool                     { return true }
func (v *securityReviewView) SelectedResource() SelectedResourceTarget { return v.target }

func (v *securityReviewView) Init(ctx context.Context) error {
	if err := v.Details.Init(ctx); err != nil {
		return err
	}
	// API-authored text can contain tview color markup; this read-only evidence
	// view renders all retained values literally.
	v.text.SetDynamicColors(false)
	for index, key := range []tcell.Key{ui.Key1, ui.Key2, ui.Key3} {
		tab := index
		v.actions.Add(key, ui.NewKeyAction("Security review: "+securityReviewTabs[index], func(event *tcell.EventKey) *tcell.EventKey {
			if v.cmdBuff.IsActive() {
				return event
			}
			v.activeTab = tab
			v.render()
			return nil
		}, true))
	}
	v.actions.Add(tcell.KeyTab, ui.NewKeyAction("Next security review tab", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.activeTab = (v.activeTab + 1) % len(securityReviewTabs)
		v.render()
		return nil
	}, true))
	v.actions.Add(ui.KeyR, ui.NewKeyAction("Refresh declared security fields", func(event *tcell.EventKey) *tcell.EventKey {
		if v.cmdBuff.IsActive() {
			return event
		}
		v.refresh()
		return nil
	}, true))
	return nil
}

func (v *securityReviewView) Start() {
	v.started = true
	v.Details.Start()
	v.app.Prompt().SetModel(v.cmdBuff)
	v.refresh()
}
func (v *securityReviewView) Stop() {
	v.started = false
	v.generation++
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.Details.Stop()
}
func (v *securityReviewView) ExtraHints() map[string]string {
	hints := map[string]string{
		"Review":    "Declared configuration only; no security score or enforcement claim. Search with /, scroll with j/k, refresh with r.",
		"Providers": "Scanner and admission policy providers are not configured by this native view. Use :providers to inspect available adapters.",
	}
	serviceAccount := v.snapshot.ServiceAccount
	if serviceAccount == securityNotDeclared || serviceAccount == "" {
		serviceAccount = v.snapshot.ServiceAccountAlias
	}
	if serviceAccount != securityNotDeclared && serviceAccount != "" {
		hints["RBAC"] = fmt.Sprintf(
			"Existing RBAC subject-rules view: :can s:%s. Check that the active namespace is %s; "+
				"RBAC rules do not prove workload token use, admission, or runtime authorization.",
			serviceAccount, v.snapshot.Identity.Namespace)
	}
	return hints
}
func (v *securityReviewView) refresh() {
	if v.target.Context != v.app.Config.ActiveContextName() || v.revision != v.app.Config.DestinationRevision() {
		v.Update("Captured destination changed. Reopen :security-review to inspect the current destination.")
		return
	}
	if v.cancel != nil {
		v.cancel()
	}
	v.generation++
	gen := v.generation
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	v.cancel = cancel
	loader := v.loader
	if loader == nil {
		return
	}
	v.status = "Reading captured declarations..."
	v.render()
	target := v.target
	go func() {
		snapshot, err := loader(ctx, target)
		contextErr := ctx.Err()
		cancel()
		if contextErr != nil && err == nil {
			err = contextErr
		}
		if !v.app.IsRunning() {
			return
		}
		v.app.QueueUpdateDraw(func() {
			if !v.current(gen, target.Context) {
				return
			}
			if errors.Is(err, errSecurityIdentityChanged) {
				v.status = "Object identity changed or is unverified; retained declarations describe the previous observation. Reopen the source list."
				v.render()
				return
			}
			if err != nil {
				v.status = "Refresh failed: " + safeSecurityError(err) + "; retained declarations were not refreshed."
				v.render()
				return
			}
			if snapshot.Identity.UID != string(target.UID) {
				v.status = "Selected UID was replaced; retained declarations describe the previous UID. Reopen the source list."
				v.render()
				return
			}
			v.snapshot, v.status = snapshot, ""
			v.render()
		})
	}()
}

func (v *securityReviewView) current(generation uint64, contextName string) bool {
	return v.started && v.generation == generation && v.app.Content.Top() == v &&
		v.revision == v.app.Config.DestinationRevision() && contextName == v.app.Config.ActiveContextName()
}

func safeSecurityError(err error) string {
	if apierrors.IsForbidden(err) {
		return "read denied (Forbidden)"
	}
	if apierrors.IsNotFound(err) {
		return "requested read unavailable (404); object absence unverified"
	}
	if apierrors.IsTimeout(err) {
		return "read timed out"
	}
	return "read failed; inspect connection and permissions with :connection"
}

func (v *securityReviewView) render() {
	s := v.snapshot
	if s.Identity.UID == "" {
		s.Identity = securityReviewIdentity{Context: v.target.Context, Namespace: v.target.Namespace, Name: v.target.Name, UID: string(v.target.UID)}
	}
	capturedAt := "pending"
	if !s.Identity.CapturedAt.IsZero() {
		capturedAt = s.Identity.CapturedAt.UTC().Format(time.RFC3339)
	}
	var b strings.Builder
	if v.status != "" {
		fmt.Fprintf(&b, "%s\n", v.status)
	}
	fmt.Fprintf(&b,
		"%s · %s/%s · UID %s · RV %s\n"+
			"Context %s · captured %s\n\n",
		securityReviewTabs[v.activeTab], s.Identity.Namespace, s.Identity.Name,
		s.Identity.UID, s.Identity.ResourceVersion, s.Identity.Context,
		capturedAt)
	switch v.activeTab {
	case 0:
		fmt.Fprintf(&b,
			"Pod spec declarations\n"+
				"hostNetwork: %s · hostPID: %s · hostIPC: %s\n"+
				"serviceAccountName: %s · deprecated serviceAccount alias: %s\n"+
				"automountServiceAccountToken: %s\n",
			s.HostNetwork, s.HostPID, s.HostIPC, s.ServiceAccount,
			s.ServiceAccountAlias, s.AutomountSAToken)
		for _, vol := range s.Volumes {
			fmt.Fprintf(&b, "volume: %s\n", vol)
		}
		for i := range s.Containers {
			c := &s.Containers[i]
			fmt.Fprintf(&b,
				"\n%s container %s\nimage declared: %s\nimageID observed: %s\n"+
					"privileged: %s · allowPrivilegeEscalation: %s · readOnlyRootFilesystem: %s\n"+
					"runAsNonRoot: %s · runAsUser: %s · runAsGroup: %s · procMount: %s\n"+
					"capabilities add: %s · drop: %s\n"+
					"seccomp: %s · AppArmor field: %s · WindowsHostProcess: %s\n",
				c.Class, c.Name, emptyDeclared(c.Image), emptyUnknown(c.ImageID),
				c.Privileged, c.AllowPrivilegeEscalation, c.ReadOnlyRootFilesystem,
				c.RunAsNonRoot, c.RunAsUser, c.RunAsGroup, c.ProcMount,
				declaredList(c.CapabilitiesAdd, c.CapabilitiesAddDeclared),
				declaredList(c.CapabilitiesDrop, c.CapabilitiesDropDeclared),
				c.Seccomp, c.AppArmor, c.WindowsHostProcess)
			for _, mount := range c.Mounts {
				fmt.Fprintf(&b, "mount: %s\n", mount)
			}
		}
	case 1:
		b.WriteString("Coverage and unknowns\n" + strings.Join(s.Coverage, "\n") +
			"\n\nOmitted fields remain unknown. Kubernetes defaults, admission decisions, " +
			"effective runtime state, traffic and actual enforcement are not inferred. " +
			"Annotation values are excluded, so legacy AppArmor annotations are not inspected.\n")
	case 2:
		fmt.Fprintf(&b,
			"Source: one namespaced GET of captured %s\nIdentity: %s/%s UID %s\n"+
				"Resource version: %s\nCaptured at: %s\n\n"+
				"Optional scanner and policy adapters: not configured / unsupported here. "+
				"No scanner is installed or executed. Exceptions and policy versions are unavailable.\n\n"+
				"This screen presents current API declarations and selected status image IDs as bounded evidence. "+
				"Defaults and admission changes may be included; original authorship is not established. "+
				"Human demand validation and a configured scanner/admission provider remain open.\n",
			s.Identity.GVR, s.Identity.Namespace, s.Identity.Name, s.Identity.UID,
			s.Identity.ResourceVersion, capturedAt)
	}
	v.Update(b.String())
}
func emptyUnknown(s string) string {
	if s == "" {
		return "unknown / unavailable"
	}
	return s
}
func emptyDeclared(s string) string {
	if s == "" {
		return securityNotDeclared
	}
	return s
}
func declaredList(items []string, declared bool) string {
	if !declared {
		return securityNotDeclared
	}
	if len(items) == 0 {
		return "declared (empty)"
	}
	return strings.Join(items, ",")
}

//nolint:gocritic // Keep the captured target value independent of subsequent navigation.
func loadSecurityDeclarations(ctx context.Context, dyn dynamic.Interface, target SelectedResourceTarget, captured time.Time) (securityDeclarationSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return securityDeclarationSnapshot{}, err
	}
	if err := securityReviewTargetError(target); err != nil {
		return securityDeclarationSnapshot{}, err
	}
	obj, err := dyn.Resource(target.GVR.GVR()).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return securityDeclarationSnapshot{}, err
	}
	expectedKind := map[string]string{
		client.PodGVR.String(): "Pod", client.DpGVR.String(): "Deployment",
		client.StsGVR.String(): "StatefulSet", client.DsGVR.String(): "DaemonSet",
		client.JobGVR.String(): "Job", client.CjGVR.String(): "CronJob",
	}[target.GVR.String()]
	if obj == nil || obj.GetUID() != target.UID || obj.GetName() != target.Name || obj.GetNamespace() != target.Namespace ||
		obj.GetAPIVersion() != target.GVR.GV().String() || obj.GetKind() != expectedKind {
		return securityDeclarationSnapshot{}, errSecurityIdentityChanged
	}
	return projectSecurityDeclarations(target, obj, captured), nil
}

//nolint:gocritic // Project from a captured target without mutating the identity value.
func projectSecurityDeclarations(target SelectedResourceTarget, obj *unstructured.Unstructured, captured time.Time) securityDeclarationSnapshot {
	s := securityDeclarationSnapshot{
		Identity: securityReviewIdentity{
			Context: boundedSecurityValue(target.Context), GVR: target.GVR.String(), Namespace: target.Namespace,
			Name: target.Name, UID: string(obj.GetUID()), ResourceVersion: boundedSecurityValue(obj.GetResourceVersion()), CapturedAt: captured,
		},
		Coverage: []string{
			"Pod-spec section: allowlisted declarations projected from this one GET; omitted properties remain unknown.",
			"Container section: init, application and ephemeral declarations projected; status image IDs are included only when present.",
			"Retention limits: 96 containers, 128 hostPath volumes, 64 mounts per container and 64 capabilities per container; facts past a limit are omitted.",
			"Admission and runtime evidence: not collected.", "Scanner/policy provider results and exceptions: not configured.",
		},
	}
	podSpec := securityPodSpec(obj, target.GVR)
	s.HostNetwork = declaredBool(podSpec, "hostNetwork")
	s.HostPID = declaredBool(podSpec, "hostPID")
	s.HostIPC = declaredBool(podSpec, "hostIPC")
	s.ServiceAccount = declaredString(podSpec, "serviceAccountName")
	s.ServiceAccount = boundedSecurityValue(s.ServiceAccount)
	s.ServiceAccountAlias = boundedSecurityValue(declaredString(podSpec, "serviceAccount"))
	s.AutomountSAToken = declaredBool(podSpec, "automountServiceAccountToken")
	projectSecurityVolumes(&s, podSpec)
	projectSecurityContainers(&s, podSpec, obj)
	return s
}

func securityPodSpec(obj *unstructured.Unstructured, gvr *client.GVR) map[string]any {
	root := []string{"spec"}
	if gvr.String() == client.CjGVR.String() {
		root = []string{"spec", "jobTemplate", "spec", "template", "spec"}
	} else if gvr.String() != client.PodGVR.String() {
		root = []string{"spec", "template", "spec"}
	}
	spec, _, _ := unstructured.NestedMap(obj.Object, root...)
	return spec
}

func projectSecurityVolumes(s *securityDeclarationSnapshot, podSpec map[string]any) {
	if vols, ok, _ := unstructured.NestedSlice(podSpec, "volumes"); ok {
		for _, raw := range vols {
			vol, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name, _ := vol["name"].(string)
			hp, found, _ := unstructured.NestedMap(vol, "hostPath")
			if found {
				path, _ := hp["path"].(string)
				typ, _ := hp["type"].(string)
				s.Volumes = appendBounded(s.Volumes, boundedSecurityValue(fmt.Sprintf("%s hostPath %q type=%s", name, path, emptyUnknown(typ))), maxSecurityVolumes)
			}
		}
	}
	if len(s.Volumes) == maxSecurityVolumes {
		s.Coverage = append(s.Coverage, "HostPath volume retention limit reached; additional volumes may be omitted.")
	}
}

func projectSecurityContainers(s *securityDeclarationSnapshot, podSpec map[string]any, obj *unstructured.Unstructured) {
	for _, class := range []struct{ name, path string }{{"init", "initContainers"}, {"app", "containers"}, {"ephemeral", "ephemeralContainers"}} {
		rows, ok, _ := unstructured.NestedSlice(podSpec, class.path)
		if !ok {
			continue
		}
		for _, raw := range rows {
			if len(s.Containers) >= maxSecurityContainers {
				s.Coverage = append(s.Coverage, "Container declarations: capped at the retention limit; remaining containers were not retained.")
				break
			}
			c, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			facts := securityContainerFacts{Class: class.name}
			facts.Name, _ = c["name"].(string)
			facts.Name = boundedSecurityValue(facts.Name)
			facts.Image, _ = c["image"].(string)
			facts.Image = boundedSecurityValue(facts.Image)
			facts.ImageID = statusImageID(obj, class.path, facts.Name)
			facts.Privileged = effectiveDeclaredBool(podSpec, c, "privileged")
			facts.AllowPrivilegeEscalation = effectiveDeclaredBool(podSpec, c, "allowPrivilegeEscalation")
			facts.ReadOnlyRootFilesystem = effectiveDeclaredBool(podSpec, c, "readOnlyRootFilesystem")
			facts.RunAsNonRoot = effectiveDeclaredBool(podSpec, c, "runAsNonRoot")
			facts.RunAsUser = effectiveDeclaredScalar(podSpec, c, "runAsUser")
			facts.RunAsGroup = effectiveDeclaredScalar(podSpec, c, "runAsGroup")
			facts.ProcMount = effectiveDeclaredScalar(podSpec, c, "procMount")
			facts.WindowsHostProcess = effectiveDeclaredNestedBool(podSpec, c, "windowsOptions", "hostProcess")
			facts.Seccomp = effectiveProfile(podSpec, c, "seccompProfile")
			facts.AppArmor = effectiveProfile(podSpec, c, "appArmorProfile")
			facts.CapabilitiesAdd, facts.CapabilitiesDrop, facts.CapabilitiesAddDeclared, facts.CapabilitiesDropDeclared = effectiveCapabilities(c)
			if mounts, ok, _ := unstructured.NestedSlice(c, "volumeMounts"); ok {
				for _, rawMount := range mounts {
					mount, ok := rawMount.(map[string]any)
					if !ok {
						continue
					}
					volume, _ := mount["name"].(string)
					path, _ := mount["mountPath"].(string)
					readOnly := declaredBool(mount, "readOnly")
					subPath := declaredString(mount, "subPath")
					facts.Mounts = appendBounded(
						facts.Mounts,
						boundedSecurityValue(fmt.Sprintf("%s at %s readOnly=%s subPath=%s", emptyDeclared(volume), emptyDeclared(path), readOnly, subPath)),
						maxSecurityMounts,
					)
				}
			}
			s.Containers = append(s.Containers, facts)
		}
	}
}

func declaredBool(spec map[string]any, key string) string {
	if value, ok := spec[key]; ok {
		if b, ok := value.(bool); ok {
			return fmt.Sprint(b)
		}
	}
	return securityNotDeclared
}
func declaredString(spec map[string]any, key string) string {
	if value, ok := spec[key].(string); ok {
		return value
	}
	return securityNotDeclared
}
func declaredSecurityField(spec, c map[string]any, path ...string) (any, bool) {
	if value, found, _ := unstructured.NestedFieldNoCopy(c, append([]string{"securityContext"}, path...)...); found {
		return value, true
	}
	value, found, _ := unstructured.NestedFieldNoCopy(spec, append([]string{"securityContext"}, path...)...)
	return value, found
}
func effectiveDeclaredBool(spec, c map[string]any, key string) string {
	if value, found := declaredSecurityField(spec, c, key); found {
		if v, ok := value.(bool); ok {
			return fmt.Sprint(v)
		}
	}
	return securityNotDeclared
}
func effectiveDeclaredScalar(spec, c map[string]any, key string) string {
	if v, ok := declaredSecurityField(spec, c, key); ok {
		switch value := v.(type) {
		case int64:
			return fmt.Sprint(value)
		case string:
			if key == "procMount" {
				return boundedSecurityValue(value)
			}
		}
	}
	return securityNotDeclared
}
func effectiveDeclaredNestedBool(spec, c map[string]any, parent, key string) string {
	value, found := declaredSecurityField(spec, c, parent, key)
	if v, ok := value.(bool); found && ok {
		return fmt.Sprint(v)
	}
	return securityNotDeclared
}
func effectiveProfile(spec, c map[string]any, key string) string {
	value, ok := declaredSecurityField(spec, c, key, "type")
	v, stringOK := value.(string)
	if !ok || !stringOK {
		return securityNotDeclared
	}
	path, _, _ := unstructured.NestedString(firstDeclaredSecurity(spec, c, key), "localhostProfile")
	if path != "" {
		return boundedSecurityValue(v) + " (profile path omitted)"
	}
	return boundedSecurityValue(v)
}
func effectiveCapabilities(c map[string]any) (add, drop []string, addDeclared, dropDeclared bool) {
	_, addDeclared, _ = unstructured.NestedFieldNoCopy(c, "securityContext", "capabilities", "add")
	_, dropDeclared, _ = unstructured.NestedFieldNoCopy(c, "securityContext", "capabilities", "drop")
	for _, pair := range []struct {
		key string
		out *[]string
	}{{"add", &add}, {"drop", &drop}} {
		list, _, _ := unstructured.NestedStringSlice(c, "securityContext", "capabilities", pair.key)
		list = append([]string(nil), list...)
		if len(list) > maxSecurityCapabilities {
			list = list[:maxSecurityCapabilities]
		}
		for index := range list {
			list[index] = boundedSecurityValue(list[index])
		}
		*pair.out = list
	}
	return add, drop, addDeclared, dropDeclared
}
func firstDeclaredSecurity(spec, c map[string]any, key string) map[string]any {
	if value, ok, _ := unstructured.NestedMap(c, "securityContext", key); ok {
		return value
	}
	value, _, _ := unstructured.NestedMap(spec, "securityContext", key)
	return value
}
func boundedSecurityValue(value string) string {
	const limit = 512
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return value
}

const (
	maxSecurityContainers   = 96
	maxSecurityVolumes      = 128
	maxSecurityMounts       = 64
	maxSecurityCapabilities = 64
)

func appendBounded(items []string, value string, limit int) []string {
	if len(items) < limit {
		return append(items, value)
	}
	return items
}
func statusImageID(obj *unstructured.Unstructured, class, name string) string {
	statusClasses := map[string]string{
		"initContainers": "initContainerStatuses", "containers": "containerStatuses",
		"ephemeralContainers": "ephemeralContainerStatuses",
	}
	statusClass := statusClasses[class]
	rows, ok, _ := unstructured.NestedSlice(obj.Object, "status", statusClass)
	if !ok {
		return ""
	}
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if row["name"] == name {
			id, _ := row["imageID"].(string)
			return boundedSecurityValue(id)
		}
	}
	return ""
}

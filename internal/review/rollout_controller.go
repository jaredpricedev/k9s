// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const rolloutStatefulSet, rolloutDaemonSet, rolloutControllerRevision = "StatefulSet", "DaemonSet", "ControllerRevision"
const RolloutManual = "manual update"
const rolloutRollingUpdate, rolloutOnDelete = "RollingUpdate", "OnDelete"

type RolloutConfiguration struct{ Kind, Name, Namespace, Source, Revision, State string }

func newControllerRollout(workload *unstructured.Unstructured, revisions, pods []*unstructured.Unstructured,
	coverage []RolloutCoverage, contextName string, at time.Time) *RolloutSnapshot {
	kind := workload.GetKind()
	resource := "apps/v1/statefulsets"
	if kind == rolloutDaemonSet {
		resource = "apps/v1/daemonsets"
	}
	s := &RolloutSnapshot{Identity: rolloutIdentity(workload, contextName, resource), Kind: kind, CapturedAt: at, DeploymentState: inspect.ObservationComplete,
		ResourceVersion: workload.GetResourceVersion(), TemplateSHA256: rolloutTemplateDigest(workload)}
	for _, c := range coverage {
		s.Coverage = append(s.Coverage, RolloutCoverage{Source: rolloutText(c.Source), State: rolloutText(c.State), Detail: rolloutText(c.Detail)})
	}
	s.Generation = rolloutNumber(workload.Object, "metadata", "generation")
	s.ObservedGeneration = rolloutNumber(workload.Object, "status", "observedGeneration")
	s.Conditions = rolloutConditions(workload)
	s.Strategy, _, _ = unstructured.NestedString(workload.Object, "spec", "updateStrategy", "type")
	if s.Strategy == "" {
		s.Strategy = rolloutRollingUpdate
	}
	s.DeploymentTemplate = rolloutTemplateObservation(workload, s.Identity, at, false)
	s.Configuration = rolloutConfiguration(workload)
	if kind == rolloutStatefulSet {
		s.Desired = rolloutNumber(workload.Object, "spec", "replicas")
		s.Updated = rolloutNumber(workload.Object, "status", "updatedReplicas")
		s.Ready = rolloutNumber(workload.Object, "status", "readyReplicas")
		s.Available = rolloutNumber(workload.Object, "status", "availableReplicas")
		s.Replicas = rolloutNumber(workload.Object, "status", "replicas")
		s.CurrentRevision, _, _ = unstructured.NestedString(workload.Object, "status", "currentRevision")
		s.UpdateRevision, _, _ = unstructured.NestedString(workload.Object, "status", "updateRevision")
		s.Partition = rolloutDefaultNumber(workload.Object, "spec", "updateStrategy", "rollingUpdate", "partition")
		s.StartOrdinal = rolloutDefaultNumber(workload.Object, "spec", "ordinals", "start")
	} else {
		s.Desired = rolloutNumber(workload.Object, "status", "desiredNumberScheduled")
		s.Updated = rolloutNumber(workload.Object, "status", "updatedNumberScheduled")
		s.Ready = rolloutNumber(workload.Object, "status", "numberReady")
		s.Available = rolloutNumber(workload.Object, "status", "numberAvailable")
		s.Replicas = rolloutNumber(workload.Object, "status", "currentNumberScheduled")
		s.Misscheduled = rolloutNumber(workload.Object, "status", "numberMisscheduled")
	}
	if s.Identity.UID == "" {
		s.DeploymentState = inspect.ObservationUnknown
		s.addCoverage("identity", inspect.ObservationUnknown, "Workload UID missing; descendants and outcome continuity cannot be verified")
		return s
	}
	s.addCoverage("Configuration revisions", inspect.ObservationUnknown,
		"Only declared references and checksum-shaped annotations retained; live ConfigMap/Secret revisions are not collected")
	s.addControllerRevisions(workload, revisions, contextName)
	s.addControllerPods(pods, contextName)
	return s
}

func (s *RolloutSnapshot) controllerProgress() (state, reason string) {
	if state, reason := s.controllerObservationVerdict(); state != "" {
		return state, reason
	}
	required := *s.Desired
	partition := int64(0)
	if s.Kind == rolloutStatefulSet && s.Strategy == rolloutRollingUpdate {
		if s.Partition == nil || s.StartOrdinal == nil {
			return RolloutUnknown, "StatefulSet partition or start ordinal invalid/unavailable"
		}
		partition = *s.Partition
		// Partition is an absolute Pod ordinal, not an offset into replicas.
		offset := min(required, max(int64(0), partition-*s.StartOrdinal))
		required -= offset
	}
	ready := *s.Replicas == *s.Desired && *s.Ready >= *s.Desired && *s.Available >= *s.Desired
	updated := *s.Updated >= required
	if s.Strategy == rolloutOnDelete && (!ready || *s.Updated < *s.Desired) {
		return RolloutManual, "OnDelete does not automatically replace Pods; explicit operator updates are required"
	}
	if s.Strategy != rolloutOnDelete && s.Strategy != rolloutRollingUpdate {
		return RolloutUnknown, "Unrecognized controller update strategy"
	}
	if s.Kind == rolloutStatefulSet && s.Strategy == rolloutRollingUpdate && required == *s.Desired && (s.CurrentRevision == "" || s.UpdateRevision == "") {
		return RolloutUnknown, "StatefulSet current/update revision identities unavailable"
	}
	if s.Kind == rolloutStatefulSet && s.Strategy == rolloutRollingUpdate && required == *s.Desired && s.CurrentRevision != s.UpdateRevision {
		return RolloutProgressing, "StatefulSet current and update revisions have not converged"
	}
	if ready && updated {
		if required < *s.Desired {
			return RolloutComplete, fmt.Sprintf("Partition %d rollout target is observed available; lower ordinals intentionally keep prior templates", partition)
		}
		return RolloutComplete, s.Kind + " status reports the current generation available; child evidence has separate coverage"
	}
	return RolloutProgressing, "Current generation observed; configured updated/available target has not converged"
}

func (s *RolloutSnapshot) addControllerRevisions(workload *unstructured.Unstructured, objects []*unstructured.Unstructured, contextName string) {
	if len(objects) > rolloutInputLimit {
		s.addCoverage("ControllerRevisions", inspect.ObservationIncomplete, "Input exceeded bounded collection limit")
	}
	for _, object := range objects[:min(len(objects), rolloutInputLimit)] {
		if !rolloutNativeChild(object, rolloutAppsAPI, rolloutControllerRevision, workload.GetNamespace()) ||
			!rolloutOwnedBy(object, s.Kind, s.Identity.UID) || object.GetUID() == "" {
			continue
		}
		identity := rolloutIdentity(object, contextName, "apps/v1/controllerrevisions")
		revision := RolloutRevision{Identity: identity, ResourceVersion: object.GetResourceVersion()}
		if number := rolloutNumber(object.Object, "revision"); number != nil {
			revision.Revision = fmt.Sprint(*number)
		}
		template, valid := controllerRevisionTemplate(object)
		if valid {
			projection := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"template": template}}}
			revision.Template = rolloutTemplateObservation(projection, identity, s.CapturedAt, true)
			revision.Template.Source = "ControllerRevision API /data/spec/template"
			revision.TemplateSHA256 = rolloutTemplateDigest(projection)
			revision.Current, revision.CurrentKnown = rolloutTemplatesMatch(workload, projection)
		} else {
			revision.Template = inspect.Observation{Identity: identity, ObservedAt: s.CapturedAt, Source: "ControllerRevision API /data/spec/template",
				State: inspect.ObservationUnknown, Reason: "Revision data does not contain a bounded reviewable Pod template"}
			s.addCoverage("ControllerRevisions", inspect.ObservationIncomplete, "A retained revision's template was unavailable")
		}
		s.Revisions = append(s.Revisions, revision)
	}
	sort.Slice(s.Revisions, func(i, j int) bool { return s.Revisions[i].Identity.Name < s.Revisions[j].Identity.Name })
	if len(s.Revisions) > MaxRolloutRevisions {
		s.Revisions = s.Revisions[:MaxRolloutRevisions]
		s.addCoverage("ControllerRevisions", inspect.ObservationIncomplete, "Retained revision cap reached")
	}
	s.reportControllerMatching()
}

func controllerRevisionTemplate(object *unstructured.Unstructured) (map[string]any, bool) {
	data, found, _ := unstructured.NestedMap(object.Object, "data")
	if !found {
		return nil, false
	}
	encoded, err := json.Marshal(data)
	if err != nil || len(encoded) > MaxSourceBytes {
		return nil, false
	}
	template, found, _ := unstructured.NestedMap(data, "spec", "template")
	if !found {
		return nil, false
	}
	if patch, exists := template["$patch"]; exists && patch != "replace" {
		return nil, false
	}
	delete(template, "$patch")
	return template, true
}

func (s *RolloutSnapshot) addControllerPods(objects []*unstructured.Unstructured, contextName string) {
	if len(objects) > rolloutInputLimit {
		s.addCoverage("Pods", inspect.ObservationIncomplete, "Input exceeded bounded collection limit")
	}
	for _, object := range objects[:min(len(objects), rolloutInputLimit)] {
		if !rolloutNativeChild(object, "v1", "Pod", s.Identity.Namespace) || !rolloutOwnedBy(object, s.Kind, s.Identity.UID) || object.GetUID() == "" {
			continue
		}
		phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
		s.Pods = append(s.Pods, RolloutPod{Identity: rolloutIdentity(object, contextName, "v1/pods"),
			OwnerKind: s.Kind, OwnerUID: s.Identity.UID, Phase: rolloutText(phase), Images: rolloutImages(object)})
	}
	sort.Slice(s.Pods, func(i, j int) bool { return s.Pods[i].Identity.Name < s.Pods[j].Identity.Name })
	if len(s.Pods) > MaxRolloutPods {
		s.Pods = s.Pods[:MaxRolloutPods]
		s.addCoverage("Pods", inspect.ObservationIncomplete, "Retained Pod cap reached")
	}
}

func rolloutTemplateDigest(object *unstructured.Unstructured) string {
	template, found, _ := unstructured.NestedMap(object.Object, "spec", "template")
	if !found {
		return ""
	}
	rolloutRemoveHash(template)
	encoded, err := json.Marshal(template)
	if err != nil || len(encoded) > MaxSourceBytes {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// Declared references and checksum-shaped annotations are retained configuration
// evidence. No ConfigMap or Secret contents/revisions are fetched or inferred.
func rolloutConfiguration(workload *unstructured.Unstructured) []RolloutConfiguration {
	var refs []RolloutConfiguration
	annotations, _, _ := unstructured.NestedStringMap(workload.Object, "spec", "template", "metadata", "annotations")
	keys := make([]string, 0, len(annotations))
	for key := range annotations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if len(refs) >= 32 {
			break
		}
		value := annotations[key]
		if !strings.Contains(strings.ToLower(key), "checksum") || len(value) < 8 || len(value) > 128 {
			continue
		}
		if _, err := hex.DecodeString(value); err != nil {
			continue
		}
		refs = append(refs, RolloutConfiguration{Kind: "template checksum annotation", Name: rolloutText(key), Namespace: workload.GetNamespace(),
			Source: "Pod template annotation /" + rolloutText(key), Revision: strings.ToLower(value),
			State: "declared metadata; live configuration revision unverified"})
	}
	return appendConfigurationReferences(refs, workload)
}

func (s *RolloutSnapshot) reportControllerMatching() {
	state := inspect.ObservationComplete
	if len(s.Revisions) == 0 {
		state = inspect.ObservationUnknown
	}
	for i := range s.Revisions {
		if !s.Revisions[i].CurrentKnown {
			state = inspect.ObservationIncomplete
		}
	}
	s.addCoverage("Template matching", state, "Template equality describes retained equivalence; reported current/update revision names remain separate controller evidence")
}

func appendConfigurationReferences(refs []RolloutConfiguration, workload *unstructured.Unstructured) []RolloutConfiguration {
	seen := make(map[string]bool)
	add := func(kind, name, source string) {
		if len(refs) >= 32 || !validReviewName(name) || seen[kind+"/"+name] {
			return
		}
		seen[kind+"/"+name] = true
		refs = append(refs, RolloutConfiguration{Kind: kind, Name: name, Namespace: workload.GetNamespace(), Source: source,
			State: "declared reference; live configuration revision unverified"})
	}
	for _, group := range []string{"containers", "initContainers"} {
		containers, _, _ := unstructured.NestedSlice(workload.Object, "spec", "template", "spec", group)
		for _, raw := range containers[:min(len(containers), rolloutStatusLimit)] {
			container, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			env, _, _ := unstructured.NestedSlice(container, "env")
			for _, rawEnv := range env[:min(len(env), rolloutStatusLimit)] {
				field, ok := rawEnv.(map[string]any)
				if !ok {
					continue
				}
				for _, ref := range []struct{ key, kind string }{{"configMapKeyRef", "ConfigMap"}, {"secretKeyRef", "Secret"}} {
					name, _, _ := unstructured.NestedString(field, "valueFrom", ref.key, "name")
					add(ref.kind, name, "Pod template env reference")
				}
			}
			envFrom, _, _ := unstructured.NestedSlice(container, "envFrom")
			for _, rawFrom := range envFrom[:min(len(envFrom), rolloutStatusLimit)] {
				field, ok := rawFrom.(map[string]any)
				if !ok {
					continue
				}
				for _, ref := range []struct{ key, kind string }{{"configMapRef", "ConfigMap"}, {"secretRef", "Secret"}} {
					name, _, _ := unstructured.NestedString(field, ref.key, "name")
					add(ref.kind, name, "Pod template envFrom reference")
				}
			}
		}
	}
	volumes, _, _ := unstructured.NestedSlice(workload.Object, "spec", "template", "spec", "volumes")
	for _, raw := range volumes[:min(len(volumes), rolloutStatusLimit)] {
		field, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(field, "configMap", "name")
		add("ConfigMap", name, "Pod template volume reference")
		name, _, _ = unstructured.NestedString(field, "secret", "secretName")
		add("Secret", name, "Pod template volume reference")
	}
	return refs
}

// API defaults establish strategy parameters, never missing observed counts.
func rolloutDefaultNumber(object map[string]any, fields ...string) *int64 {
	_, found, err := unstructured.NestedFieldNoCopy(object, fields...)
	if !found && err == nil {
		zero := int64(0)
		return &zero
	}
	return rolloutNumber(object, fields...)
}

func (s *RolloutSnapshot) controllerObservationVerdict() (state, reason string) {
	if s.DeploymentState != inspect.ObservationComplete || s.Identity.UID == "" {
		return RolloutUnknown, "Controller identity or API observation unavailable"
	}
	if s.Generation == nil || *s.Generation <= 0 || s.ObservedGeneration == nil {
		return RolloutUnknown, "Current generation or controller observedGeneration unavailable"
	}
	if *s.ObservedGeneration < *s.Generation {
		return RolloutProgressing, "Controller has not observed the current generation; older conditions are not a current verdict"
	}
	if *s.ObservedGeneration > *s.Generation {
		return RolloutUnknown, "Controller observedGeneration exceeds resource generation"
	}
	for _, c := range s.Conditions {
		if c.Status == "True" && (c.Type == "ReplicaFailure" || c.Type == "Failed") || c.Type == "Progressing" && c.Status == "False" {
			reason := c.Reason
			if reason == "" {
				reason = c.Type + "=" + c.Status
			}
			return RolloutBlocked, reason
		}
	}
	for _, count := range []*int64{s.Desired, s.Updated, s.Ready, s.Available, s.Replicas} {
		if count == nil {
			return RolloutUnknown, "One or more controller replica counts are unavailable; missing is not zero"
		}
	}
	if s.Kind == rolloutDaemonSet {
		if s.Misscheduled == nil {
			return RolloutUnknown, "DaemonSet misscheduled count unavailable"
		}
		if *s.Misscheduled > 0 {
			return RolloutProgressing, "DaemonSet still reports Pods on nodes outside its desired schedule"
		}
	}
	return "", ""
}

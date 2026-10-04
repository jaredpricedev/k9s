// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package configreview

import (
	corev1 "k8s.io/api/core/v1"
)

// References projects only names and wiring from a PodSpec. A literal env value
// is intentionally ignored, as are annotations and complete resource objects.
func References(consumer *Identity, spec *corev1.PodSpec) []Reference {
	if consumer == nil || spec == nil {
		return nil
	}
	var refs []Reference
	containers := append(append([]corev1.Container(nil), spec.InitContainers...), spec.Containers...)
	for i := range spec.EphemeralContainers {
		c := &spec.EphemeralContainers[i].EphemeralContainerCommon
		containers = append(containers, corev1.Container{Name: c.Name, Env: c.Env, EnvFrom: c.EnvFrom, VolumeMounts: c.VolumeMounts})
	}
	for i := range containers {
		c := &containers[i]
		refs = boundedAppend(refs, containerReferences(consumer, c))
		if len(refs) > MaxReferences {
			return refs
		}
		for j := range c.VolumeMounts {
			mount := &c.VolumeMounts[j]
			for k := range spec.Volumes {
				volume := &spec.Volumes[k]
				if volume.Name == mount.Name {
					refs = boundedAppend(refs, volumeReferences(consumer, c.Name, mount, volume))
					if len(refs) > MaxReferences {
						return refs
					}
				}
			}
		}
	}
	for _, pull := range spec.ImagePullSecrets[:min(len(spec.ImagePullSecrets), MaxReferences+1)] {
		refs = append(refs, Reference{Consumer: *consumer, Kind: SecretKind, Name: pull.Name, Use: "image pull"})
		if len(refs) > MaxReferences {
			return refs
		}
	}
	return refs
}

func containerReferences(consumer *Identity, c *corev1.Container) []Reference {
	var refs []Reference
	for i := range c.Env {
		if len(refs) > MaxReferences {
			return refs
		}
		env := &c.Env[i]
		if env.ValueFrom == nil {
			continue
		}
		if from := env.ValueFrom.ConfigMapKeyRef; from != nil {
			refs = append(refs, Reference{Consumer: *consumer, Kind: ConfigMapKind, Name: from.Name, Key: from.Key,
				Container: c.Name, Variable: env.Name, Use: "env", Optional: optional(from.Optional)})
		}
		if from := env.ValueFrom.SecretKeyRef; from != nil {
			refs = append(refs, Reference{Consumer: *consumer, Kind: SecretKind, Name: from.Name, Key: from.Key,
				Container: c.Name, Variable: env.Name, Use: "env", Optional: optional(from.Optional)})
		}
	}
	for i := range c.EnvFrom {
		if len(refs) > MaxReferences {
			return refs
		}
		from := &c.EnvFrom[i]
		if cm := from.ConfigMapRef; cm != nil {
			refs = append(refs, Reference{Consumer: *consumer, Kind: ConfigMapKind, Name: cm.Name,
				Container: c.Name, Use: "envFrom", Prefix: from.Prefix, Optional: optional(cm.Optional)})
		}
		if secret := from.SecretRef; secret != nil {
			refs = append(refs, Reference{Consumer: *consumer, Kind: SecretKind, Name: secret.Name,
				Container: c.Name, Use: "envFrom", Prefix: from.Prefix, Optional: optional(secret.Optional)})
		}
	}
	return refs
}

func volumeReferences(consumer *Identity, container string, mount *corev1.VolumeMount, volume *corev1.Volume) []Reference {
	base := Reference{Consumer: *consumer, Container: container, Use: "volume", Mount: mount.MountPath,
		SubPath: mount.SubPath != "" || mount.SubPathExpr != ""}
	var refs []Reference
	if cm := volume.ConfigMap; cm != nil {
		refs = boundedAppend(refs, itemReferences(&base, ConfigMapKind, cm.Name, cm.Items, cm.Optional))
	}
	if secret := volume.Secret; secret != nil {
		refs = boundedAppend(refs, itemReferences(&base, SecretKind, secret.SecretName, secret.Items, secret.Optional))
	}
	if projected := volume.Projected; projected != nil {
		for i := range projected.Sources {
			if len(refs) > MaxReferences {
				return refs
			}
			source := &projected.Sources[i]
			if cm := source.ConfigMap; cm != nil {
				refs = boundedAppend(refs, itemReferences(&base, ConfigMapKind, cm.Name, cm.Items, cm.Optional))
			}
			if secret := source.Secret; secret != nil {
				refs = boundedAppend(refs, itemReferences(&base, SecretKind, secret.Name, secret.Items, secret.Optional))
			}
		}
	}
	if csi := volume.CSI; csi != nil && csi.NodePublishSecretRef != nil {
		base.Kind, base.Name, base.Use = SecretKind, csi.NodePublishSecretRef.Name, "CSI node publish"
		refs = append(refs, base)
	}
	return refs
}

func itemReferences(base *Reference, kind, name string, items []corev1.KeyToPath, opt *bool) []Reference {
	ref := *base
	ref.Kind, ref.Name, ref.Optional = kind, name, optional(opt)
	if len(items) == 0 {
		return []Reference{ref}
	}
	refs := make([]Reference, 0, min(len(items), MaxReferences+1))
	for _, item := range items[:min(len(items), MaxReferences+1)] {
		itemRef := ref
		itemRef.Key, itemRef.ItemPath = item.Key, item.Path
		refs = append(refs, itemRef)
	}
	return refs
}

func optional(value *bool) bool { return value != nil && *value }

func boundedAppend(refs, added []Reference) []Reference {
	remaining := MaxReferences + 1 - len(refs)
	return append(refs, added[:min(len(added), max(0, remaining))]...)
}

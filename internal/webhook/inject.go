package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	stokerv1alpha1 "github.com/ia-eknorr/stoker-operator/api/v1alpha1"
	stokertypes "github.com/ia-eknorr/stoker-operator/pkg/types"
)

const (
	agentContainerName = "stoker-agent"

	defaultAgentImage = "ghcr.io/ia-eknorr/stoker-agent:latest"

	// Volume names injected by the webhook.
	volumeSyncRepo       = "sync-repo"
	volumeGitCredentials = "git-credentials"
	volumeAPIKey         = "api-key"
	volumeGitHubToken    = "git-token"
	volumeGitTmp         = "git-tmp"
	volumeKnownHosts     = "known-hosts"

	// Mount paths inside the agent container.
	mountRepo           = "/repo"
	mountIgnitionData   = "/ignition-data"
	mountGitCredentials = "/etc/stoker/git-credentials"
	mountAPIKey         = "/etc/stoker/api-key"
	mountGitHubToken    = "/etc/stoker/git-token"
	mountKnownHosts     = "/etc/stoker/known-hosts"

	// Environment variable for operator-level default agent image.
	envDefaultAgentImage = "DEFAULT_AGENT_IMAGE"

	// annotationTrue is the canonical "true" value for boolean annotations.
	annotationTrue = "true"
)

// PodInjector implements admission.Handler for sidecar injection.
type PodInjector struct {
	Client  client.Client
	Decoder admission.Decoder
}

// Handle processes admission requests for pod creation and injects the stoker-agent sidecar.
func (p *PodInjector) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := logf.FromContext(ctx).WithName("pod-injector")

	pod := &corev1.Pod{}
	if err := p.Decoder.Decode(req, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Early return for non-annotated pods (~1ms, no network calls)
	if pod.Annotations[stokertypes.AnnotationInject] != annotationTrue {
		webhookInjectorInjectionsTotal.WithLabelValues(req.Namespace, "skipped").Inc()
		return admission.Allowed("injection not requested")
	}

	// Idempotency: skip if already injected
	if isAlreadyInjected(pod) {
		webhookInjectorInjectionsTotal.WithLabelValues(req.Namespace, "skipped").Inc()
		return admission.Allowed("already injected")
	}

	// Resolve CR name (annotation or auto-derive)
	crName, err := p.resolveCRName(ctx, req.Namespace, pod)
	if err != nil {
		log.Info("denied injection", "pod", pod.Name, "reason", err.Error())
		return admission.Denied(err.Error())
	}

	// Write discovered CR name back to pod so the controller can find it
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	if pod.Annotations[stokertypes.AnnotationCRName] == "" {
		pod.Annotations[stokertypes.AnnotationCRName] = crName
	}

	// Fetch GatewaySync CR
	var gs stokerv1alpha1.GatewaySync
	key := client.ObjectKey{Name: crName, Namespace: req.Namespace}
	if err := p.Client.Get(ctx, key, &gs); err != nil {
		if apierrors.IsNotFound(err) {
			return admission.Denied(fmt.Sprintf(
				"GatewaySync '%s' not found in namespace '%s'", crName, req.Namespace))
		}
		return admission.Errored(http.StatusInternalServerError, err)
	}

	// Check if CR is paused
	if gs.Spec.Paused {
		return admission.Denied(fmt.Sprintf(
			"GatewaySync '%s' is paused", crName))
	}

	// Validate profile if specified — check against embedded profiles map
	profileName := pod.Annotations[stokertypes.AnnotationProfile]
	if profileName != "" {
		if _, exists := gs.Spec.Sync.Profiles[profileName]; !exists {
			return admission.Denied(fmt.Sprintf(
				"profile '%s' not found in GatewaySync '%s'", profileName, gs.Name))
		}
	}

	// Inject sidecar
	injectSidecar(pod, &gs)

	// Return JSON patch
	marshaledPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	webhookInjectorInjectionsTotal.WithLabelValues(req.Namespace, "injected").Inc()
	log.Info("injected stoker-agent sidecar", "pod", pod.Name, "cr", crName, "namespace", req.Namespace)
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledPod)
}

// isAlreadyInjected checks if the stoker-agent container already exists.
func isAlreadyInjected(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.InitContainers {
		if c.Name == agentContainerName {
			return true
		}
	}
	return false
}

// resolveCRName resolves the GatewaySync CR name from annotation or auto-derives it.
func (p *PodInjector) resolveCRName(ctx context.Context, namespace string, pod *corev1.Pod) (string, error) {
	if crName := pod.Annotations[stokertypes.AnnotationCRName]; crName != "" {
		return crName, nil
	}

	// Auto-discover: list CRs in namespace
	var list stokerv1alpha1.GatewaySyncList
	if err := p.Client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return "", fmt.Errorf("failed to list GatewaySync CRs: %w", err)
	}

	switch len(list.Items) {
	case 0:
		return "", fmt.Errorf("no GatewaySync CR found in namespace '%s'", namespace)
	case 1:
		return list.Items[0].Name, nil
	default:
		names := make([]string, len(list.Items))
		for i, item := range list.Items {
			names[i] = item.Name
		}
		return "", fmt.Errorf(
			"multiple GatewaySync CRs in namespace '%s': [%s] — set annotation '%s' explicitly",
			namespace, strings.Join(names, ", "), stokertypes.AnnotationCRName)
	}
}

// injectSidecar patches the pod spec with the stoker-agent native sidecar.
func injectSidecar(pod *corev1.Pod, gs *stokerv1alpha1.GatewaySync) {
	image := resolveAgentImage(gs)
	pullPolicy := resolveAgentPullPolicy(gs)

	// Determine gateway name for env var
	gatewayName := pod.Annotations[stokertypes.AnnotationGatewayName]

	// Determine profile name
	profile := pod.Annotations[stokertypes.AnnotationProfile]

	// Determine CR name — use annotation if set, otherwise use gs.Name
	crName := pod.Annotations[stokertypes.AnnotationCRName]
	if crName == "" {
		crName = gs.Name
	}

	// Gateway port and TLS from CR. The API server applies the CRD defaults
	// (8088, TLS off); these fallbacks only fire when defaulting was bypassed
	// and must agree with the CRD so behavior doesn't depend on the path taken.
	gatewayPort := fmt.Sprintf("%d", gs.Spec.Gateway.Port)
	if gs.Spec.Gateway.Port == 0 {
		gatewayPort = "8088"
	}
	gatewayTLS := "false"
	if gs.Spec.Gateway.TLS != nil && *gs.Spec.Gateway.TLS {
		gatewayTLS = annotationTrue
	}

	// Build env vars
	env := buildEnvVars(crName, gatewayName, profile, gatewayPort, gatewayTLS, gs)

	// Build resources
	resources := buildResources(gs)

	// Security context (restricted PSS).
	// We intentionally omit RunAsUser so the agent inherits the pod-level
	// security context. This ensures files written to the shared data volume
	// are owned by the same UID as the gateway container (e.g. 2003 for
	// Ignition helm chart pods), preventing permission errors.
	restartAlways := corev1.ContainerRestartPolicyAlways
	agentContainer := corev1.Container{
		Name:            agentContainerName,
		Image:           image,
		ImagePullPolicy: corev1.PullPolicy(pullPolicy),
		Command:         []string{"/agent"},
		RestartPolicy:   &restartAlways,
		Env:             env,
		Resources:       resources,
		Ports: []corev1.ContainerPort{
			{Name: "metrics", ContainerPort: 8083, Protocol: corev1.ProtocolTCP},
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             ptr.To(true),
			ReadOnlyRootFilesystem:   ptr.To(true),
			AllowPrivilegeEscalation: ptr.To(false),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/startupz",
					Port: intstr.FromInt32(8082),
				},
			},
			PeriodSeconds:    2,
			FailureThreshold: 150,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromInt32(8082),
				},
			},
			PeriodSeconds:    10,
			FailureThreshold: 3,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/readyz",
					Port: intstr.FromInt32(8082),
				},
			},
			PeriodSeconds:    5,
			FailureThreshold: 3,
		},
		VolumeMounts: agentVolumeMounts(gs),
	}

	// Mount the Ignition data volume if it exists on the pod.
	// Try well-known names: "ignition-data" (explicit) or "data" (Ignition helm chart PVC).
	// When found, discover the mount path from existing containers to set DATA_PATH correctly.
	dataVolName, dataPath := resolveDataVolume(pod)
	if dataVolName != "" {
		agentContainer.VolumeMounts = append(agentContainer.VolumeMounts, corev1.VolumeMount{
			Name:      dataVolName,
			MountPath: dataPath,
		})
		// Override DATA_PATH env var to match the actual mount
		for i := range agentContainer.Env {
			if agentContainer.Env[i].Name == "DATA_PATH" {
				agentContainer.Env[i].Value = dataPath
				break
			}
		}
	}

	// Prepend as native sidecar (initContainer with restartPolicy: Always)
	pod.Spec.InitContainers = append([]corev1.Container{agentContainer}, pod.Spec.InitContainers...)

	// Add volumes
	pod.Spec.Volumes = append(pod.Spec.Volumes, agentVolumes(gs)...)

	// Set injected annotation
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[stokertypes.AnnotationInjected] = annotationTrue

	// Set agent label for PodMonitor discovery (labels are indexed, annotations are not).
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels[stokertypes.LabelAgent] = annotationTrue
}

// resolveAgentImage resolves the agent image using 2-tier priority:
// 1. CR spec.agent.image
// 2. Environment variable / hardcoded default
func resolveAgentImage(gs *stokerv1alpha1.GatewaySync) string {
	spec := gs.Spec.Agent.Image
	if spec.Repository != "" {
		tag := spec.Tag
		if tag == "" {
			tag = "latest"
		}
		return spec.Repository + ":" + tag
	}

	if img := os.Getenv(envDefaultAgentImage); img != "" {
		return img
	}
	return defaultAgentImage
}

// resolveAgentPullPolicy returns the image pull policy from CR spec or default.
func resolveAgentPullPolicy(gs *stokerv1alpha1.GatewaySync) string {
	if p := gs.Spec.Agent.Image.PullPolicy; p != "" {
		return p
	}
	return "IfNotPresent"
}

// buildEnvVars constructs the environment variables for the agent container.
func buildEnvVars(crName, gatewayName, profile, gatewayPort, gatewayTLS string, gs *stokerv1alpha1.GatewaySync) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{
			Name: "POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
			},
		},
		{
			Name: "POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
			},
		},
		{Name: "CR_NAME", Value: crName},
		{
			Name: "CR_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
			},
		},
		{Name: "GATEWAY_NAME", Value: gatewayName},
		{Name: "PROFILE", Value: profile},
		{Name: "REPO_PATH", Value: mountRepo},
		{Name: "DATA_PATH", Value: mountIgnitionData},
		{Name: "GATEWAY_PORT", Value: gatewayPort},
		{Name: "GATEWAY_TLS", Value: gatewayTLS},
		{Name: "API_KEY_FILE", Value: mountAPIKey + "/" + gs.Spec.Gateway.API.SecretKey},
	}

	// Git credential env vars depend on auth type
	if gs.Spec.Git.Auth != nil {
		if gs.Spec.Git.Auth.SSHKey != nil {
			env = append(env, corev1.EnvVar{
				Name:  "GIT_SSH_KEY_FILE",
				Value: mountGitCredentials + "/" + gs.Spec.Git.Auth.SSHKey.SecretRef.Key,
			})
			if gs.Spec.Git.Auth.SSHKey.KnownHosts != nil {
				env = append(env, corev1.EnvVar{
					Name:  "GIT_KNOWN_HOSTS_FILE",
					Value: mountKnownHosts + "/" + gs.Spec.Git.Auth.SSHKey.KnownHosts.SecretRef.Key,
				})
			}
		} else if gs.Spec.Git.Auth.Token != nil {
			env = append(env, corev1.EnvVar{
				Name:  "GIT_TOKEN_FILE",
				Value: mountGitCredentials + "/" + gs.Spec.Git.Auth.Token.SecretRef.Key,
			})
		} else if gs.Spec.Git.Auth.GitHubApp != nil {
			env = append(env, corev1.EnvVar{
				Name:  "GIT_TOKEN_FILE",
				Value: mountGitHubToken + "/token",
			})
		}
	}

	// Sync period defaults to 30
	env = append(env, corev1.EnvVar{Name: "SYNC_PERIOD", Value: "30"})

	return env
}

// buildResources returns the agent container resources from CR spec or defaults.
func buildResources(gs *stokerv1alpha1.GatewaySync) corev1.ResourceRequirements {
	if gs.Spec.Agent.Resources != nil {
		return *gs.Spec.Agent.Resources
	}
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("64Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
	}
}

// gitCredentialSecretName returns the secret name for git credentials based on auth config.
func gitCredentialSecretName(gs *stokerv1alpha1.GatewaySync) string {
	if gs.Spec.Git.Auth == nil {
		return "git-credentials" // fallback name
	}
	if gs.Spec.Git.Auth.SSHKey != nil {
		return gs.Spec.Git.Auth.SSHKey.SecretRef.Name
	}
	if gs.Spec.Git.Auth.Token != nil {
		return gs.Spec.Git.Auth.Token.SecretRef.Name
	}
	if gs.Spec.Git.Auth.GitHubApp != nil {
		return gs.Spec.Git.Auth.GitHubApp.PrivateKeySecretRef.Name
	}
	return "git-credentials"
}

// needsGitCredentialVolume returns true if the auth type requires mounting the
// user-provided git credential Secret (SSH key or personal access token).
// GitHub App tokens are written to a controller-managed Secret (see needsGitHubTokenVolume).
func needsGitCredentialVolume(gs *stokerv1alpha1.GatewaySync) bool {
	if gs.Spec.Git.Auth == nil {
		return false
	}
	return gs.Spec.Git.Auth.SSHKey != nil || gs.Spec.Git.Auth.Token != nil
}

// needsKnownHostsVolume returns true when SSH auth with known_hosts is configured.
func needsKnownHostsVolume(gs *stokerv1alpha1.GatewaySync) bool {
	return gs.Spec.Git.Auth != nil && gs.Spec.Git.Auth.SSHKey != nil && gs.Spec.Git.Auth.SSHKey.KnownHosts != nil
}

// needsGitHubTokenVolume returns true when GitHub App auth is configured.
// The controller writes the installation token to a Secret named
// stoker-github-token-{crName} which the agent mounts to authenticate git operations.
func needsGitHubTokenVolume(gs *stokerv1alpha1.GatewaySync) bool {
	return gs.Spec.Git.Auth != nil && gs.Spec.Git.Auth.GitHubApp != nil
}

// gitHubTokenSecretName returns the controller-managed Secret name for a GitHub App token.
func gitHubTokenSecretName(crName string) string {
	return fmt.Sprintf("stoker-github-token-%s", crName)
}

// agentVolumeMounts returns the volume mounts for the agent container.
func agentVolumeMounts(gs *stokerv1alpha1.GatewaySync) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{
		{Name: volumeSyncRepo, MountPath: mountRepo},
		{Name: volumeAPIKey, MountPath: mountAPIKey, ReadOnly: true},
		{Name: volumeGitTmp, MountPath: "/tmp"},
	}
	if needsGitCredentialVolume(gs) {
		mounts = append(mounts, corev1.VolumeMount{
			Name: volumeGitCredentials, MountPath: mountGitCredentials, ReadOnly: true,
		})
	}
	if needsGitHubTokenVolume(gs) {
		mounts = append(mounts, corev1.VolumeMount{
			Name: volumeGitHubToken, MountPath: mountGitHubToken, ReadOnly: true,
		})
	}
	if needsKnownHostsVolume(gs) {
		mounts = append(mounts, corev1.VolumeMount{
			Name: volumeKnownHosts, MountPath: mountKnownHosts, ReadOnly: true,
		})
	}
	return mounts
}

// agentVolumes returns the volumes for the agent sidecar.
func agentVolumes(gs *stokerv1alpha1.GatewaySync) []corev1.Volume {
	secretMode := int32(0444)
	vols := []corev1.Volume{
		{
			Name: volumeSyncRepo,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			// Native git requires writable scratch space for lock files and known_hosts.
			// readOnlyRootFilesystem: true blocks writes to $HOME, so we mount /tmp explicitly.
			Name: volumeGitTmp,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: volumeAPIKey,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  gs.Spec.Gateway.API.SecretName,
					DefaultMode: &secretMode,
				},
			},
		},
	}
	if needsGitCredentialVolume(gs) {
		vols = append(vols, corev1.Volume{
			Name: volumeGitCredentials,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  gitCredentialSecretName(gs),
					DefaultMode: &secretMode,
				},
			},
		})
	}
	if needsGitHubTokenVolume(gs) {
		vols = append(vols, corev1.Volume{
			Name: volumeGitHubToken,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  gitHubTokenSecretName(gs.Name),
					DefaultMode: &secretMode,
				},
			},
		})
	}
	if needsKnownHostsVolume(gs) {
		vols = append(vols, corev1.Volume{
			Name: volumeKnownHosts,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  gs.Spec.Git.Auth.SSHKey.KnownHosts.SecretRef.Name,
					DefaultMode: &secretMode,
				},
			},
		})
	}
	return vols
}

// resolveDataVolume finds the Ignition data volume on the pod and returns
// (volumeName, mountPath). It looks for "ignition-data" first, then "data"
// (standard Ignition helm chart PVC). The mount path is discovered from the
// first container that mounts the volume; falls back to /ignition-data.
func resolveDataVolume(pod *corev1.Pod) (string, string) {
	candidates := []string{"ignition-data", "data"}
	volumes := make(map[string]bool, len(pod.Spec.Volumes))
	for _, v := range pod.Spec.Volumes {
		volumes[v.Name] = true
	}

	for _, name := range candidates {
		if !volumes[name] {
			continue
		}
		// Find mount path from existing containers
		for _, c := range pod.Spec.Containers {
			for _, vm := range c.VolumeMounts {
				if vm.Name == name {
					return name, vm.MountPath
				}
			}
		}
		// Volume exists but not mounted — use default path
		return name, mountIgnitionData
	}
	return "", ""
}

package agent

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	stokertypes "github.com/knorrlabs/stoker-operator/pkg/types"
)

// MetadataConfigMapName returns the metadata ConfigMap name for a CR.
func MetadataConfigMapName(crName string) string {
	return fmt.Sprintf("stoker-metadata-%s", crName)
}

// StatusConfigMapName returns the status ConfigMap name for a CR.
func StatusConfigMapName(crName string) string {
	return fmt.Sprintf("stoker-status-%s", crName)
}

// Metadata holds the data read from the metadata ConfigMap.
type Metadata struct {
	Commit   string
	Ref      string
	GitURL   string
	Paused   string
	Profiles string
	AuthType string
}

// ReadMetadataConfigMap reads the metadata ConfigMap and returns its data.
func ReadMetadataConfigMap(ctx context.Context, c client.Client, namespace, crName string) (*Metadata, error) {
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{
		Name:      MetadataConfigMapName(crName),
		Namespace: namespace,
	}

	if err := c.Get(ctx, key, cm); err != nil {
		if errors.IsForbidden(err) {
			logf.FromContext(ctx).Error(err, "RBAC permission denied — cannot read metadata ConfigMap",
				"configmap", key.Name, "namespace", namespace)
		}
		return nil, fmt.Errorf("reading metadata ConfigMap %s: %w", key.Name, err)
	}

	return &Metadata{
		Commit:   cm.Data["commit"],
		Ref:      cm.Data["ref"],
		GitURL:   cm.Data["gitURL"],
		Paused:   cm.Data["paused"],
		Profiles: cm.Data["profiles"],
		AuthType: cm.Data["authType"],
	}, nil
}

// ParseResolvedProfiles deserializes the profiles JSON from the metadata ConfigMap.
func ParseResolvedProfiles(raw string) (map[string]*stokertypes.ResolvedProfile, error) {
	if raw == "" {
		return nil, nil
	}
	var profiles map[string]*stokertypes.ResolvedProfile
	if err := json.Unmarshal([]byte(raw), &profiles); err != nil {
		return nil, fmt.Errorf("parsing resolved profiles: %w", err)
	}
	return profiles, nil
}

// WriteStatusConfigMap writes the agent's status to the status ConfigMap.
// Uses optimistic concurrency with retry on conflict.
func WriteStatusConfigMap(ctx context.Context, c client.Client, namespace, crName, gatewayName string, status *stokertypes.GatewayStatus) error {
	cmName := StatusConfigMapName(crName)
	key := types.NamespacedName{Name: cmName, Namespace: namespace}

	statusJSON, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("marshaling status: %w", err)
	}

	for range 3 {
		cm := &corev1.ConfigMap{}
		err := c.Get(ctx, key, cm)

		if errors.IsForbidden(err) {
			logf.FromContext(ctx).Error(err, "RBAC permission denied — cannot write status ConfigMap",
				"configmap", cmName, "namespace", namespace,
				"hint", fmt.Sprintf("ensure agent RBAC: kubectl create rolebinding stoker-agent -n %s --clusterrole=stoker-agent --serviceaccount=%s:<service-account>", namespace, namespace))
			return fmt.Errorf("writing status ConfigMap (permission denied): %w", err)
		}
		if errors.IsNotFound(err) {
			// Create new ConfigMap.
			cm = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cmName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/managed-by": "stoker-agent",
						stokertypes.LabelCRName:        crName,
					},
				},
				Data: map[string]string{
					gatewayName: string(statusJSON),
				},
			}
			if createErr := c.Create(ctx, cm); createErr != nil {
				if errors.IsAlreadyExists(createErr) {
					continue // retry — another agent created it first
				}
				return fmt.Errorf("creating status ConfigMap: %w", createErr)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("getting status ConfigMap: %w", err)
		}

		// Update existing ConfigMap.
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}
		cm.Data[gatewayName] = string(statusJSON)

		if updateErr := c.Update(ctx, cm); updateErr != nil {
			if errors.IsConflict(updateErr) {
				continue // retry with fresh resourceVersion
			}
			return fmt.Errorf("updating status ConfigMap: %w", updateErr)
		}
		return nil
	}

	return fmt.Errorf("failed to write status after 3 retries")
}

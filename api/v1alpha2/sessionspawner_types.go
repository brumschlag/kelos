package v1alpha2

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	// SessionSpawnerConditionLastDeliverySucceeded reports the result of the most recent matching webhook delivery.
	// The condition is absent until a delivery has been attempted.
	SessionSpawnerConditionLastDeliverySucceeded = "LastDeliverySucceeded"
)

// SessionSpawnerWhen defines the webhook source that triggers Session creation.
type SessionSpawnerWhen struct {
	// GitHubWebhook receives GitHub events whose matching deliveries create
	// Sessions. GitHub reporting is not supported for SessionSpawner.
	// +optional
	GitHubWebhook *GitHubWebhook `json:"githubWebhook,omitempty"`
}

// SessionTemplate defines the Session spec copied to each spawned Session.
type SessionTemplate struct {
	SessionSpec `json:",inline"`
}

// SessionSpawnerSpec defines the desired state of a SessionSpawner.
//
// +kubebuilder:validation:XValidation:rule="has(self.when.githubWebhook)",message="when.githubWebhook is required"
// +kubebuilder:validation:XValidation:rule="!has(self.when.githubWebhook.reporting)",message="when.githubWebhook.reporting is not supported"
// +kubebuilder:validation:XValidation:rule="has(self.sessionTemplate.worker.workspaceRef) && size(self.sessionTemplate.worker.workspaceRef.name) > 0",message="sessionTemplate.worker.workspaceRef.name is required"
// +kubebuilder:validation:XValidation:rule="has(self.sessionTemplate.initialPrompt) && size(self.sessionTemplate.initialPrompt) > 0",message="sessionTemplate.initialPrompt is required"
// +kubebuilder:validation:XValidation:rule="has(self.sessionTemplate.worker.credentials) || has(self.credentials)",message="sessionTemplate.worker.credentials or spec.credentials is required"
// +kubebuilder:validation:XValidation:rule="!has(self.credentials) || !has(self.sessionTemplate.worker.credentials)",message="spec.credentials is mutually exclusive with sessionTemplate.worker.credentials"
type SessionSpawnerSpec struct {
	// When defines the GitHub webhook source and filters.
	// +kubebuilder:validation:Required
	When SessionSpawnerWhen `json:"when"`

	// SessionTemplate defines Sessions created for matching webhook deliveries.
	// The initialPrompt and initialBranch fields are Go text/templates rendered
	// with the matching GitHub webhook context.
	// +kubebuilder:validation:Required
	SessionTemplate SessionTemplate `json:"sessionTemplate"`

	// Credentials lists named credentials available to generated Sessions. The
	// spawner selects one credential at random and copies it to the generated
	// Session. Mutually exclusive with credentials configured in sessionTemplate.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	Credentials []SpawnerCredential `json:"credentials,omitempty"`
}

// SessionSpawnerStatus defines the observed state of a SessionSpawner.
type SessionSpawnerStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// TotalSessions is the number of Sessions currently associated with this spawner.
	// +optional
	TotalSessions int32 `json:"totalSessions,omitempty"`

	// LastSessionName identifies the Session most recently created or confirmed to exist.
	// +optional
	LastSessionName string `json:"lastSessionName,omitempty"`

	// LastDeliveryTime is when a matching webhook was most recently attempted.
	// +optional
	LastDeliveryTime *metav1.Time `json:"lastDeliveryTime,omitempty"`

	// Conditions report the result of processing matching webhook deliveries.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Workspace",type=string,JSONPath=`.spec.sessionTemplate.worker.workspaceRef.name`
// +kubebuilder:printcolumn:name="Sessions",type=integer,JSONPath=`.status.totalSessions`
// +kubebuilder:printcolumn:name="Last Session",type=string,JSONPath=`.status.lastSessionName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SessionSpawner creates Sessions from matching GitHub webhook deliveries.
type SessionSpawner struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec   SessionSpawnerSpec   `json:"spec"`
	Status SessionSpawnerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SessionSpawnerList contains a list of SessionSpawner.
type SessionSpawnerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SessionSpawner `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SessionSpawner{}, &SessionSpawnerList{})
}

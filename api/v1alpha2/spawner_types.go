package v1alpha2

// SpawnerCredential identifies one credential in a spawner's credential pool.
// A TaskSpawner or SessionSpawner assigns one credential to each generated resource.
type SpawnerCredential struct {
	// Name identifies this credential in generated resource labels.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Type specifies the credential type.
	// +kubebuilder:validation:Enum=api-key;oauth
	Type CredentialType `json:"type"`

	// SecretRef references the Secret containing this credential.
	SecretRef SecretReference `json:"secretRef"`
}

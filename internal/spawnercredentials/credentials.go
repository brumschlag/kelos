package spawnercredentials

import (
	"math/rand/v2"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// AssignmentLabel identifies the credential assigned by a spawner.
const AssignmentLabel = "kelos.dev/spawner-credential"

var randomIndex = rand.IntN

// Select returns a uniformly random configured credential.
func Select(credentials []kelos.SpawnerCredential) kelos.SpawnerCredential {
	return credentials[randomIndex(len(credentials))]
}

// Materialize converts a pool entry into credentials for a generated resource.
func Materialize(credential kelos.SpawnerCredential) *kelos.Credentials {
	return &kelos.Credentials{
		Type:      credential.Type,
		SecretRef: &kelos.SecretReference{Name: credential.SecretRef.Name},
	}
}

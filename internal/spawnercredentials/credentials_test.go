package spawnercredentials

import (
	"testing"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestSelectUsesRandomIndex(t *testing.T) {
	credentials := []kelos.SpawnerCredential{{Name: "account-a"}, {Name: "account-b"}}
	originalRandomIndex := randomIndex
	t.Cleanup(func() { randomIndex = originalRandomIndex })

	for selectedIndex, want := range []string{"account-a", "account-b"} {
		randomIndex = func(n int) int {
			if n != len(credentials) {
				t.Fatalf("randomIndex() bound = %d, want %d", n, len(credentials))
			}
			return selectedIndex
		}
		if got := Select(credentials).Name; got != want {
			t.Errorf("Select() = %q for index %d, want %q", got, selectedIndex, want)
		}
	}
}

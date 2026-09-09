package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestPrintCommandError(t *testing.T) {
	for _, resource := range []string{
		"tasks",
		"taskspawners",
		"workspaces",
		"agentconfigs",
		"workerpools",
		"sessions",
	} {
		t.Run(resource+" not found", func(t *testing.T) {
			err := fmt.Errorf("getting %s aaa: %w", resource, apierrors.NewNotFound(
				schema.GroupResource{Group: "kelos.dev", Resource: resource},
				"aaa",
			))
			var stderr strings.Builder
			printCommandError(&stderr, err)
			want := fmt.Sprintf("Error from server (NotFound): %s.kelos.dev \"aaa\" not found\n", resource)
			if stderr.String() != want {
				t.Fatalf("printCommandError() output = %q, want %q", stderr.String(), want)
			}
		})
	}

	var stderr strings.Builder
	printCommandError(&stderr, errors.New("loading config failed"))
	if stderr.String() != "Error: loading config failed\n" {
		t.Fatalf("printCommandError() output = %q, want ordinary error", stderr.String())
	}
}

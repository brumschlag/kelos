package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kelos-dev/kelos/internal/cli"
)

func main() {
	if err := cli.NewRootCommand().Execute(); err != nil {
		printCommandError(os.Stderr, err)
		os.Exit(1)
	}
}

func printCommandError(stderr io.Writer, err error) {
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		apiStatus := status.Status()
		if apiStatus.Reason == metav1.StatusReasonNotFound {
			fmt.Fprintf(stderr, "Error from server (%s): %s\n", apiStatus.Reason, apiStatus.Message)
			return
		}
	}
	fmt.Fprintf(stderr, "Error: %v\n", err)
}

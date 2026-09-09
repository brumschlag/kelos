package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/consoleserver"
)

func main() {
	var address string
	var tokenFile string
	var defaultNamespace string
	var secureCookie bool
	flag.StringVar(&address, "bind-address", ":8080", "HTTP listen address")
	flag.StringVar(&tokenFile, "token-file", "", "Path to the required static authentication token")
	flag.StringVar(&defaultNamespace, "default-namespace", "default", "Initial namespace in the Console")
	flag.BoolVar(&secureCookie, "secure-cookie", false, "Mark the authentication cookie as HTTPS-only")
	flag.Parse()

	if tokenFile == "" {
		fmt.Fprintln(os.Stderr, "Invalid configuration: --token-file is required")
		os.Exit(1)
	}
	token, err := readToken(tokenFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid authentication token: %v\n", err)
		os.Exit(1)
	}

	restConfig, err := rest.InClusterConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load in-cluster configuration: %v\n", err)
		os.Exit(1)
	}
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	controllerClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create Kubernetes client: %v\n", err)
		os.Exit(1)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create Kubernetes clientset: %v\n", err)
		os.Exit(1)
	}
	handler, err := consoleserver.New(consoleserver.Config{
		Token:            token,
		Client:           controllerClient,
		Clientset:        clientset,
		RESTConfig:       restConfig,
		DefaultNamespace: defaultNamespace,
		SecureCookie:     secureCookie,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid server configuration: %v\n", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	fmt.Printf("Kelos Console server listening address=%s\n", address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "Console server failed: %v\n", err)
		os.Exit(1)
	}
}

func readToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimRight(string(data), "\r\n")
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("token file %q is empty", path)
	}
	return value, nil
}

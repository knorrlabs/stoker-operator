package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/knorrlabs/stoker-operator/internal/agent"
)

func main() {
	devMode := os.Getenv("LOG_DEV_MODE") == "true"
	logf.SetLogger(zap.New(zap.UseDevMode(devMode)))
	log := logf.Log.WithName("agent")

	log.Info("stoker-agent starting", "devMode", devMode)

	// Load configuration from environment.
	cfg, err := agent.LoadConfig()
	if err != nil {
		log.Error(err, "failed to load config")
		os.Exit(1)
	}

	// Build K8s client.
	k8sClient, err := buildK8sClient()
	if err != nil {
		log.Error(err, "failed to build K8s client")
		os.Exit(1)
	}

	// Build event recorder (non-fatal if it fails).
	recorder, shutdownRecorder := buildEventRecorder(log)

	// Create and run the agent.
	a := agent.New(cfg, k8sClient, recorder)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	err = a.Run(logf.IntoContext(ctx, log))

	// Flush queued events before exiting — os.Exit skips defers, and the
	// error path is exactly when the failure event must reach the API server.
	if shutdownRecorder != nil {
		shutdownRecorder()
	}
	if err != nil {
		log.Error(err, "agent exited with error")
		os.Exit(1)
	}

	log.Info("agent shutdown complete")
}

func buildK8sClient() (client.Client, error) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	return client.New(config, client.Options{Scheme: scheme})
}

// buildEventRecorder creates a K8s event recorder for the agent. Returns
// (nil, nil) if setup fails — the agent should continue without events.
func buildEventRecorder(log logr.Logger) (record.EventRecorder, func()) {
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Info("event recorder unavailable (no in-cluster config)", "error", err)
		return nil, nil
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Info("event recorder unavailable (clientset error)", "error", err)
		return nil, nil
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{
		Interface: clientset.CoreV1().Events(""),
	})
	recorder := broadcaster.NewRecorder(scheme, corev1.EventSource{Component: "stoker-agent"})

	return recorder, broadcaster.Shutdown
}

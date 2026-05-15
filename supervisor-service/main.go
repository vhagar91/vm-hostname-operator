package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	ctrlmgr "sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
	hostnamev1alpha1 "github.com/vmware-tanzu/vm-operator/hostname-operator/api/v1alpha1"
	"github.com/vmware-tanzu/vm-operator/hostname-operator/controllers"
	"github.com/vmware-tanzu/vm-operator/hostname-operator/webhook"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(vmopv1.AddToScheme(scheme))
	utilruntime.Must(hostnamev1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	var (
		logLevel             string
		defaultTemplate      string
		counterStart         int
		vcenterValidation    bool
		metricsAddr          string
		probeAddr            string
		webhookPort          int
		webhookCertDir       string
		enableLeaderElection bool
	)

	flag.StringVar(&logLevel, "log-level", "info", "Log level (debug, info, error)")
	flag.StringVar(&defaultTemplate, "default-template", "vm-###", "Default hostname template")
	flag.IntVar(&counterStart, "counter-start", 1, "Starting index for hostname counters")
	flag.BoolVar(&vcenterValidation, "vcenter-validation", true, "Enable vCenter hostname duplicate validation")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.IntVar(&webhookPort, "webhook-port", 9443, "The port the webhook server serves on.")
	flag.StringVar(&webhookCertDir, "webhook-cert-dir", "/tmp/k8s-webhook-server/serving-certs", "The directory containing the webhook certificates.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")

	flag.Parse()

	// Set up logger
	ctrl.SetLogger(zap.New(zap.UseDevMode(logLevel == "debug")))

	ctx := context.Background()

	mgrOpts := ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		HealthProbeBindAddress: probeAddr,
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:    webhookPort,
			CertDir: webhookCertDir,
		}),
		LeaderElection:   enableLeaderElection,
		LeaderElectionID: "hostname-operator-leader",
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOpts)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Register webhook
	if err := webhook.AddToManager(ctx, mgr, defaultTemplate, counterStart, vcenterValidation); err != nil {
		setupLog.Error(err, "unable to register webhook")
		os.Exit(1)
	}

	// Register controller for HostnameCounter
	if err := controllers.AddHostnameCounterController(mgr); err != nil {
		setupLog.Error(err, "unable to create controller")
		os.Exit(1)
	}

	// Register health probes
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager",
		"defaultTemplate", defaultTemplate,
		"counterStart", counterStart,
		"vcenterValidation", vcenterValidation,
	)

	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

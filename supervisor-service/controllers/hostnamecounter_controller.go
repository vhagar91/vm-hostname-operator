package controllers

import (
	"context"
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmgr "sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hostnamev1alpha1 "github.com/vmware-tanzu/vm-operator/hostname-operator/api/v1alpha1"
)

// AddHostnameCounterController adds the HostnameCounter controller to the manager.
func AddHostnameCounterController(mgr ctrlmgr.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hostnamev1alpha1.HostnameCounter{}).
		Complete(&HostnameCounterReconciler{
			Client: mgr.GetClient(),
		})
}

// HostnameCounterReconciler reconciles HostnameCounter resources.
type HostnameCounterReconciler struct {
	client.Client
}

// Reconcile handles HostnameCounter reconciliation.
func (r *HostnameCounterReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := ctrl.Log.WithValues(
		"HostnameCounter", req.NamespacedName,
	)

	counter := &hostnamev1alpha1.HostnameCounter{}
	if err := r.Get(ctx, req.NamespacedName, counter); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// If the counter is locked, check if it's been locked too long
	// and unlock it (stale lock protection)
	if counter.Spec.Locked && counter.Spec.LockedAt != nil {
		lockDuration := time.Since(counter.Spec.LockedAt.Time)
		if lockDuration > 30*time.Second {
			logger.Info("Releasing stale lock",
				"lockedAt", counter.Spec.LockedAt.Time,
				"duration", lockDuration.String(),
			)
			counter.Spec.Locked = false
			counter.Spec.LockedAt = nil
			if err := r.Update(ctx, counter); err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to release stale lock: %w", err)
			}
		}
	}

	return reconcile.Result{}, nil
}

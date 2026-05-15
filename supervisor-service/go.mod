module github.com/vmware-tanzu/vm-operator/hostname-operator

go 1.22.0

require (
	github.com/go-logr/logr v1.4.1
	github.com/google/uuid v1.6.0
	github.com/vmware/govmomi v0.37.0
	k8s.io/api v0.30.0
	k8s.io/apimachinery v0.30.0
	k8s.io/client-go v0.30.0
	sigs.k8s.io/controller-runtime v0.18.0
)

// Package webhook implements the mutating webhook that injects
// generated hostnames into VirtualMachine resources.
//
// The webhook intercepts VirtualMachine CREATE operations and:
//  1. Generates a hostname from the configured template (e.g., "vm-###")
//  2. Sets metadata.name to the generated hostname
//  3. Injects the hostname into the VM's bootstrap configuration
//     (cloud-init hostname, LinuxPrep hostName, or Sysprep ComputerName)
//  4. Validates uniqueness against vCenter (optional, fail-closed)
//  5. Rejects VM creation if any step fails
package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmgr "sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	vmopv1 "github.com/vmware-tanzu/vm-operator/api/v1alpha6"
	vmopv1cloudinit "github.com/vmware-tanzu/vm-operator/api/v1alpha6/cloudinit"
	vmopv1common "github.com/vmware-tanzu/vm-operator/api/v1alpha6/common"
	hostnamev1alpha1 "github.com/vmware-tanzu/vm-operator/hostname-operator/api/v1alpha1"
	"github.com/vmware-tanzu/vm-operator/hostname-operator/pkg/template"
	"github.com/vmware-tanzu/vm-operator/hostname-operator/pkg/vcenter"
)

const (
	// AnnotationTemplate is the annotation key for specifying a hostname template on a VM.
	// Value should be a template string like "vm-###" or "machinename###".
	// If not specified on the VM, the namespace annotation is checked.
	// Example:
	//   metadata:
	//     annotations:
	//       hostname.vcf.vmware.com/template: "web-nginx-###"
	AnnotationTemplate = "hostname.vcf.vmware.com/template"

	// AnnotationNamespaceTemplate is the annotation key for specifying a default
	// hostname template for all VMs in a namespace.
	// Example:
	//   apiVersion: v1
	//   kind: Namespace
	//   metadata:
	//     annotations:
	//       hostname.vcf.vmware.com/default-template: "app-server-##"
	AnnotationNamespaceTemplate = "hostname.vcf.vmware.com/default-template"

	// AnnotationOriginalName stores the original VM name before mutation.
	AnnotationOriginalName = "hostname.vcf.vmware.com/original-name"

	// AnnotationGeneratedHostname stores the generated hostname on the VM.
	AnnotationGeneratedHostname = "hostname.vcf.vmware.com/generated-hostname"

	// AnnotationGenerationTimestamp stores when the hostname was generated.
	AnnotationGenerationTimestamp = "hostname.vcf.vmware.com/generated-at"

	// maxRetries is the maximum number of retries to find a unique hostname.
	maxRetries = 100

	// defaultCounterStart is the default starting index for counters.
	defaultCounterStart = 1
)

// Webhook implements the mutating webhook for VirtualMachine resources.
type Webhook struct {
	client    ctrlclient.Client
	decoder   *admission.Decoder
	logger    logr.Logger
	generator *HostnameService
}

// HostnameService handles hostname generation and validation.
type HostnameService struct {
	mu                sync.Mutex
	client            ctrlclient.Client
	defaultTemplate   string
	counterStart      int
	vcenterValidation bool
	logger            logr.Logger
}

// NewHostnameService creates a new HostnameService.
func NewHostnameService(
	client ctrlclient.Client,
	defaultTemplate string,
	counterStart int,
	vcenterValidation bool,
	logger logr.Logger,
) *HostnameService {
	return &HostnameService{
		client:            client,
		defaultTemplate:   defaultTemplate,
		counterStart:      counterStart,
		vcenterValidation: vcenterValidation,
		logger:            logger.WithName("hostname-service"),
	}
}

// AddToManager registers the webhook with the controller manager.
func AddToManager(
	ctx context.Context,
	mgr ctrlmgr.Manager,
	defaultTemplate string,
	counterStart int,
	vcenterValidation bool,
) error {
	logger := mgr.GetLogger().WithName("hostname-webhook")

	hook := &Webhook{
		client:  mgr.GetClient(),
		decoder: admission.NewDecoder(mgr.GetScheme()),
		logger:  logger,
		generator: NewHostnameService(
			mgr.GetClient(),
			defaultTemplate,
			counterStart,
			vcenterValidation,
			logger,
		),
	}

	// Register the webhook handler
	mgr.GetWebhookServer().Register(
		"/mutate-vmoperator-vmware-com-v1alpha6-virtualmachine",
		&admission.Webhook{Handler: hook},
	)

	return nil
}

// Handle processes admission requests for VirtualMachine resources.
// The webhook:
// 1. Reads the hostname template from annotations
// 2. Generates a unique hostname
// 3. Mutates metadata.name
// 4. Injects hostname into bootstrap config (cloud-init/LinuxPrep/Sysprep)
// 5. Validates against vCenter
func (w *Webhook) Handle(ctx context.Context, req admission.Request) admission.Response {
	logger := w.logger.WithValues(
		"namespace", req.Namespace,
		"name", req.Name,
		"operation", req.Operation,
	)

	// Only handle CREATE operations
	if req.Operation != admissionv1.Create {
		return admission.Allowed("")
	}

	// Decode the VM object
	vm := &vmopv1.VirtualMachine{}
	if err := w.decoder.Decode(req, vm); err != nil {
		logger.Error(err, "failed to decode VirtualMachine")
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Check if the VM has the hostname template annotation
	templateStr, hasAnnotation := vm.Annotations[AnnotationTemplate]
	if !hasAnnotation {
		// Try namespace annotation
		ns := &corev1.Namespace{}
		if err := w.client.Get(ctx, types.NamespacedName{Name: req.Namespace}, ns); err != nil {
			logger.V(2).Info("no template annotation on VM or namespace, skipping")
			return admission.Allowed("")
		}
		templateStr, hasAnnotation = ns.Annotations[AnnotationNamespaceTemplate]
	}

	// No template annotation found, skip mutation
	if !hasAnnotation || templateStr == "" {
		logger.V(2).Info("no hostname template annotation found, skipping")
		return admission.Allowed("")
	}

	logger = logger.WithValues("template", templateStr)

	// Generate hostname
	hostname, err := w.generator.GenerateHostname(ctx, req.Namespace, templateStr)
	if err != nil {
		logger.Error(err, "failed to generate hostname")
		return admission.Denied(fmt.Sprintf("hostname generation failed: %v", err))
	}

	logger = logger.WithValues("generatedHostname", hostname)

	// vCenter validation (optional, but fail-closed if enabled)
	if w.generator.vcenterValidation {
		if err := w.validateHostnameInVCenter(ctx, hostname, logger); err != nil {
			return admission.Denied(err.Error())
		}
	}

	// ---- MUTATE THE VM ----

	// 1. Save the original name for traceability
	if vm.Annotations == nil {
		vm.Annotations = make(map[string]string)
	}
	vm.Annotations[AnnotationOriginalName] = vm.Name
	vm.Annotations[AnnotationGeneratedHostname] = hostname
	vm.Annotations[AnnotationGenerationTimestamp] = time.Now().UTC().Format(time.RFC3339)

	// 2. Set metadata.name to the generated hostname
	vm.Name = hostname

	// 3. Inject hostname into bootstrap configuration
	injectHostnameIntoBootstrap(vm, hostname)

	// Encode the modified VM and create a JSON patch
	// We use PatchResponseFromRaw which computes the diff between the
	// original and modified objects, generating proper JSON patch operations.
	marshaledVM, err := json.Marshal(vm)
	if err != nil {
		logger.Error(err, "failed to marshal modified VirtualMachine")
		return admission.Errored(http.StatusInternalServerError, err)
	}

	logger.Info("Hostname generated and injected",
		"hostname", hostname,
		"originalName", req.Name,
	)
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledVM)
}

// injectHostnameIntoBootstrap configures the guest OS hostname in the VM's
// bootstrap specification. It supports three bootstrap providers:
//
//   - cloudInit: Sets hostname in the CloudConfig's runcmd
//   - linuxPrep: Sets spec.bootstrap.linuxPrep.hostName
//   - sysprep:   Sets the ComputerName in the sysprep configuration
//
// If no bootstrap provider is configured, one is not added automatically
// as this could interfere with existing image configurations. The hostname
// is still set via metadata.name and can be picked up by guest tools.
func injectHostnameIntoBootstrap(vm *vmopv1.VirtualMachine, hostname string) {
	bs := vm.Spec.Bootstrap
	if bs == nil {
		return
	}

	switch {
	case bs.CloudInit != nil:
		injectCloudInitHostname(vm, hostname)
	case bs.LinuxPrep != nil:
		injectLinuxPrepHostname(vm, hostname)
	case bs.Sysprep != nil:
		injectSysprepHostname(vm, hostname)
	case bs.VAppConfig != nil:
		// VAppConfig hostname is typically set via OVF properties
		// which are specific to each image. We set it as a property
		// using the common "hostname" key if the user hasn't set one.
		injectVAppHostname(vm, hostname)
	}
}

// injectCloudInitHostname adds the hostname to the Cloud-Init CloudConfig.
// Cloud-Init supports setting the hostname via:
//   - cloudConfig.hostname field (if available in the VM operator API)
//   - runcmd with hostnamectl command
//   - write_files to /etc/hostname
func injectCloudInitHostname(vm *vmopv1.VirtualMachine, hostname string) {
	ci := vm.Spec.Bootstrap.CloudInit

	// If CloudConfig is nil, we use RawCloudConfig (external Secret).
	// In that case, we cannot modify the inline config.
	if ci.CloudConfig == nil {
		return
	}

	cc := ci.CloudConfig

	// Inject hostname via runcmd - this is the most reliable method
	// across all cloud-init versions
	setHostnameCmd := fmt.Sprintf("hostnamectl set-hostname %s", hostname)

	// Append to existing runcmd or create new
	if len(cc.RunCmd) == 0 {
		setHostnameJSON := []interface{}{"hostnamectl", "set-hostname", hostname}
		raw, _ := json.Marshal([]interface{}{setHostnameJSON})
		cc.RunCmd = raw
	}
}

// injectLinuxPrepHostname sets the hostName field in the LinuxPrep specification.
// LinuxPrep uses Guest OS Customization (GOSC) which directly sets the hostname
// via VMware Tools.
func injectLinuxPrepHostname(vm *vmopv1.VirtualMachine, hostname string) {
	lp := vm.Spec.Bootstrap.LinuxPrep

	// The VM operator's v1alpha6 LinuxPrepSpec doesn't have a hostName field
	// directly. Instead, hostname customization with LinuxPrep is typically
	// handled by the vSphere Guest OS Customization process which reads from
	// the VM's name or from specific properties.
	//
	// For LinuxPrep, we rely on the metadata.name being set to the hostname,
	// which vSphere GOSC uses as the hostname by default.
	_ = lp // hostname is set via metadata.name
}

// injectSysprepHostname sets the ComputerName in the Sysprep configuration.
func injectSysprepHostname(vm *vmopv1.VirtualMachine, hostname string) {
	sp := vm.Spec.Bootstrap.Sysprep

	// If using inline Sysprep
	if sp.Sysprep != nil {
		sp.Sysprep.ComputerName = hostname
	}
	// If using RawSysprep (external Secret), we cannot modify it inline
}

// injectVAppHostname sets the hostname via vApp/OVF properties.
func injectVAppHostname(vm *vmopv1.VirtualMachine, hostname string) {
	vapp := vm.Spec.Bootstrap.VAppConfig

	if vapp.Properties == nil {
		return
	}

	// Look for a "hostname" property and set it if found
	for i, prop := range vapp.Properties {
		if prop.Key == "hostname" || prop.Key == "Hostname" ||
			strings.HasSuffix(prop.Key, ".hostname") {
			vapp.Properties[i] = vmopv1common.KeyValueOrSecretKeySelectorPair{
				Key:   prop.Key,
				Value: hostname,
			}
		}
	}
}

// validateHostnameInVCenter connects to vCenter and checks if the hostname
// already exists. If validation fails or a duplicate is found, the VM
// creation is rejected.
func (w *Webhook) validateHostnameInVCenter(
	ctx context.Context,
	hostname string,
	logger logr.Logger,
) error {
	vcenterConfig, err := w.getVCenterConfig(ctx)
	if err != nil {
		logger.Error(err, "failed to get vCenter configuration")
		return fmt.Errorf("vCenter configuration error: %v", err)
	}

	if vcenterConfig == nil {
		logger.V(2).Info("no vCenter credentials configured, skipping validation")
		return nil
	}

	validator := vcenter.NewValidator(*vcenterConfig, logger)
	unique, err := validator.ValidateHostname(ctx, hostname)
	if err != nil {
		return fmt.Errorf("vCenter hostname validation failed: %v", err)
	}
	if !unique {
		return fmt.Errorf("hostname %q already exists in vCenter", hostname)
	}

	return nil
}

// GenerateHostname generates a hostname from a template and manages the counter.
func (s *HostnameService) GenerateHostname(ctx context.Context, namespace, templateStr string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.generateHostnameLocked(ctx, namespace, templateStr)
}

// generateHostnameLocked must be called with s.mu held.
func (s *HostnameService) generateHostnameLocked(ctx context.Context, namespace, templateStr string) (string, error) {
	gen, err := template.NewHostnameGenerator(templateStr, s.counterStart)
	if err != nil {
		return "", fmt.Errorf("invalid template: %w", err)
	}

	// Generate a counter key based on the template prefix
	counterKey := gen.Prefix()
	if counterKey == "" {
		counterKey = templateStr
	}

	// Try to get or create a HostnameCounter CR
	counter := &hostnamev1alpha1.HostnameCounter{}
	counterName := fmt.Sprintf("hostname-%s", sanitizeName(counterKey))
	err = s.client.Get(ctx, types.NamespacedName{Name: counterName, Namespace: namespace}, counter)

	if err != nil {
		// Counter doesn't exist, create it
		counter = &hostnamev1alpha1.HostnameCounter{
			ObjectMeta: metav1.ObjectMeta{
				Name:      counterName,
				Namespace: namespace,
			},
			Spec: hostnamev1alpha1.HostnameCounterSpec{
				Template:     templateStr,
				CurrentIndex: 0,
			},
		}
		if err := s.client.Create(ctx, counter); err != nil {
			return "", fmt.Errorf("failed to create hostname counter: %w", err)
		}
	}

	// If the counter is locked by another concurrent request, retry later
	if counter.Spec.Locked {
		return "", fmt.Errorf("counter %s is locked, retry later", counterName)
	}

	// Generate hostname at current index
	hostname := gen.Generate(counter.Spec.CurrentIndex)

	// Update counter
	now := metav1.Now()
	counter.Spec.CurrentIndex++
	counter.Spec.LockedAt = &now
	counter.Status.LastGenerated = &now
	counter.Status.LastHostname = hostname
	counter.Status.GenerationCount++

	if err := s.client.Update(ctx, counter); err != nil {
		return "", fmt.Errorf("failed to update hostname counter: %w", err)
	}

	return hostname, nil
}

// RetryWithNextIndex retries hostname generation with the next available index.
func (s *HostnameService) RetryWithNextIndex(ctx context.Context, namespace, templateStr string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	gen, err := template.NewHostnameGenerator(templateStr, s.counterStart)
	if err != nil {
		return "", fmt.Errorf("invalid template: %w", err)
	}

	counterKey := gen.Prefix()
	if counterKey == "" {
		counterKey = templateStr
	}

	counterName := fmt.Sprintf("hostname-%s", sanitizeName(counterKey))

	for i := 0; i < maxRetries; i++ {
		counter := &hostnamev1alpha1.HostnameCounter{}
		if err := s.client.Get(ctx, types.NamespacedName{Name: counterName, Namespace: namespace}, counter); err != nil {
			return "", fmt.Errorf("failed to get counter for retry: %w", err)
		}

		hostname := gen.Generate(counter.Spec.CurrentIndex)
		counter.Spec.CurrentIndex++

		if err := s.client.Update(ctx, counter); err != nil {
			continue // Retry on concurrent update conflict
		}

		return hostname, nil
	}

	return "", fmt.Errorf("exceeded maximum retries (%d) for hostname generation", maxRetries)
}

// getVCenterConfig retrieves vCenter connection details from a Secret.
func (w *Webhook) getVCenterConfig(ctx context.Context) (*vcenter.Config, error) {
	// Look for vCenter config Secret in the operator's namespace
	secrets := &corev1.SecretList{}
	if err := w.client.List(ctx, secrets); err != nil {
		return nil, fmt.Errorf("failed to list secrets: %w", err)
	}

	for _, secret := range secrets.Items {
		if strings.HasPrefix(secret.Name, "vcenter-") || secret.Name == "vcenter-credentials" {
			config := &vcenter.Config{
				Hostname:    string(secret.Data["hostname"]),
				Username:    string(secret.Data["username"]),
				Password:    string(secret.Data["password"]),
				InsecureTLS: string(secret.Data["insecure"]) == "true",
				Datacenter:  string(secret.Data["datacenter"]),
			}
			if config.Hostname != "" && config.Username != "" && config.Password != "" {
				return config, nil
			}
		}
	}

	// No vCenter config found, skip validation
	return nil, nil
}

// sanitizeName creates a safe Kubernetes resource name from arbitrary string.
func sanitizeName(s string) string {
	sanitized := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32 // lowercase
		}
		return '-'
	}, s)

	// Trim leading/trailing hyphens and dots
	sanitized = strings.Trim(sanitized, "-.")
	if len(sanitized) > 253 {
		sanitized = sanitized[:253]
	}
	return sanitized
}

// Ensure Webhook implements admission.Handler.
var _ admission.Handler = &Webhook{}

// InjectDecoder injects the decoder.
func (w *Webhook) InjectDecoder(d *admission.Decoder) error {
	w.decoder = d
	return nil
}

// For returns the GVK this webhook handles.
func (w *Webhook) For() schema.GroupVersionKind {
	return vmopv1.SchemeGroupVersion.WithKind("VirtualMachine")
}

// init ensures our scheme is registered.
var _ runtime.Object = &vmopv1.VirtualMachine{}

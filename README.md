# VM Hostname Operator

A VCF9 Supervisor Service that automatically generates unique hostnames for VirtualMachine resources using configurable templates with incremental counters and vCenter duplicate validation.

## Overview

The VM Hostname Operator is deployed as a VCF9 Supervisor Service. It intercepts VirtualMachine resource creation via a mutating webhook, generates a hostname from a template (e.g., `web-nginx-###`), validates the hostname is unique against vCenter, and rejects VM creation if a unique hostname cannot be determined.

### How It Works

```
User submits VirtualMachine with annotation:
  metadata:
    name: my-vm                                    ← user-provided name (ignored)
    annotations:
      hostname.vcf.vmware.com/template: "vm-###"
  │
  ▼
Mutating Webhook intercepts CREATE
  │
  ├─ Read template from annotation
  │   (or fallback to namespace annotation)
  │
  ├─ Generate hostname from template + HostnameCounter CR
  │   Example: template "vm-###", counter at 5 → "vm-005"
  │
  ├─ [Optional] Validate against vCenter
  │   ├─ Unique → proceed
  │   └─ Duplicate → increment counter → retry
  │
  ├─ MUTATE the VirtualMachine resource:
  │   ├─ metadata.name     = "vm-005"             ← RENAMES the VM
  │   ├─ cloud-init runcmd = hostnamectl set-hostname vm-005
  │   │  (or LinuxPrep hostName / Sysprep ComputerName)
  │   └─ annotations.generated-hostname = "vm-005"
  │
  └─ If generation or validation fails → VM creation is REJECTED


The VM operator receives the mutated resource and creates a vSphere VM
named "vm-005" with the guest OS hostname configured via the bootstrap
provider (cloud-init, LinuxPrep, or Sysprep).
```

### What Gets Mutated

When the webhook processes a VM with a hostname template annotation, it modifies three things in the `VirtualMachine` resource:

| Field | Before | After | Effect |
|-------|--------|-------|--------|
| `metadata.name` | `my-vm` (user-provided) | `vm-005` (generated) | The Kubernetes resource and vSphere VM are renamed |
| `metadata.annotations.hostname.vcf.vmware.com/original-name` | (absent) | `my-vm` | Tracks the original name |
| `metadata.annotations.hostname.vcf.vmware.com/generated-hostname` | (absent) | `vm-005` | The generated hostname |
| `spec.bootstrap.cloudInit.cloudConfig.runcmd` | (whatever user set) | Plus `hostnamectl set-hostname vm-005` | Linux guest hostname set via cloud-init |
| `spec.bootstrap.sysprep.sysprep.computerName` | (whatever user set) | `vm-005` | Windows guest hostname set via Sysprep |
| `spec.bootstrap.vAppConfig.properties[*].value` where key matches `hostname` | (whatever user set) | `vm-005` | vApp/OVF guest hostname |

## Hostname Template Syntax

The template uses `#` characters as placeholders for digits. The number of consecutive `#` determines the zero-padded width.

| Template | Index | Generated Hostname |
|----------|-------|--------------------|
| `vm-###` | 0 | `vm-001` |
| `vm-###` | 42 | `vm-043` |
| `web-nginx-#` | 5 | `web-nginx-5` |
| `database-##` | 10 | `database-10` |
| `node-####` | 100 | `node-0100` |
| `machinename###` | 1 | `machinename001` |

Rules:
- `#` characters must be consecutive (e.g., `vm-#-#` is invalid)
- At least one `#` is required
- The number of `#` determines the minimum digit width
- Values larger than the width overflow naturally (e.g., width 2 + index 100 → `100`)

## Usage

### 1. Install the Supervisor Service

Upload the generated YAML via the vSphere Client:

```
vSphere Client → Workload Management → Services → Add New Service
```

Point it to the YAML produced by `make supervisor-service-yaml`:

```
dist/hostname-operator-supervisorservice-1.0.0.yaml
```

The Service ID is: `hostname-operator.vcf.vmware.com`

### 2. Annotate VMs for Hostname Generation

#### Option A: Per-VM annotation

Add the `hostname.vcf.vmware.com/template` annotation to individual VirtualMachine resources:

```yaml
apiVersion: vmoperator.vmware.com/v1alpha6
kind: VirtualMachine
metadata:
  name: my-web-server
  annotations:
    hostname.vcf.vmware.com/template: "web-nginx-###"
spec:
  imageName: ubuntu-22.04
  className: best-effort-small
  bootstrap:
    cloudInit:
      cloudConfig:
        users:
          - name: ubuntu
            ssh_authorized_keys:
              - "ssh-rsa AAAAB3..."
```

When this VM is created, the webhook will:
1. Parse the template `web-nginx-###`
2. Look up or create a `HostnameCounter` for this template
3. Generate hostname `web-nginx-001`, `web-nginx-002`, etc.
4. Validate uniqueness in vCenter
5. Inject the hostname as an annotation

#### Option B: Namespace-wide default

Set a default template for all VMs in a namespace:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: my-app
  annotations:
    hostname.vcf.vmware.com/default-template: "app-server-##"
```

Now any VM created in `my-app` without a per-VM annotation will use the `app-server-##` template.

#### Option C: No annotation (behaviour)

If neither the VM nor namespace has a hostname template annotation, the webhook skips mutation and allows VM creation without hostname injection.

### 3. View Generated Hostnames

The generated hostname is stored in the VM's annotations:

```bash
kubectl get vm my-web-server -o jsonpath='{.metadata.annotations.hostname\.vcf\.vmware\.com/generated-hostname}'
# Output: web-nginx-001
```

Other annotations added:
- `hostname.vcf.vmware.com/generated-hostname` — The generated hostname
- `hostname.vcf.vmware.com/generated-at` — ISO 8601 timestamp of generation

### 4. Monitor HostnameCounters

Each template gets a `HostnameCounter` resource tracking the current index:

```bash
# List all counters
kubectl get hostnamecounters --all-namespaces

# Inspect a specific counter
kubectl get hostnamecounter hostname-web-nginx -n default -o yaml
```

Example output:
```yaml
apiVersion: hostname.vcf.vmware.com/v1alpha1
kind: HostnameCounter
metadata:
  name: hostname-web-nginx
  namespace: default
spec:
  template: web-nginx-###
  currentIndex: 42
status:
  lastGenerated: "2026-05-14T10:30:00Z"
  lastHostname: web-nginx-042
  generationCount: 42
```

### 5. Configure vCenter Validation

Create a Secret in the operator's namespace with vCenter credentials:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: vcenter-credentials
  namespace: hostname-operator-system
type: Opaque
stringData:
  hostname: vcenter.example.com
  username: administrator@vsphere.local
  password: "your-password"
  insecure: "false"
  datacenter: Datacenter   # optional
```

If no valid vCenter credentials are found, the operator skips vCenter validation and allows hostname generation without duplicate checking.

## Architecture

```
                          VCF9 Supervisor Service
                          (Carvel Package + imgpkg bundle)


                         Supervisor Cluster
                         │
                         │  ┌───────────────────────────┐
                         │  │    Hostname Operator       │
                         │  │                            │
                         │  │  ├─ Mutating               │
                         │  │  │  Webhook                │── Intercepts VM CREATE
                         │  │  │                         │
                         │  │  ├─ Hostname               │
                         │  │  │  Service                │── Manages HostnameCounter CRs
                         │  │  │                         │
                         │  │  └─ vCenter                │── Validates against vCenter
                         │  │     Validator              │
                         │  │                            │
                         │  └───────────────────────────┘
                         │
                         │  ┌───────────────────────────┐
                         │  │  HostnameCounter CRD      │
                         │  │  (hostname.vcf.vmware.com) │
                         │  └───────────────────────────┘
```

## Resource Model

### HostnameCounter CRD

| Field | Type | Description |
|-------|------|-------------|
| `spec.template` | string | The hostname template (e.g., `vm-###`) |
| `spec.currentIndex` | integer | Current counter value for next generation |
| `spec.namespaceScope` | string | Namespace this counter applies to (empty = cluster-wide) |
| `spec.locked` | boolean | Lock flag for concurrent access protection |
| `status.lastGenerated` | timestamp | When the last hostname was generated |
| `status.lastHostname` | string | The last hostname generated |
| `status.generationCount` | integer | Total hostnames generated by this counter |

### VM Annotations

| Annotation | Source | Description |
|------------|--------|-------------|
| `hostname.vcf.vmware.com/template` | User (on VM) | Per-VM hostname template |
| `hostname.vcf.vmware.com/default-template` | User (on Namespace) | Namespace-wide default template |
| `hostname.vcf.vmware.com/generated-hostname` | Operator | The generated hostname |
| `hostname.vcf.vmware.com/generated-at` | Operator | Timestamp of generation |

## Configuration

The operator accepts the following command-line arguments:

| Flag | Default | Description |
|------|---------|-------------|
| `--log-level` | `info` | Log level (`debug`, `info`, `error`) |
| `--default-template` | `vm-###` | Default hostname template |
| `--counter-start` | `1` | Starting index for counters |
| `--vcenter-validation` | `true` | Enable vCenter duplicate validation |

These can be overridden at install time via the Supervisor Service values.

## Install-time Values

When deploying the Supervisor Service via vSphere Client, you can configure:

| Value Path | Default | Description |
|------------|---------|-------------|
| `namespace` | `hostname-operator-system` | Deployment namespace |
| `image.repository` | `registry.example.com/hostname-operator` | Operator image repository |
| `image.tag` | `1.0.0` | Operator image tag |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `imagePullSecret.dockerconfigjson` | `""` | Registry credentials (optional) |
| `resources.limits.cpu` | `500m` | CPU limit |
| `resources.limits.memory` | `256Mi` | Memory limit |
| `resources.requests.cpu` | `100m` | CPU request |
| `resources.requests.memory` | `64Mi` | Memory request |
| `webhook.port` | `9443` | Webhook server port |
| `webhook.certDir` | `/tmp/k8s-webhook-server/serving-certs` | TLS cert directory |
| `webhook.serviceName` | `hostname-operator-webhook-service` | Webhook service name |
| `webhook.secretName` | `hostname-operator-webhook-cert` | TLS cert secret name |
| `runtime.logLevel` | `info` | Log level |
| `runtime.defaultTemplate` | `vm-###` | Default hostname template |
| `runtime.counterStart` | `1` | Starting counter index |
| `runtime.vcenterValidation` | `true` | Enable vCenter validation |

## Error Handling

- **Template parsing error**: VM creation is rejected with a clear error message
- **Counter locked**: If the counter is locked (concurrent access), the webhook returns a retryable error
- **vCenter connection failure**: VM creation is rejected (fail closed)
- **Duplicate hostname**: The webhook automatically retries with the next counter index
- **Maximum retries exceeded (100)**: VM creation is rejected

## Development

### Prerequisites

- [Carvel tools](https://carvel.dev/) (ytt, kbld, imgpkg, kapp)
- Go 1.22+
- Docker (or podman) for container images
- Access to a container registry (for production builds)

Install Carvel tools:

```bash
brew tap carvel-dev/carvel
brew install ytt kbld imgpkg kapp
```

### Building the Go Operator

```bash
cd supervisor-service
CGO_ENABLED=0 go build -o bin/hostname-operator .
```

### Building and Publishing the Supervisor Service

#### Full Release (build + push + bundle + service YAML)

```bash
# From the hostname-operator root directory
make supervisor-release REGISTRY=my-registry.example.com
```

This runs:
1. `make docker-build` — Build operator Go binary + container image
2. `make docker-push` — Push operator image to registry
3. `make supervisor-bundle` — Run ytt → kbld → build imgpkg bundle → push to registry
4. `make supervisor-service-yaml` — Generate upload-ready Package YAML

The output file is:

```
dist/hostname-operator-supervisorservice-1.0.0.yaml
```

Upload this YAML via vSphere Client → Workload Management → Services → Add New Service.

#### Step-by-step

```bash
# 1. Build and push the operator image
make docker-build docker-push IMG=my-registry.example.com/hostname-operator:1.0.0

# 2. Build and push the Carvel bundle (includes all templates + locked images)
make supervisor-bundle IMG=my-registry.example.com/hostname-operator:1.0.0 \
    BUNDLE_IMG=my-registry.example.com/hostname-operator-bundle:1.0.0

# 3. Generate the upload-ready Package YAML
make supervisor-service-yaml BUNDLE_IMG=my-registry.example.com/hostname-operator-bundle:1.0.0
```

### Offline / Air-Gapped Environments

#### Step 1: Build and export the bundle as a tarball (on a connected host)

```bash
make supervisor-release REGISTRY=my-registry.example.com
make supervisor-offline-tar BUNDLE_IMG=my-registry.example.com/hostname-operator-bundle:1.0.0
```

Produces: `dist/hostname-operator-airgap-1.0.0.tar`

#### Step 2: Transfer to air-gapped lab and import

```bash
# Copy dist/hostname-operator-airgap-1.0.0.tar and
# dist/hostname-operator-supervisorservice-1.0.0.yaml to the lab.

# Import the bundle to a local registry
make supervisor-offline-import \
    TAR=hostname-operator-airgap-1.0.0.tar \
    DEST_REPO=nexus.corp/vcf/hostname-operator-bundle \
    SERVICE_YAML=hostname-operator-supervisorservice-1.0.0.yaml
```

Upload the pinned `hostname-operator-supervisorservice-1.0.0.yaml` via vSphere Client.

### Relocating to a Different Registry

```bash
make supervisor-relocate \
    BUNDLE_IMG=ghcr.io/myorg/hostname-operator-bundle:1.0.0 \
    DEST_REPO=nexus.corp/vcf/hostname-operator-bundle

# Then regenerate the service YAML with the relocated digest
make supervisor-service-yaml BUNDLE_IMG=nexus.corp/vcf/hostname-operator-bundle:1.0.0
```

### Validating Templates

```bash
# Validate that ytt templates render correctly
make validate
```

### Available Make Targets

```bash
make help
```

| Target | Description |
|--------|-------------|
| `build` | Build the Go operator binary |
| `docker-build` | Build Docker image |
| `docker-push` | Push Docker image to registry |
| `supervisor-crd-sync` | Sync CRD into bundle |
| `supervisor-bundle` | Build and push imgpkg bundle (ytt + kbld) |
| `supervisor-service-yaml` | Generate upload-ready Package YAML |
| `supervisor-release` | One-shot: build + push + bundle + service YAML |
| `supervisor-relocate` | Relocate bundle to a different registry |
| `supervisor-offline-tar` | Export bundle as air-gap tarball |
| `supervisor-offline-import` | Import air-gap tarball into a registry |
| `validate` | Validate ytt templates |
| `clean` | Clean build artifacts |
| `help` | Show help message |

### Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `IMG` | `registry.example.com/hostname-operator:1.0.0` | Operator image reference |
| `REGISTRY` | `registry.example.com` | Container registry |
| `VERSION` | `1.0.0` | Release version |
| `BUNDLE_IMG` | `$(REGISTRY)/hostname-operator-bundle:$(VERSION)` | Bundle image reference |
| `DEST_REPO` | (required for relocate) | Destination registry repo |

## Project Structure

```
hostname-operator/
├── Makefile                                           # Build targets (VCF9 Supervisor Service)
├── package.yml                                        # Placeholder (source in config/supervisor-service/package/)
├── config/
│   └── supervisor-service/                            # VCF9 Supervisor Service definition
│       ├── kustomization.yaml                         # Guard to prevent accidental kustomize
│       ├── sample-values.yaml                         # Documented sample install-time values
│       ├── bundle/                                    # imgpkg bundle content
│       │   └── config/
│       │       ├── schema.yaml                        # ytt values schema
│       │       ├── values.yaml                        # ytt default values
│       │       ├── 001-namespace.yaml                 # Namespace + imagePullSecret
│       │       ├── 002-crd.yaml                       # HostnameCounter CRD
│       │       ├── 003-rbac.yaml                      # RBAC (ServiceAccount + ClusterRole + Binding)
│       │       └── 004-manager.yaml                   # Deployment + Service + MutatingWebhook
│       └── package/                                   # Carvel Package source
│           ├── package-metadata.yaml                  # Service identity (hostname-operator.vcf.vmware.com)
│           └── package.yaml                           # Versioned Package with valuesSchema + template
└── supervisor-service/                                # Go operator source
    ├── main.go                                        # Entrypoint
    ├── go.mod                                         # Go module
    ├── api/v1alpha1/
    │   ├── hostnamecounter_types.go                   # CRD types
    │   └── groupversion_info.go                       # API group registration
    ├── controllers/
    │   └── hostnamecounter_controller.go              # Counter reconciliation
    ├── webhook/
    │   └── hostname_webhook.go                        # Mutating webhook handler
    └── pkg/
        ├── template/template.go                       # Template parsing & rendering
        └── vcenter/validator.go                       # vCenter uniqueness checks
```

## Comparison with vcf-salt-operator

This project follows the same VCF9 Supervisor Service structure as `vcf-salt-operator`:

| Aspect | hostname-operator | vcf-salt-operator |
|--------|-------------------|-------------------|
| Service ID | `hostname-operator.vcf.vmware.com` | `vcf-salt-operator.salt.vcf.io` |
| Bundle path | `config/supervisor-service/bundle/` | `config/supervisor-service/bundle/` |
| Package source | `config/supervisor-service/package/` | `config/supervisor-service/package/` |
| Build system | Carvel (ytt + kbld + imgpkg + kapp) | Carvel (ytt + kbld + imgpkg + kapp) |
| Deployment | VCF9 Supervisor Service | VCF9 Supervisor Service |
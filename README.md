# VM Hostname Operator

A VCF Service that automatically generates unique hostnames for VirtualMachine resources using configurable templates with incremental counters and vCenter duplicate validation.

## Overview

The VM Hostname Operator is deployed as a VCF Service across one or more Supervisor clusters. It intercepts VirtualMachine resource creation via a mutating webhook, generates a hostname from a template (e.g., `web-nginx-###`), validates the hostname is unique against vCenter, and rejects VM creation if a unique hostname cannot be determined.

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

### 1. Deploy the VCF Service

Use the VCF Automation API to register and activate the service:

```bash
# Variables
VCFA_HOST="vcfa.example.com"
API="https://${VCFA_HOST}/api/extension/broadcom/service-manager/v2"
ACCEPT="application/json;version=41.0.0-alpha"
TOKEN="<your-bearer-token>"

# Upload and create service
SERVICE_ID=$(curl -s -X POST "${API}/vcf-services" \
  -H "Accept: ${ACCEPT}" -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"source": "https://depot.example.com:9443/hostname-operator-1.0.0.tar"}' | jq -r '.id')

# Wait for Ready state, then activate
# See VCF Service API documentation for full activation workflow
```

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
  namespace: vm-operator-system
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
┌────────────────────────────────────────────────────────────────┐
│                    VCF Automation                              │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │                  Service Manager                          │  │
│  │  Creates SupervisorService CR → deploys Carvel package   │  │
│  └──────────────────────┬───────────────────────────────────┘  │
│                         │                                      │
└─────────────────────────┼──────────────────────────────────────┘
                          │
                          ▼
              ┌─────────────────────────┐
              │    Supervisor Cluster    │
              │                         │
              │  ┌───────────────────┐  │
              │  │ Hostname Operator │  │
              │  │                   │  │
              │  │ ├─ Mutating       │  │
              │  │ │  Webhook        │──┼── Intercepts VM CREATE
              │  │ │                  │  │
              │  │ ├─ Hostname        │  │
              │  │ │  Service         │──┼── Manages HostnameCounter CRs
              │  │ │                  │  │
              │  │ ├─ vCenter         │──┼── Validates against vCenter
              │  │ │  Validator      │  │
              │  │ └──────────────────┘  │
              │                         │
              │  ┌───────────────────┐  │
              │  │ HostnameCounter   │  │
              │  │ CRD               │  │
              │  └───────────────────┘  │
              └─────────────────────────┘
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

These can be overridden at install time via the `SupervisorService` CR's values.

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

### Building the VCF Service Tarball

The build produces the `hostname-operator-1.0.0.tar` file that gets uploaded to VCF Automation.

#### Method 1: Production Build (full pipeline with image locking)

```bash
# From the hostname-operator directory
make quick REGISTRY=my-registry.example.com
```

This runs the full pipeline:
1. `make build-go` — Compile the Go binary
2. `make docker-build` — Build the container image
3. `make docker-push` — Push image to registry
4. `make kbld-lock` — Lock all image references into `.imgpkg/images.yml`
5. `make bundle` — Push the Carvel bundle to the registry
6. `make tarball` — Export the bundle to `dist/hostname-operator-1.0.0.tar`

The tarball is now ready at `dist/hostname-operator-1.0.0.tar` (self-contained, includes all OCI images).

#### Method 2: Offline/Source Tarball (quick packaging without registry)

```bash
# From the hostname-operator directory
make offline-tarball
```

Creates `dist/hostname-operator-1.0.0.tar` with the source files. This tarball can be uploaded to VCF Automation directly, but requires the Service Manager to pull images from a registry at activation time.

#### Method 3: Manual tarball creation

```bash
# From the hostname-operator directory
mkdir -p dist
tar -cvf dist/hostname-operator-1.0.0.tar \
    --exclude='dist' \
    --exclude='.imgpkg' \
    --exclude='supervisor-service/bin' \
    .
```

The tarball should contain:
```
package.yml
config/values.yml
config/vcf-service.yml
config/hostname-operator.lib.yml
.values/render.yml
.values/transpiler.yml
supervisor-service/package.yml
supervisor-service/config/values.yml
supervisor-service/config/deployment.yml
supervisor-service/go.mod
supervisor-service/main.go
supervisor-service/api/v1alpha1/hostnamecounter_types.go
supervisor-service/api/v1alpha1/groupversion_info.go
supervisor-service/controllers/hostnamecounter_controller.go
supervisor-service/webhook/hostname_webhook.go
supervisor-service/pkg/template/template.go
supervisor-service/pkg/vcenter/validator.go
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
| `all` | Full build pipeline (clean → build → docker → kbld → bundle → tarball) |
| `build-go` | Build the Go operator binary |
| `docker-build` | Build Docker image |
| `docker-push` | Push Docker image to registry |
| `kbld-lock` | Lock image references |
| `bundle` | Build Carvel bundle |
| `tarball` | Export bundle to tarball |
| `quick` | Run full pipeline with one command |
| `offline-tarball` | Create source-only tarball without registry |
| `validate` | Validate ytt templates |
| `clean` | Clean build artifacts |
| `help` | Show help message |

## Files

```
vcf-services/hostname-operator/
├── package.yml                                    # VCF Service Package definition
├── config/
│   ├── values.yml                                 # ytt data values schema
│   ├── vcf-service.yml                            # Entry point template
│   └── hostname-operator.lib.yml                  # SupervisorService CR generator
├── .values/
│   ├── render.yml                                 # Build-time example values
│   └── transpiler.yml                             # Inventory transform
└── supervisor-service/                            # Go operator source
    ├── package.yml                                # Supervisor Service Package
    ├── config/
    │   ├── values.yml                             # Deployment values
    │   └── deployment.yml                         # Kubernetes manifests
    ├── main.go                                    # Entrypoint
    ├── api/v1alpha1/
    │   ├── hostnamecounter_types.go               # CRD types
    │   └── groupversion_info.go                   # API group
    ├── controllers/
    │   └── hostnamecounter_controller.go          # Counter controller
    ├── webhook/
    │   └── hostname_webhook.go                    # Mutating webhook
    └── pkg/
        ├── template/template.go                   # Template engine
        └── vcenter/validator.go                   # vCenter validator
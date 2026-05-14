// Package vcenter provides utilities for validating hostname uniqueness
// against vCenter's inventory.
package vcenter

import (
	"context"
	"fmt"
	"net/url"

	"github.com/go-logr/logr"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/vim25/soap"
)

// Config holds vCenter connection configuration.
type Config struct {
	Hostname     string
	Username     string
	Password     string
	InsecureTLS  bool
	Datacenter   string
}

// Validator checks hostname uniqueness against vCenter.
type Validator struct {
	config Config
	logger logr.Logger
}

// NewValidator creates a new vCenter validator.
func NewValidator(config Config, logger logr.Logger) *Validator {
	return &Validator{
		config: config,
		logger: logger.WithName("vcenter-validator"),
	}
}

// ValidateHostname checks if a VM with the given hostname already exists in vCenter.
// Returns true if the hostname is unique (no VM with that name exists).
// Returns false and an error if validation fails or a duplicate is found.
func (v *Validator) ValidateHostname(ctx context.Context, hostname string) (bool, error) {
	logger := v.logger.WithValues("hostname", hostname)

	client, err := v.connect(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to connect to vCenter: %w", err)
	}
	defer func() {
		if err := client.Logout(ctx); err != nil {
			logger.Error(err, "failed to logout from vCenter")
		}
	}()

	finder := find.NewFinder(client.Client, true)

	// Try to find a VM with the given name in the root folder
	// If no datacenter is specified, search recursively
	if v.config.Datacenter != "" {
		dc, err := finder.Datacenter(ctx, v.config.Datacenter)
		if err != nil {
			return false, fmt.Errorf("failed to find datacenter %s: %w", v.config.Datacenter, err)
		}
		finder = find.NewFinder(dc, true)
	}

	matches, err := finder.VirtualMachineList(ctx, hostname)
	if err != nil {
		// If no VMs matched, finder returns an error
		// This means the hostname is unique
		logger.V(2).Info("Hostname is unique in vCenter (no matching VMs found)")
		return true, nil
	}

	if len(matches) > 0 {
		logger.Info("Hostname already exists in vCenter",
			"matchedCount", len(matches),
			"firstMatch", matches[0].Name())
		return false, nil
	}

	logger.V(2).Info("Hostname is unique in vCenter")
	return true, nil
}

// connect creates a new vCenter SOAP client connection.
func (v *Validator) connect(ctx context.Context) (*govmomi.Client, error) {
	u, err := url.Parse(fmt.Sprintf("https://%s/sdk", v.config.Hostname))
	if err != nil {
		return nil, fmt.Errorf("failed to parse vCenter URL: %w", err)
	}
	u.User = url.UserPassword(v.config.Username, v.config.Password)

	soapClient, err := soap.NewClient(u, v.config.InsecureTLS)
	if err != nil {
		return nil, fmt.Errorf("failed to create SOAP client: %w", err)
	}

	client, err := govmomi.NewClient(ctx, soapClient, true)
	if err != nil {
		return nil, fmt.Errorf("failed to create govmomi client: %w", err)
	}

	return client, nil
}

// ValidateInBulk checks multiple hostnames against vCenter in a single pass.
// Returns a map of hostname -> exists (true if duplicate, false if unique).
func (v *Validator) ValidateInBulk(ctx context.Context, hostnames []string) (map[string]bool, error) {
	results := make(map[string]bool, len(hostnames))
	for _, hostname := range hostnames {
		unique, err := v.ValidateHostname(ctx, hostname)
		if err != nil {
			return nil, fmt.Errorf("failed to validate hostname %s: %w", hostname, err)
		}
		results[hostname] = !unique
	}
	return results, nil
}

// IsVCenterConfigured returns true if the validator has vCenter connection details.
func (v *Validator) IsVCenterConfigured() bool {
	return v.config.Hostname != "" && v.config.Username != "" && v.config.Password != ""
}
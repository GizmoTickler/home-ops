# HomeOps CLI Project Overview

## Purpose
This is a comprehensive home infrastructure repository containing a Go-based CLI tool for infrastructure automation. The CLI manages:
- **Kubernetes cluster configuration** using GitOps with Flux
- **Talos Linux** cluster management on TrueNAS VMs  
- **HomeOps CLI** for infrastructure automation
- **Kubernetes applications** deployed via Helm and Kustomize

## Tech Stack
- **Language**: Go 1.25.0
- **CLI Framework**: Cobra + Viper for configuration
- **Testing**: testify/assert and testify/require
- **Logging**: Zap structured logging
- **Infrastructure**: Kubernetes, Talos Linux, TrueNAS
- **Secret Management**: 1Password integration
- **Template Engine**: Jinja2 templates embedded via go:embed
- **GitOps**: Flux for continuous deployment

## Main CLI Commands Structure
- `bootstrap/` - Complete cluster bootstrap with preflight checks
- `talos/` - Talos Linux node and VM management  
- `kubernetes/` - Kubernetes cluster operations
- `volsync/` - Volume backup and restore operations
- `workstation/` - Local development environment setup
- `completion/` - Shell completion support

## Internal Packages
- `internal/templates/` - Embedded Jinja2 templates for Talos configs
- `internal/talos/` - Talos factory API integration for custom ISOs
- `internal/truenas/` - TrueNAS API client for VM management
- `internal/yaml/` - YAML processing and merging utilities
- `internal/ssh/` - SSH client for remote operations
- `internal/iso/` - ISO download and management
- `internal/config/` - Configuration management
- `internal/common/` - Common utilities (logger, 1Password auth, secrets)
- `internal/errors/` - Error handling and reporting
- `internal/testutil/` - Test utilities and helpers

## Key Integration Points
- **1Password**: For secret management and template rendering
- **TrueNAS**: VM management via API
- **Talos Factory API**: Custom ISO generation
- **vSphere**: VM configuration and management
- **Kubernetes**: Resource management and operations
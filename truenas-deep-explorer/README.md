# TrueNAS Deep Explorer

A specialized tool for deep exploration and documentation of the TrueNAS VM API. This tool systematically explores TrueNAS API methods, collects real data examples, and generates comprehensive documentation.

## Features

- **Deep API Exploration**: Systematically explores 20+ TrueNAS API methods including VM, device, and dataset operations
- **Real Data Collection**: Gathers actual VM and dataset examples from your TrueNAS system
- **Comprehensive Documentation**: Generates detailed Markdown documentation with:
  - Real VM examples with full JSON structures
  - Real dataset examples
  - API method details with working parameters
  - Error analysis and parameter requirements
  - Complete API response structures

## Usage

### Prerequisites

- Go 1.22.2 or later
- Access to a TrueNAS SCALE system
- TrueNAS API key
- (Optional) 1Password CLI (`op`) for secure credential management

### Installation

```bash
cd truenas-deep-explorer
go mod tidy
go build -o truenas-deep-explorer .
```

### 1Password Integration (Recommended)

The tool supports secure credential management through 1Password CLI:

```bash
# Ensure 1Password CLI is installed and authenticated
op whoami

# Configure secrets in 1Password (Infrastructure vault, talosdeploy item):
# - TRUENAS_HOST: Your TrueNAS hostname or IP
# - TRUENAS_API: Your TrueNAS API key

# Run with 1Password integration (no credentials needed)
./truenas-deep-explorer
```

### Running the Tool

```bash
# Run with command-line arguments (overrides 1Password/env vars)
./truenas-deep-explorer \
  -truenas-host "your-truenas-host.local" \
  -truenas-api-key "your-api-key" \
  -output "docs/TRUENAS-VM-DEEP-API.md"
```

### Command Line Options

- `-truenas-host`: TrueNAS hostname or IP address (optional with 1Password)
- `-truenas-api-key`: TrueNAS API key (optional with 1Password)
- `-output`: Output markdown file path (default: `docs/TRUENAS-VM-DEEP-API.md`)
- `-no-ssl`: Use ws:// instead of wss:// for connection (optional)

### Credential Priority

The tool uses the following priority for credentials:
1. Command-line flags (`-truenas-host`, `-truenas-api-key`)
2. 1Password secrets (`op://Infrastructure/talosdeploy/TRUENAS_HOST`, `op://Infrastructure/talosdeploy/TRUENAS_API`)
3. Environment variables (`TRUENAS_HOST`, `TRUENAS_API_KEY`)

### Environment Variables

You can also set credentials via environment variables (fallback if 1Password fails):

```bash
export TRUENAS_HOST="your-truenas-host.local"
export TRUENAS_API_KEY="your-api-key"
./truenas-deep-explorer
```

## API Methods Explored

### VM Methods
- `vm.query` - List all VMs
- `vm.create` - Create new VM
- `vm.update` - Update VM configuration
- `vm.delete` - Delete VM
- `vm.start` - Start VM
- `vm.stop` - Stop VM
- `vm.status` - Get VM status
- `vm.get_instance` - Get VM instance details
- `vm.bootloader_options` - Available bootloader options
- `vm.cpu_model_choices` - Available CPU models
- `vm.random_mac` - Generate random MAC address
- `vm.resolution_choices` - Available display resolutions
- `vm.get_available_memory` - Available system memory
- `vm.maximum_supported_vcpus` - Maximum supported VCPUs

### VM Device Methods
- `vm.device.query` - List VM devices
- `vm.device.create` - Create VM device
- `vm.device.update` - Update VM device
- `vm.device.delete` - Delete VM device
- `vm.device.disk_choices` - Available disk options
- `vm.device.nic_attach_choices` - Available network interfaces
- `vm.device.bind_choices` - Available bind options
- `vm.device.iotype_choices` - Available I/O types

### Dataset Methods
- `pool.dataset.query` - List datasets
- `pool.dataset.create` - Create dataset
- `pool.dataset.delete` - Delete dataset

## Output

The tool generates a comprehensive Markdown file containing:

1. **Real VM Examples**: Actual VM configurations from your system
2. **Real Dataset Examples**: Actual dataset structures from your system
3. **API Method Details**: Complete documentation of each API method including:
   - Successful response examples
   - Working parameter examples
   - Error messages and parameter requirements
   - Response structure analysis

## Purpose

This tool was created to:

- Document the TrueNAS VM API for development purposes
- Understand API method signatures and requirements
- Collect real-world examples of VM and dataset structures
- Aid in developing TrueNAS integration tools

## Note

This is a development/documentation tool, not a production management tool. For production VM management, use the main `homeops-cli` tool which includes operational VM management commands.
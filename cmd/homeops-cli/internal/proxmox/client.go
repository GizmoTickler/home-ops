// Package proxmox provides a client for interacting with Proxmox VE API
package proxmox

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"

	"homeops-cli/internal/common"
	"homeops-cli/internal/constants"

	"github.com/luthermonson/go-proxmox"
)

// Client wraps the go-proxmox client with authentication handling
type Client struct {
	client *proxmox.Client
	node   *proxmox.Node
	ctx    context.Context
	cancel context.CancelFunc
	logger *common.ColorLogger
}

// NewClient creates a new Proxmox client
func NewClient(host, tokenID, secret string, insecure bool) (*Client, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Build the API URL
	apiURL := fmt.Sprintf("https://%s:8006/api2/json", host)

	// Create HTTP client with optional insecure TLS
	httpClient := http.DefaultClient
	if insecure {
		httpClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	}

	// Create Proxmox client with API token authentication
	client := proxmox.NewClient(apiURL,
		proxmox.WithAPIToken(tokenID, secret),
		proxmox.WithHTTPClient(httpClient),
	)

	return &Client{
		client: client,
		ctx:    ctx,
		cancel: cancel,
		logger: common.NewColorLogger(),
	}, nil
}

// Connect verifies connection and retrieves node information
func (c *Client) Connect(nodeName string) error {
	// Verify connection by getting version
	version, err := c.client.Version(c.ctx)
	if err != nil {
		return fmt.Errorf("failed to connect to Proxmox: %w", err)
	}
	c.logger.Info("Connected to Proxmox VE %s", version.Version)

	// Get the specified node
	node, err := c.client.Node(c.ctx, nodeName)
	if err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}
	c.node = node
	c.logger.Debug("Using Proxmox node: %s", nodeName)

	return nil
}

// Close closes the client connection
func (c *Client) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	return nil
}

// GetNextVMID gets the next available VMID
func (c *Client) GetNextVMID() (int, error) {
	cluster, err := c.client.Cluster(c.ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get cluster: %w", err)
	}
	return cluster.NextID(c.ctx)
}

// CreateVM creates a new VM with the specified options
func (c *Client) CreateVM(vmid int, options ...proxmox.VirtualMachineOption) (*proxmox.Task, error) {
	if c.node == nil {
		return nil, fmt.Errorf("not connected to a node")
	}
	return c.node.NewVirtualMachine(c.ctx, vmid, options...)
}

// GetVM retrieves a VM by ID
func (c *Client) GetVM(vmid int) (*proxmox.VirtualMachine, error) {
	if c.node == nil {
		return nil, fmt.Errorf("not connected to a node")
	}
	return c.node.VirtualMachine(c.ctx, vmid)
}

// ListVMs lists all VMs on the node
func (c *Client) ListVMs() (proxmox.VirtualMachines, error) {
	if c.node == nil {
		return nil, fmt.Errorf("not connected to a node")
	}
	return c.node.VirtualMachines(c.ctx)
}

// GetStorage retrieves a storage by name
func (c *Client) GetStorage(storageName string) (*proxmox.Storage, error) {
	if c.node == nil {
		return nil, fmt.Errorf("not connected to a node")
	}
	return c.node.Storage(c.ctx, storageName)
}

// UploadISO uploads an ISO file to Proxmox storage via URL download
func (c *Client) UploadISO(storageName, isoURL, filename string) (*proxmox.Task, error) {
	storage, err := c.GetStorage(storageName)
	if err != nil {
		return nil, fmt.Errorf("failed to get storage %s: %w", storageName, err)
	}
	return storage.DownloadURL(c.ctx, "iso", filename, isoURL)
}

// Context returns the client context
func (c *Client) Context() context.Context {
	return c.ctx
}

// Node returns the current node
func (c *Client) Node() *proxmox.Node {
	return c.node
}

// GetCredentials retrieves Proxmox credentials from 1Password or environment variables
func GetCredentials() (host, tokenID, secret, nodeName string, err error) {
	logger := common.NewColorLogger()
	usedEnvFallback := false

	// Try 1Password first - batch lookup for better performance
	secrets := common.Get1PasswordSecretsBatch([]string{
		constants.OpProxmoxHost,
		constants.OpProxmoxTokenID,
		constants.OpProxmoxTokenSecret,
		constants.OpProxmoxNode,
	})
	host = secrets[constants.OpProxmoxHost]
	tokenID = secrets[constants.OpProxmoxTokenID]
	secret = secrets[constants.OpProxmoxTokenSecret]
	nodeName = secrets[constants.OpProxmoxNode]

	// Fall back to environment variables if 1Password fails
	if host == "" {
		host = os.Getenv(constants.EnvProxmoxHost)
		if host != "" {
			usedEnvFallback = true
		}
	}
	if tokenID == "" {
		tokenID = os.Getenv(constants.EnvProxmoxTokenID)
		if tokenID != "" {
			usedEnvFallback = true
		}
	}
	if secret == "" {
		secret = os.Getenv(constants.EnvProxmoxTokenSecret)
		if secret != "" {
			usedEnvFallback = true
		}
	}
	if nodeName == "" {
		nodeName = os.Getenv(constants.EnvProxmoxNode)
		if nodeName == "" {
			nodeName = "pve" // Default node name
		}
	}

	// Check if we have required credentials
	if host == "" || tokenID == "" || secret == "" {
		return "", "", "", "", fmt.Errorf("proxmox credentials not found: set %s, %s, and %s environment variables or configure 1Password with '%s', '%s', and '%s'",
			constants.EnvProxmoxHost, constants.EnvProxmoxTokenID, constants.EnvProxmoxTokenSecret,
			constants.OpProxmoxHost, constants.OpProxmoxTokenID, constants.OpProxmoxTokenSecret)
	}

	// Warn if using environment variables (less secure than 1Password)
	if usedEnvFallback {
		logger.Warn("Using environment variables for Proxmox credentials. Consider using 1Password for better security.")
	}

	return host, tokenID, secret, nodeName, nil
}

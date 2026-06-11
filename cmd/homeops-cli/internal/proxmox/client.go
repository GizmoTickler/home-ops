// Package proxmox provides a client for interacting with Proxmox VE API
package proxmox

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"

	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/secrets"

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
		common.NewColorLogger().Warn("Proxmox TLS verification DISABLED via %s=true (unset it to verify the endpoint)", constants.EnvProxmoxInsecure)
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
	if c.client == nil {
		return fmt.Errorf("proxmox client is not initialized")
	}

	// Verify connection by getting version. Reads are idempotent, so retry on
	// transient errors (e.g. a momentary 5xx or connection reset).
	version, err := common.RetryValue(common.DefaultAPIRetry(), func() (*proxmox.Version, error) {
		return c.client.Version(c.ctx)
	})
	if err != nil {
		return fmt.Errorf("failed to connect to Proxmox: %w", err)
	}
	c.logger.Info("Connected to Proxmox VE %s", version.Version)

	// Get the specified node
	node, err := common.RetryValue(common.DefaultAPIRetry(), func() (*proxmox.Node, error) {
		return c.client.Node(c.ctx, nodeName)
	})
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
	if c.client == nil {
		return 0, fmt.Errorf("proxmox client is not initialized")
	}
	return common.RetryValue(common.DefaultAPIRetry(), func() (int, error) {
		cluster, err := c.client.Cluster(c.ctx)
		if err != nil {
			return 0, fmt.Errorf("failed to get cluster: %w", err)
		}
		return cluster.NextID(c.ctx)
	})
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
	return common.RetryValue(common.DefaultAPIRetry(), func() (*proxmox.VirtualMachine, error) {
		return c.node.VirtualMachine(c.ctx, vmid)
	})
}

// ListVMs lists all VMs on the node
func (c *Client) ListVMs() (proxmox.VirtualMachines, error) {
	if c.node == nil {
		return nil, fmt.Errorf("not connected to a node")
	}
	return common.RetryValue(common.DefaultAPIRetry(), func() (proxmox.VirtualMachines, error) {
		return c.node.VirtualMachines(c.ctx)
	})
}

// GetStorage retrieves a storage by name
func (c *Client) GetStorage(storageName string) (*proxmox.Storage, error) {
	if c.node == nil {
		return nil, fmt.Errorf("not connected to a node")
	}
	return common.RetryValue(common.DefaultAPIRetry(), func() (*proxmox.Storage, error) {
		return c.node.Storage(c.ctx, storageName)
	})
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

// GetCredentials retrieves Proxmox credentials through the configured secret
// references (homeops.yaml `secrets:` map), with legacy environment-variable
// fallbacks.
func GetCredentials() (host, tokenID, secret, nodeName string, err error) {
	cfg := config.Get()
	hostRef := cfg.SecretRef(config.KeyProxmoxHost)
	tokenIDRef := cfg.SecretRef(config.KeyProxmoxTokenID)
	tokenSecretRef := cfg.SecretRef(config.KeyProxmoxTokenSecret)
	nodeRef := cfg.SecretRef(config.KeyProxmoxNode)

	resolved := secrets.ResolveBatch([]string{hostRef, tokenIDRef, tokenSecretRef, nodeRef})
	host = resolved[hostRef]
	tokenID = resolved[tokenIDRef]
	secret = resolved[tokenSecretRef]
	nodeName = resolved[nodeRef]

	// Legacy fallback: plain environment variables, regardless of the
	// configured references.
	if host == "" {
		host = os.Getenv(constants.EnvProxmoxHost)
	}
	if tokenID == "" {
		tokenID = os.Getenv(constants.EnvProxmoxTokenID)
	}
	if secret == "" {
		secret = os.Getenv(constants.EnvProxmoxTokenSecret)
	}
	if nodeName == "" {
		nodeName = os.Getenv(constants.EnvProxmoxNode)
		if nodeName == "" {
			nodeName = config.DefaultProxmoxNodeName
		}
	}

	if host == "" || tokenID == "" || secret == "" {
		return "", "", "", "", fmt.Errorf("proxmox credentials not found: secrets.%s (%s), secrets.%s (%s), and secrets.%s (%s) did not resolve — fix the references in your homeops config or set %s/%s/%s, then re-check with 'homeops-cli config doctor'",
			config.KeyProxmoxHost, hostRef, config.KeyProxmoxTokenID, tokenIDRef, config.KeyProxmoxTokenSecret, tokenSecretRef,
			constants.EnvProxmoxHost, constants.EnvProxmoxTokenID, constants.EnvProxmoxTokenSecret)
	}

	return host, tokenID, secret, nodeName, nil
}

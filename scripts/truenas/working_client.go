package truenas

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/truenas/api_client_golang/truenas_api"
)

// VM represents a virtual machine
type VM struct {
	ID          int                    `json:"id"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Memory      int                    `json:"memory"`
	VCPUs       int                    `json:"vcpus"`
	Bootloader  string                 `json:"bootloader"`
	Autostart   bool                   `json:"autostart"`
	Status      map[string]interface{} `json:"status"`
	Devices     []VMDevice             `json:"devices"`
}

// VMCreateRequest represents a VM creation request
type VMCreateRequest struct {
	Name            string     `json:"name"`
	Description     string     `json:"description,omitempty"`
	Memory          int        `json:"memory"`
	VCPUs           int        `json:"vcpus"`
	Bootloader      string     `json:"bootloader"`
	Autostart       bool       `json:"autostart"`
	Time            string     `json:"time,omitempty"`
	ShutdownTimeout int        `json:"shutdown_timeout,omitempty"`
	Devices         []VMDevice `json:"devices"`
}

// VMDevice represents a VM device using a map structure for maximum flexibility
type VMDevice map[string]interface{}

// Dataset represents a TrueNAS dataset
type Dataset struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	Type     string                 `json:"type"`
	Pool     string                 `json:"pool"`
	Children []Dataset              `json:"children"`
	Props    map[string]interface{} `json:"properties"`
}

// DatasetCreateRequest represents a dataset creation request
type DatasetCreateRequest struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Volsize      *int64 `json:"volsize,omitempty"`
	Volblocksize string `json:"volblocksize,omitempty"`
}

// WorkingClient wraps the official TrueNAS API client with the correct authentication
type WorkingClient struct {
	client *truenas_api.Client
	apiKey string
	host   string
	port   int
	useSSL bool
}

// NewWorkingClient creates a new working TrueNAS client using the official API client
func NewWorkingClient(host, apiKey string, port int, useSSL bool) *WorkingClient {
	return &WorkingClient{
		host:   host,
		apiKey: apiKey,
		port:   port,
		useSSL: useSSL,
	}
}

// Connect establishes connection and authenticates with TrueNAS
func (c *WorkingClient) Connect() error {
	// Construct server URL like the working tnascert-deploy tool
	protocol := "wss"
	if !c.useSSL {
		protocol = "ws"
	}

	serverURL := fmt.Sprintf("%s://%s:%d/api/current", protocol, c.host, c.port)
	log.Printf("Connecting to TrueNAS at %s", serverURL)

	// Create client using official TrueNAS API client
	client, err := truenas_api.NewClient(serverURL, !c.useSSL) // tlsSkipVerify is opposite of useSSL
	if err != nil {
		return fmt.Errorf("failed to create TrueNAS client: %w", err)
	}

	c.client = client

	// Authenticate using the working pattern: empty username/password, API key only
	log.Printf("Authenticating with API key...")
	err = c.client.Login("", "", c.apiKey)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	log.Printf("Successfully authenticated with TrueNAS")
	return nil
}

// Close closes the connection
func (c *WorkingClient) Close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

// Call makes an API call using the official client
func (c *WorkingClient) Call(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
	if c.client == nil {
		return nil, fmt.Errorf("not connected to TrueNAS")
	}

	return c.client.Call(method, timeoutSeconds, params)
}

// QueryVMs queries VMs with optional filters
func (c *WorkingClient) QueryVMs(filters interface{}) ([]VM, error) {
	params := []interface{}{}
	if filters != nil {
		params = append(params, filters)
	}

	result, err := c.Call("vm.query", params, 30)
	if err != nil {
		return nil, err
	}

	var vms []VM
	if err := json.Unmarshal(result, &vms); err != nil {
		return nil, fmt.Errorf("failed to unmarshal VMs: %w", err)
	}

	return vms, nil
}

// CreateVM creates a new virtual machine
func (c *WorkingClient) CreateVM(vmConfig VMCreateRequest) (*VM, error) {
	result, err := c.Call("vm.create", []interface{}{vmConfig}, 60)
	if err != nil {
		return nil, err
	}

	var vm VM
	if err := json.Unmarshal(result, &vm); err != nil {
		return nil, fmt.Errorf("failed to unmarshal VM: %w", err)
	}

	return &vm, nil
}

// StartVM starts a virtual machine
func (c *WorkingClient) StartVM(vmID int) error {
	_, err := c.Call("vm.start", []interface{}{vmID}, 30)
	return err
}

// StopVM stops a virtual machine
func (c *WorkingClient) StopVM(vmID int) error {
	_, err := c.Call("vm.stop", []interface{}{vmID}, 30)
	return err
}

// DeleteVM deletes a virtual machine
func (c *WorkingClient) DeleteVM(vmID int) error {
	_, err := c.Call("vm.delete", []interface{}{vmID}, 30)
	return err
}

// QueryDatasets queries datasets with optional filters
func (c *WorkingClient) QueryDatasets(filters interface{}) ([]Dataset, error) {
	params := []interface{}{}
	if filters != nil {
		params = append(params, filters)
	}

	result, err := c.Call("pool.dataset.query", params, 30)
	if err != nil {
		return nil, err
	}

	var datasets []Dataset
	if err := json.Unmarshal(result, &datasets); err != nil {
		return nil, fmt.Errorf("failed to unmarshal datasets: %w", err)
	}

	return datasets, nil
}

// CreateDataset creates a new dataset
func (c *WorkingClient) CreateDataset(datasetConfig DatasetCreateRequest) (*Dataset, error) {
	result, err := c.Call("pool.dataset.create", []interface{}{datasetConfig}, 60)
	if err != nil {
		return nil, err
	}

	var dataset Dataset
	if err := json.Unmarshal(result, &dataset); err != nil {
		return nil, fmt.Errorf("failed to unmarshal dataset: %w", err)
	}

	return &dataset, nil
}

// DeleteDataset deletes a dataset
func (c *WorkingClient) DeleteDataset(name string, recursive bool) error {
	params := []interface{}{name}
	if recursive {
		params = append(params, map[string]bool{"recursive": true})
	}
	_, err := c.Call("pool.dataset.delete", params, 30)
	return err
}

// GetVMChoices gets various VM configuration choices
func (c *WorkingClient) GetVMBootloaderOptions() (interface{}, error) {
	result, err := c.Call("vm.bootloader_options", []interface{}{}, 10)
	if err != nil {
		return nil, err
	}

	var options interface{}
	if err := json.Unmarshal(result, &options); err != nil {
		return nil, fmt.Errorf("failed to unmarshal bootloader options: %w", err)
	}

	return options, nil
}

func (c *WorkingClient) GetVMCPUModelChoices() (interface{}, error) {
	result, err := c.Call("vm.cpu_model_choices", []interface{}{}, 10)
	if err != nil {
		return nil, err
	}

	var choices interface{}
	if err := json.Unmarshal(result, &choices); err != nil {
		return nil, fmt.Errorf("failed to unmarshal CPU model choices: %w", err)
	}

	return choices, nil
}

func (c *WorkingClient) GetRandomMAC() (string, error) {
	result, err := c.Call("vm.random_mac", []interface{}{}, 10)
	if err != nil {
		return "", err
	}

	var mac string
	if err := json.Unmarshal(result, &mac); err != nil {
		return "", fmt.Errorf("failed to unmarshal MAC address: %w", err)
	}

	return mac, nil
}

func (c *WorkingClient) GetAvailableMemory() (interface{}, error) {
	result, err := c.Call("vm.get_available_memory", []interface{}{}, 10)
	if err != nil {
		return nil, err
	}

	var memory interface{}
	if err := json.Unmarshal(result, &memory); err != nil {
		return nil, fmt.Errorf("failed to unmarshal available memory: %w", err)
	}

	return memory, nil
}

func (c *WorkingClient) GetMaxSupportedVCPUs() (interface{}, error) {
	result, err := c.Call("vm.maximum_supported_vcpus", []interface{}{}, 10)
	if err != nil {
		return nil, err
	}

	var vcpus interface{}
	if err := json.Unmarshal(result, &vcpus); err != nil {
		return nil, fmt.Errorf("failed to unmarshal max VCPUs: %w", err)
	}

	return vcpus, nil
}

// Device-related methods
func (c *WorkingClient) GetDeviceDiskChoices() (interface{}, error) {
	result, err := c.Call("vm.device.disk_choices", []interface{}{}, 10)
	if err != nil {
		return nil, err
	}

	var choices interface{}
	if err := json.Unmarshal(result, &choices); err != nil {
		return nil, fmt.Errorf("failed to unmarshal disk choices: %w", err)
	}

	return choices, nil
}

func (c *WorkingClient) GetDeviceNICAttachChoices() (interface{}, error) {
	result, err := c.Call("vm.device.nic_attach_choices", []interface{}{}, 10)
	if err != nil {
		return nil, err
	}

	var choices interface{}
	if err := json.Unmarshal(result, &choices); err != nil {
		return nil, fmt.Errorf("failed to unmarshal NIC attach choices: %w", err)
	}

	return choices, nil
}

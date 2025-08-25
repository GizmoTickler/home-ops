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
	ID       interface{}            `json:"id"` // Can be string or number
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

	log.Printf("Successfully connected to TrueNAS")
	return nil
}

// Close closes the connection to TrueNAS
func (c *WorkingClient) Close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

// Call makes a raw API call to TrueNAS
func (c *WorkingClient) Call(method string, params interface{}, timeoutSeconds int64) (json.RawMessage, error) {
	return c.client.Call(method, timeoutSeconds, params)
}

// QueryVMs retrieves all VMs from TrueNAS
func (c *WorkingClient) QueryVMs(filters interface{}) ([]VM, error) {
	// Ensure filters is an array for JSON-RPC compatibility
	var params []interface{}
	if filters != nil {
		params = []interface{}{filters}
	} else {
		params = []interface{}{}
	}

	result, err := c.Call("vm.query", params, 30)
	if err != nil {
		return nil, fmt.Errorf("failed to query VMs: %w", err)
	}

	// Parse JSON-RPC response
	var jsonRPCResponse map[string]interface{}
	if err := json.Unmarshal(result, &jsonRPCResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON-RPC response: %w", err)
	}

	// Extract the result field
	resultField, exists := jsonRPCResponse["result"]
	if !exists {
		return nil, fmt.Errorf("no result field in response")
	}

	// Convert result to JSON and then unmarshal as VM array
	resultJSON, err := json.Marshal(resultField)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result field: %w", err)
	}

	var vms []VM
	if err := json.Unmarshal(resultJSON, &vms); err != nil {
		return nil, fmt.Errorf("failed to unmarshal VMs: %w", err)
	}

	// Query devices for each VM
	for i := range vms {
		devices, err := c.QueryVMDevices(vms[i].ID)
		if err != nil {
			// Log error but don't fail the entire query
			log.Printf("Warning: failed to query devices for VM %s: %v", vms[i].Name, err)
			continue
		}

		// Convert devices to VMDevice slice
		vmDevices := make([]VMDevice, len(devices))
		for j, device := range devices {
			vmDevices[j] = VMDevice(device)
		}
		vms[i].Devices = vmDevices
	}

	return vms, nil
}

// CreateVM creates a new VM
func (c *WorkingClient) CreateVM(vmConfig map[string]interface{}) (*VM, error) {
	result, err := c.Call("vm.create", []interface{}{vmConfig}, 120)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM: %w", err)
	}

	// Parse JSON-RPC response
	var jsonRPCResponse map[string]interface{}
	if err := json.Unmarshal(result, &jsonRPCResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON-RPC response: %w", err)
	}

	// Extract the result field
	resultField, exists := jsonRPCResponse["result"]
	if !exists {
		return nil, fmt.Errorf("no result field in response")
	}

	// Convert result to JSON and then unmarshal as VM
	resultJSON, err := json.Marshal(resultField)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result field: %w", err)
	}

	var vm VM
	if err := json.Unmarshal(resultJSON, &vm); err != nil {
		return nil, fmt.Errorf("failed to unmarshal created VM data: %w", err)
	}

	return &vm, nil
}

// StartVM starts a VM
func (c *WorkingClient) StartVM(vmID int) error {
	_, err := c.Call("vm.start", []interface{}{vmID}, 60)
	return err
}

// StopVM stops a VM
func (c *WorkingClient) StopVM(vmID int) error {
	_, err := c.Call("vm.stop", []interface{}{vmID}, 60)
	return err
}

// DeleteVM deletes a VM
func (c *WorkingClient) DeleteVM(vmID int) error {
	_, err := c.Call("vm.delete", []interface{}{vmID}, 60)
	return err
}

func (c *WorkingClient) QueryVMDevices(vmID int) ([]map[string]interface{}, error) {
	params := []interface{}{[]interface{}{[]interface{}{"vm", "=", vmID}}}

	log.Printf("DEBUG: Querying VM devices for VM ID %d with params: %+v", vmID, params)

	result, err := c.Call("vm.device.query", params, 30)
	if err != nil {
		return nil, fmt.Errorf("failed to query VM devices: %w", err)
	}

	log.Printf("DEBUG: Raw vm.device.query response: %s", string(result))

	// Parse JSON-RPC response
	var jsonRPCResponse map[string]interface{}
	if err := json.Unmarshal(result, &jsonRPCResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON-RPC response: %w", err)
	}

	log.Printf("DEBUG: Parsed JSON-RPC response: %+v", jsonRPCResponse)

	// Extract the result field
	resultField, exists := jsonRPCResponse["result"]
	if !exists {
		return nil, fmt.Errorf("no result field in response")
	}

	log.Printf("DEBUG: Result field: %+v", resultField)

	// Convert result to JSON and then unmarshal as device array
	resultJSON, err := json.Marshal(resultField)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result field: %w", err)
	}

	var devices []map[string]interface{}
	if err := json.Unmarshal(resultJSON, &devices); err != nil {
		return nil, fmt.Errorf("failed to unmarshal VM device data: %w", err)
	}

	log.Printf("DEBUG: Final devices array: %+v", devices)
	return devices, nil
}

// QueryDatasets retrieves datasets from TrueNAS
func (c *WorkingClient) QueryDatasets(filters interface{}) ([]Dataset, error) {
	// Ensure filters is an array for JSON-RPC compatibility
	var params []interface{}
	if filters != nil {
		params = []interface{}{filters}
	} else {
		params = []interface{}{}
	}

	result, err := c.Call("pool.dataset.query", params, 30)
	if err != nil {
		return nil, fmt.Errorf("failed to query datasets: %w", err)
	}

	// Parse JSON-RPC response
	var jsonRPCResponse map[string]interface{}
	if err := json.Unmarshal(result, &jsonRPCResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON-RPC response: %w", err)
	}

	// Extract the result field
	resultField, exists := jsonRPCResponse["result"]
	if !exists {
		return nil, fmt.Errorf("no result field in response")
	}

	// Convert result to JSON and then unmarshal as dataset array
	resultJSON, err := json.Marshal(resultField)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result field: %w", err)
	}

	var datasets []Dataset
	if err := json.Unmarshal(resultJSON, &datasets); err != nil {
		return nil, fmt.Errorf("failed to unmarshal datasets: %w", err)
	}

	return datasets, nil
}

// CreateDataset creates a new dataset
func (c *WorkingClient) CreateDataset(datasetConfig DatasetCreateRequest) (*Dataset, error) {
	result, err := c.Call("pool.dataset.create", []interface{}{datasetConfig}, 60)
	if err != nil {
		return nil, fmt.Errorf("failed to create dataset: %w", err)
	}

	var dataset Dataset
	err = json.Unmarshal(result, &dataset)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal created dataset data: %w", err)
	}

	return &dataset, nil
}

// DeleteDataset deletes a dataset
func (c *WorkingClient) DeleteDataset(name string, recursive bool) error {
	// TrueNAS API expects the dataset ID (which is the full path) and options
	// The correct format for pool.dataset.delete is: (id, {"recursive": bool, "force": bool})
	// Note: recursive_snapshot is not a valid parameter for the API
	params := map[string]interface{}{
		"recursive": recursive,
		"force":     true,  // Force deletion even if dataset is busy
	}
	
	log.Printf("Attempting to delete dataset: %s with params: %+v", name, params)
	
	result, err := c.Call("pool.dataset.delete", []interface{}{name, params}, 120) // Increase timeout for large datasets
	if err != nil {
		log.Printf("Failed to delete dataset %s: %v", name, err)
		return fmt.Errorf("failed to delete dataset %s: %w", name, err)
	}
	
	log.Printf("Dataset deletion result for %s: %s", name, string(result))
	return nil
}

// GetVMBootloaderOptions gets available bootloader options
func (c *WorkingClient) GetVMBootloaderOptions() (interface{}, error) {
	result, err := c.Call("vm.bootloader_options", nil, 30)
	if err != nil {
		return nil, fmt.Errorf("failed to get bootloader options: %w", err)
	}

	var options interface{}
	err = json.Unmarshal(result, &options)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal bootloader options: %w", err)
	}

	return options, nil
}

func (c *WorkingClient) GetVMCPUModelChoices() (interface{}, error) {
	result, err := c.Call("vm.cpu_model_choices", nil, 30)
	if err != nil {
		return nil, fmt.Errorf("failed to get CPU model choices: %w", err)
	}

	var choices interface{}
	err = json.Unmarshal(result, &choices)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal CPU model choices: %w", err)
	}

	return choices, nil
}

func (c *WorkingClient) GetRandomMAC() (string, error) {
	result, err := c.Call("vm.random_mac", nil, 30)
	if err != nil {
		return "", fmt.Errorf("failed to get random MAC: %w", err)
	}

	var mac string
	err = json.Unmarshal(result, &mac)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal MAC address: %w", err)
	}

	return mac, nil
}

func (c *WorkingClient) GetAvailableMemory() (interface{}, error) {
	result, err := c.Call("vm.get_available_memory", nil, 30)
	if err != nil {
		return nil, fmt.Errorf("failed to get available memory: %w", err)
	}

	var memory interface{}
	err = json.Unmarshal(result, &memory)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal available memory: %w", err)
	}

	return memory, nil
}

func (c *WorkingClient) GetMaxSupportedVCPUs() (interface{}, error) {
	result, err := c.Call("vm.maximum_supported_vcpus", nil, 30)
	if err != nil {
		return nil, fmt.Errorf("failed to get max supported vCPUs: %w", err)
	}

	var vcpus interface{}
	err = json.Unmarshal(result, &vcpus)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal max supported vCPUs: %w", err)
	}

	return vcpus, nil
}

func (c *WorkingClient) GetDeviceDiskChoices() (interface{}, error) {
	result, err := c.Call("vm.device.disk_choices", nil, 30)
	if err != nil {
		return nil, fmt.Errorf("failed to get device disk choices: %w", err)
	}

	var choices interface{}
	err = json.Unmarshal(result, &choices)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal device disk choices: %w", err)
	}

	return choices, nil
}

func (c *WorkingClient) GetDeviceNICAttachChoices() (interface{}, error) {
	result, err := c.Call("vm.device.nic_attach_choices", nil, 30)
	if err != nil {
		return nil, fmt.Errorf("failed to get device NIC attach choices: %w", err)
	}

	var choices interface{}
	err = json.Unmarshal(result, &choices)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal device NIC attach choices: %w", err)
	}

	return choices, nil
}

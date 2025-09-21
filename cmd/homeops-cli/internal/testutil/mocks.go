package testutil

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// MockHTTPClient is a mock HTTP client for testing
type MockHTTPClient struct {
	Responses map[string]*http.Response
	Errors    map[string]error
	Calls     []string
}

// NewMockHTTPClient creates a new mock HTTP client
func NewMockHTTPClient() *MockHTTPClient {
	return &MockHTTPClient{
		Responses: make(map[string]*http.Response),
		Errors:    make(map[string]error),
		Calls:     []string{},
	}
}

// Do implements the HTTP client interface
func (m *MockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	url := req.URL.String()
	m.Calls = append(m.Calls, url)

	if err, exists := m.Errors[url]; exists {
		return nil, err
	}

	if resp, exists := m.Responses[url]; exists {
		return resp, nil
	}

	return &http.Response{
		StatusCode: 404,
		Body:       io.NopCloser(strings.NewReader("not found")),
	}, nil
}

// AddResponse adds a mock response for a URL
func (m *MockHTTPClient) AddResponse(url string, statusCode int, body string) {
	m.Responses[url] = &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// AddError adds a mock error for a URL
func (m *MockHTTPClient) AddError(url string, err error) {
	m.Errors[url] = err
}

// MockKubernetesClient creates a fake Kubernetes client for testing
func MockKubernetesClient(objects ...runtime.Object) kubernetes.Interface {
	return fake.NewSimpleClientset(objects...)
}

// CreateTestPod creates a test pod object
func CreateTestPod(name, namespace string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "test-container",
					Image: "test-image:latest",
				},
			},
		},
		Status: v1.PodStatus{
			Phase: v1.PodRunning,
		},
	}
}

// CreateTestNamespace creates a test namespace object
func CreateTestNamespace(name string) *v1.Namespace {
	return &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

// CreateTestPVC creates a test persistent volume claim
func CreateTestPVC(name, namespace string) *v1.PersistentVolumeClaim {
	return &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1.PersistentVolumeClaimSpec{
			AccessModes: []v1.PersistentVolumeAccessMode{
				v1.ReadWriteOnce,
			},
		},
	}
}

// MockTalosClient is a mock Talos client for testing
type MockTalosClient struct {
	Nodes          []string
	NodeConfigs    map[string]string
	ApplyConfigErr error
	UpgradeErr     error
	RebootErr      error
	ResetErr       error
}

// NewMockTalosClient creates a new mock Talos client
func NewMockTalosClient() *MockTalosClient {
	return &MockTalosClient{
		Nodes:       []string{"192.168.122.10", "192.168.122.11", "192.168.122.12"},
		NodeConfigs: make(map[string]string),
	}
}

// ApplyConfiguration mocks applying Talos configuration
func (m *MockTalosClient) ApplyConfiguration(ctx context.Context, node string, config []byte) error {
	if m.ApplyConfigErr != nil {
		return m.ApplyConfigErr
	}
	m.NodeConfigs[node] = string(config)
	return nil
}

// Upgrade mocks upgrading a node
func (m *MockTalosClient) Upgrade(ctx context.Context, node, image string) error {
	return m.UpgradeErr
}

// Reboot mocks rebooting a node
func (m *MockTalosClient) Reboot(ctx context.Context, node string) error {
	return m.RebootErr
}

// Reset mocks resetting a node
func (m *MockTalosClient) Reset(ctx context.Context, node string) error {
	return m.ResetErr
}

// MockTrueNASClient is a mock TrueNAS client for testing
type MockTrueNASClient struct {
	VMs          map[string]VMInfo
	CreateVMErr  error
	DeleteVMErr  error
	StartVMErr   error
	StopVMErr    error
	ISOUploadErr error
	UploadedISOs []string
}

// VMInfo represents VM information
type VMInfo struct {
	Name   string
	ID     int
	State  string
	CPUs   int
	Memory int
}

// NewMockTrueNASClient creates a new mock TrueNAS client
func NewMockTrueNASClient() *MockTrueNASClient {
	return &MockTrueNASClient{
		VMs:          make(map[string]VMInfo),
		UploadedISOs: make([]string, 0),
	}
}

// CreateVM mocks creating a VM
func (m *MockTrueNASClient) CreateVM(name string, cpus, memory int, iso string) error {
	if m.CreateVMErr != nil {
		return m.CreateVMErr
	}
	m.VMs[name] = VMInfo{
		Name:   name,
		ID:     len(m.VMs) + 1,
		State:  "stopped",
		CPUs:   cpus,
		Memory: memory,
	}
	return nil
}

// DeleteVM mocks deleting a VM
func (m *MockTrueNASClient) DeleteVM(name string) error {
	if m.DeleteVMErr != nil {
		return m.DeleteVMErr
	}
	delete(m.VMs, name)
	return nil
}

// StartVM mocks starting a VM
func (m *MockTrueNASClient) StartVM(name string) error {
	if m.StartVMErr != nil {
		return m.StartVMErr
	}
	if vm, exists := m.VMs[name]; exists {
		vm.State = "running"
		m.VMs[name] = vm
		return nil
	}
	return fmt.Errorf("VM %s not found", name)
}

// StopVM mocks stopping a VM
func (m *MockTrueNASClient) StopVM(name string) error {
	if m.StopVMErr != nil {
		return m.StopVMErr
	}
	if vm, exists := m.VMs[name]; exists {
		vm.State = "stopped"
		m.VMs[name] = vm
		return nil
	}
	return fmt.Errorf("VM %s not found", name)
}

// UploadISO mocks uploading an ISO
func (m *MockTrueNASClient) UploadISO(path, name string) error {
	if m.ISOUploadErr != nil {
		return m.ISOUploadErr
	}
	m.UploadedISOs = append(m.UploadedISOs, name)
	return nil
}

// Mock1PasswordClient is a mock 1Password client for testing
type Mock1PasswordClient struct {
	Secrets map[string]string
	Error   error
}

// NewMock1PasswordClient creates a new mock 1Password client
func NewMock1PasswordClient() *Mock1PasswordClient {
	return &Mock1PasswordClient{
		Secrets: map[string]string{
			"op://vault/item/field":       "secret-value",
			"op://vault/truenas/password": "truenas-password",
			"op://vault/spice/password":   "spice-password",
		},
	}
}

// GetSecret mocks getting a secret from 1Password
func (m *Mock1PasswordClient) GetSecret(reference string) (string, error) {
	if m.Error != nil {
		return "", m.Error
	}
	if secret, exists := m.Secrets[reference]; exists {
		return secret, nil
	}
	return "", fmt.Errorf("secret not found: %s", reference)
}

// MockCommandExecutor is a mock command executor for testing
type MockCommandExecutor struct {
	Commands []string
	Outputs  map[string]string
	Errors   map[string]error
}

// NewMockCommandExecutor creates a new mock command executor
func NewMockCommandExecutor() *MockCommandExecutor {
	return &MockCommandExecutor{
		Commands: make([]string, 0),
		Outputs:  make(map[string]string),
		Errors:   make(map[string]error),
	}
}

// Execute mocks executing a command
func (m *MockCommandExecutor) Execute(cmd string, args ...string) (string, error) {
	fullCmd := fmt.Sprintf("%s %s", cmd, strings.Join(args, " "))
	m.Commands = append(m.Commands, fullCmd)

	if err, exists := m.Errors[fullCmd]; exists {
		return "", err
	}

	if output, exists := m.Outputs[fullCmd]; exists {
		return output, nil
	}

	return "", nil
}

// AddOutput adds expected output for a command
func (m *MockCommandExecutor) AddOutput(cmd string, output string) {
	m.Outputs[cmd] = output
}

// AddError adds expected error for a command
func (m *MockCommandExecutor) AddError(cmd string, err error) {
	m.Errors[cmd] = err
}

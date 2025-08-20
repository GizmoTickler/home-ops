package talos

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"homeops-cli/internal/common"
	"homeops-cli/internal/templates"
	"gopkg.in/yaml.v3"
)

const (
	// TalosFactoryBaseURL is the base URL for the Talos factory API
	TalosFactoryBaseURL = "https://factory.talos.dev"
	
	// DefaultTalosVersion is the default Talos version to use if not specified
	DefaultTalosVersion = "v1.10.6"
	
	// CacheDir is the directory where generated ISOs are cached
	CacheDir = ".cache/talos-isos"
)

// FactoryClient handles interactions with the Talos factory API
type FactoryClient struct {
	baseURL    string
	httpClient *http.Client
	logger     *common.ColorLogger
	cacheDir   string
}

// SchematicConfig represents the Talos schematic configuration
type SchematicConfig struct {
	Customization struct {
		ExtraKernelArgs   []string `yaml:"extraKernelArgs" json:"extraKernelArgs"`
		SystemExtensions  struct {
			OfficialExtensions []string `yaml:"officialExtensions" json:"officialExtensions"`
		} `yaml:"systemExtensions" json:"systemExtensions"`
	} `yaml:"customization" json:"customization"`
}

// SchematicResponse represents the response from the Talos factory API
type SchematicResponse struct {
	ID      string `json:"id"`
	
}

// ISOGenerationRequest represents a request to generate an ISO
type ISOGenerationRequest struct {
	SchematicID   string
	TalosVersion  string
	Architecture  string
	Platform      string
}

// ISOInfo contains information about a generated ISO
type ISOInfo struct {
	URL          string
	SchematicID  string
	TalosVersion string
	CacheFile    string
	Hash         string
}

// NewFactoryClient creates a new Talos factory client
func NewFactoryClient() *FactoryClient {
	return &FactoryClient{
		baseURL: TalosFactoryBaseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger:   common.NewColorLogger(),
		cacheDir: CacheDir,
	}
}

// LoadSchematicFromFile loads a schematic configuration from a YAML file
func (fc *FactoryClient) LoadSchematicFromFile(filePath string) (*SchematicConfig, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read schematic file: %w", err)
	}

	var config SchematicConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse schematic YAML: %w", err)
	}

	return &config, nil
}

// LoadSchematicFromTemplate loads a schematic configuration from embedded templates
func (fc *FactoryClient) LoadSchematicFromTemplate() (*SchematicConfig, error) {
	schematicContent, err := templates.GetTalosTemplate("talos/schematic.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to load schematic template: %w", err)
	}

	var config SchematicConfig
	if err := yaml.Unmarshal([]byte(schematicContent), &config); err != nil {
		return nil, fmt.Errorf("failed to parse schematic YAML: %w", err)
	}

	// Validate the loaded schematic
	if err := fc.ValidateSchematic(&config); err != nil {
		return nil, fmt.Errorf("schematic validation failed: %w", err)
	}

	return &config, nil
}

// CreateSchematic creates a new schematic in the Talos factory
func (fc *FactoryClient) CreateSchematic(config *SchematicConfig, talosVersion string) (*SchematicResponse, error) {
	fc.logger.Info("Creating Talos schematic for version %s", talosVersion)

	// Validate input configuration
	if err := fc.ValidateSchematic(config); err != nil {
		return nil, fmt.Errorf("schematic validation failed: %w", err)
	}

	// Prepare the request payload
	payload := map[string]interface{}{
		"customization": config.Customization,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal schematic config: %w", err)
	}

	// Create the HTTP request
	url := fmt.Sprintf("%s/schematics", fc.baseURL)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "homeops-cli/1.0")

	// Send the request
	resp, err := fc.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Validate API response
	if err := fc.ValidateAPIResponse(resp, "application/json"); err != nil {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API validation failed: %w, response: %s", err, string(body))
	}

	// Parse the response
	var schematicResp SchematicResponse
	if err := json.NewDecoder(resp.Body).Decode(&schematicResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Validate schematic response
	if err := fc.ValidateSchematicResponse(&schematicResp); err != nil {
		return nil, fmt.Errorf("schematic response validation failed: %w", err)
	}

	fc.logger.Success("Created schematic with ID: %s", schematicResp.ID)
	return &schematicResp, nil
}

// GenerateISO generates a custom Talos ISO using the factory API
func (fc *FactoryClient) GenerateISO(req ISOGenerationRequest) (*ISOInfo, error) {
	// Validate input request
	if err := fc.validateISORequest(req); err != nil {
		return nil, fmt.Errorf("ISO request validation failed: %w", err)
	}

	fc.logger.Info("Generating Talos ISO for schematic %s (version: %s, platform: %s, arch: %s)", 
		req.SchematicID, req.TalosVersion, req.Platform, req.Architecture)

	// Check cache first
	if isoInfo, found := fc.checkCache(req); found {
		fc.logger.Info("Using cached ISO: %s", isoInfo.CacheFile)
		return isoInfo, nil
	}

	// Generate the ISO URL
	isoURL := fmt.Sprintf("%s/image/%s/%s/%s-%s.iso",
		fc.baseURL,
		req.SchematicID,
		req.TalosVersion,
		req.Platform,
		req.Architecture,
	)

	// Validate that the ISO URL is accessible (optional check)
	if err := fc.validateISOURL(isoURL); err != nil {
		fc.logger.Warn("ISO URL validation failed: %v", err)
	}

	// Create ISO info
	isoInfo := &ISOInfo{
		URL:          isoURL,
		SchematicID:  req.SchematicID,
		TalosVersion: req.TalosVersion,
		Hash:         fc.generateHash(req),
	}

	// Cache the ISO info
	if err := fc.cacheISOInfo(isoInfo); err != nil {
		fc.logger.Warn("Failed to cache ISO info: %v", err)
	} else {
		fc.logger.Debug("Successfully cached ISO info")
	}

	fc.logger.Success("Generated ISO URL: %s", isoURL)
	return isoInfo, nil
}

// validateISORequest validates an ISO generation request
func (fc *FactoryClient) validateISORequest(req ISOGenerationRequest) error {
	if req.SchematicID == "" {
		return fmt.Errorf("schematic ID is required")
	}
	if req.TalosVersion == "" {
		return fmt.Errorf("Talos version is required")
	}
	if req.Architecture == "" {
		return fmt.Errorf("architecture is required")
	}
	if req.Platform == "" {
		return fmt.Errorf("platform is required")
	}

	// Validate architecture
	validArchs := []string{"amd64", "arm64"}
	validArch := false
	for _, arch := range validArchs {
		if req.Architecture == arch {
			validArch = true
			break
		}
	}
	if !validArch {
		return fmt.Errorf("unsupported architecture: %s (supported: %v)", req.Architecture, validArchs)
	}

	// Validate platform
	validPlatforms := []string{"metal", "aws", "azure", "gcp", "vmware"}
	validPlatform := false
	for _, platform := range validPlatforms {
		if req.Platform == platform {
			validPlatform = true
			break
		}
	}
	if !validPlatform {
		return fmt.Errorf("unsupported platform: %s (supported: %v)", req.Platform, validPlatforms)
	}

	fc.logger.Debug("ISO request validation passed")
	return nil
}

// validateISOURL performs a HEAD request to check if the ISO URL is accessible
func (fc *FactoryClient) validateISOURL(url string) error {
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create HEAD request: %w", err)
	}

	req.Header.Set("User-Agent", "homeops-cli/1.0")

	resp, err := fc.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to validate ISO URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ISO URL returned status %d", resp.StatusCode)
	}

	fc.logger.Debug("ISO URL validation passed")
	return nil
}

// GenerateISOFromSchematic is a convenience method that creates a schematic and generates an ISO
func (fc *FactoryClient) GenerateISOFromSchematic(config *SchematicConfig, talosVersion, architecture, platform string) (*ISOInfo, error) {
	fc.logger.Info("Starting ISO generation from schematic (version: %s, platform: %s, arch: %s)", talosVersion, platform, architecture)

	// Validate input parameters
	if config == nil {
		return nil, fmt.Errorf("schematic config is nil")
	}
	if talosVersion == "" {
		return nil, fmt.Errorf("Talos version is required")
	}
	if architecture == "" {
		return nil, fmt.Errorf("architecture is required")
	}
	if platform == "" {
		return nil, fmt.Errorf("platform is required")
	}

	// Create the schematic first
	fc.logger.Debug("Creating schematic for ISO generation")
	schematicResp, err := fc.CreateSchematic(config, talosVersion)
	if err != nil {
		return nil, fmt.Errorf("failed to create schematic: %w", err)
	}

	fc.logger.Debug("Schematic created successfully with ID: %s", schematicResp.ID)

	// Generate the ISO
	req := ISOGenerationRequest{
		SchematicID:  schematicResp.ID,
		TalosVersion: talosVersion,
		Architecture: architecture,
		Platform:     platform,
	}

	fc.logger.Debug("Generating ISO from schematic")
	isoInfo, err := fc.GenerateISO(req)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ISO: %w", err)
	}

	fc.logger.Success("Successfully generated ISO from schematic: %s", isoInfo.URL)
	return isoInfo, nil
}

// generateHash creates a hash for caching purposes
func (fc *FactoryClient) generateHash(req ISOGenerationRequest) string {
	data := fmt.Sprintf("%s-%s-%s-%s", req.SchematicID, req.TalosVersion, req.Architecture, req.Platform)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// checkCache checks if an ISO is already cached
func (fc *FactoryClient) checkCache(req ISOGenerationRequest) (*ISOInfo, bool) {
	hash := fc.generateHash(req)
	cacheFile := filepath.Join(fc.cacheDir, fmt.Sprintf("%s.json", hash))

	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		return nil, false
	}

	data, err := os.ReadFile(cacheFile)
	if err != nil {
		return nil, false
	}

	var isoInfo ISOInfo
	if err := json.Unmarshal(data, &isoInfo); err != nil {
		return nil, false
	}

	return &isoInfo, true
}

// cacheISOInfo caches ISO information
func (fc *FactoryClient) cacheISOInfo(isoInfo *ISOInfo) error {
	// Ensure cache directory exists
	if err := os.MkdirAll(fc.cacheDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	// Set cache file path
	isoInfo.CacheFile = filepath.Join(fc.cacheDir, fmt.Sprintf("%s.json", isoInfo.Hash))

	// Marshal and write to cache
	data, err := json.MarshalIndent(isoInfo, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal ISO info: %w", err)
	}

	if err := os.WriteFile(isoInfo.CacheFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	return nil
}

// ValidateSchematic validates a schematic configuration
func (fc *FactoryClient) ValidateSchematic(config *SchematicConfig) error {
	if config == nil {
		return fmt.Errorf("schematic config is nil")
	}

	// Validate system extensions
	for i, ext := range config.Customization.SystemExtensions.OfficialExtensions {
		if ext == "" {
			return fmt.Errorf("empty system extension found at index %d", i)
		}
		// Basic format validation for system extensions
		if len(ext) < 3 {
			return fmt.Errorf("system extension '%s' appears to be too short", ext)
		}
	}

	// Validate kernel args
	for i, arg := range config.Customization.ExtraKernelArgs {
		if arg == "" {
			return fmt.Errorf("empty kernel argument found at index %d", i)
		}
		// Basic validation for kernel arguments format
		if !strings.Contains(arg, "=") && !isValidKernelFlag(arg) {
			fc.logger.Warn("Kernel argument '%s' may not be in expected format", arg)
		}
	}

	fc.logger.Debug("Schematic validation passed")
	return nil
}

// isValidKernelFlag checks if a kernel argument is a valid flag (without =)
func isValidKernelFlag(arg string) bool {
	validFlags := []string{"quiet", "splash", "nomodeset", "acpi_enforce_resources", "pci"}
	for _, flag := range validFlags {
		if strings.HasPrefix(arg, flag) {
			return true
		}
	}
	return false
}

// ValidateAPIResponse validates responses from the Talos factory API
func (fc *FactoryClient) ValidateAPIResponse(resp *http.Response, expectedContentType string) error {
	if resp == nil {
		return fmt.Errorf("response is nil")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, resp.Status)
	}

	contentType := resp.Header.Get("Content-Type")
	if expectedContentType != "" && !strings.Contains(contentType, expectedContentType) {
		fc.logger.Warn("Unexpected content type: got %s, expected %s", contentType, expectedContentType)
	}

	return nil
}

// ValidateSchematicResponse validates a schematic creation response
func (fc *FactoryClient) ValidateSchematicResponse(resp *SchematicResponse) error {
	if resp == nil {
		return fmt.Errorf("schematic response is nil")
	}

	if resp.ID == "" {
		return fmt.Errorf("schematic ID is empty")
	}


	// Validate ID format (should be a hash-like string)
	if len(resp.ID) < 10 {
		return fmt.Errorf("schematic ID '%s' appears to be too short", resp.ID)
	}

	fc.logger.Debug("Schematic response validation passed: ID=%s", resp.ID)
	return nil
}

// ClearCache clears the ISO cache
func (fc *FactoryClient) ClearCache() error {
	if err := os.RemoveAll(fc.cacheDir); err != nil {
		return fmt.Errorf("failed to clear cache: %w", err)
	}
	fc.logger.Success("Cache cleared successfully")
	return nil
}
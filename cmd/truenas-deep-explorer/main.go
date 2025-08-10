package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"truenas-deep-explorer/truenas"
)

type Config struct {
	TrueNASHost   string
	TrueNASAPIKey string
	OutputFile    string
	NoSSL         bool
}

// APIMethodInfo represents information about an API method
type APIMethodInfo struct {
	Method      string
	Description string
	Parameters  interface{}
	Example     interface{}
	Response    interface{}
}

// getTrueNASCredentials retrieves TrueNAS credentials from 1Password or environment variables
func getTrueNASCredentials() (host, apiKey string, err error) {
	// Try 1Password first
	host = get1PasswordSecret("op://Infrastructure/talosdeploy/TRUENAS_HOST")
	apiKey = get1PasswordSecret("op://Infrastructure/talosdeploy/TRUENAS_API")
	
	// Fall back to environment variables if 1Password fails
	if host == "" {
		host = os.Getenv("TRUENAS_HOST")
	}
	if apiKey == "" {
		apiKey = os.Getenv("TRUENAS_API_KEY")
	}
	
	// Check if we have both credentials
	if host == "" || apiKey == "" {
		return "", "", fmt.Errorf("TrueNAS credentials not found. Please set TRUENAS_HOST and TRUENAS_API_KEY environment variables or configure 1Password with 'op://Infrastructure/talosdeploy/TRUENAS_HOST' and 'op://Infrastructure/talosdeploy/TRUENAS_API'")
	}
	
	return host, apiKey, nil
}

// get1PasswordSecret retrieves a secret from 1Password using the op CLI
func get1PasswordSecret(reference string) string {
	cmd := exec.Command("op", "read", reference)
	output, err := cmd.Output()
	if err != nil {
		// Silently fail and return empty string to allow fallback to env vars
		return ""
	}
	return strings.TrimSpace(string(output))
}

func main() {
	var config Config
	flag.StringVar(&config.TrueNASHost, "truenas-host", "", "TrueNAS hostname or IP (optional if using 1Password)")
	flag.StringVar(&config.TrueNASAPIKey, "truenas-api-key", "", "TrueNAS API key (optional if using 1Password)")
	flag.StringVar(&config.OutputFile, "output", "docs/TRUENAS-VM-DEEP-API.md", "Output markdown file")
	flag.BoolVar(&config.NoSSL, "no-ssl", false, "Use ws:// instead of wss://")
	flag.Parse()

	// Get credentials from 1Password or environment variables if not provided via flags
	if config.TrueNASHost == "" || config.TrueNASAPIKey == "" {
		host, apiKey, err := getTrueNASCredentials()
		if err != nil {
			log.Fatal(err)
		}
		if config.TrueNASHost == "" {
			config.TrueNASHost = host
		}
		if config.TrueNASAPIKey == "" {
			config.TrueNASAPIKey = apiKey
		}
	}

	if config.TrueNASHost == "" || config.TrueNASAPIKey == "" {
		log.Fatal("TrueNAS host and API key are required")
	}

	log.Printf("Deep exploring TrueNAS VM API...")

	// Connect to TrueNAS using the working client
	client := truenas.NewWorkingClient(config.TrueNASHost, config.TrueNASAPIKey, 443, !config.NoSSL)
	err := client.Connect()
	if err != nil {
		log.Fatalf("Failed to connect to TrueNAS: %v", err)
	}
	defer client.Close()

	var methods []APIMethodInfo

	// Explore core VM methods with real API calls
	vmMethods := []string{
		"vm.query",
		"vm.create",
		"vm.update",
		"vm.delete",
		"vm.start",
		"vm.stop",
		"vm.status",
		"vm.get_instance",
		"vm.bootloader_options",
		"vm.cpu_model_choices",
		"vm.random_mac",
		"vm.resolution_choices",
		"vm.get_available_memory",
		"vm.maximum_supported_vcpus",
	}

	// Explore VM device methods
	deviceMethods := []string{
		"vm.device.query",
		"vm.device.create",
		"vm.device.update",
		"vm.device.delete",
		"vm.device.disk_choices",
		"vm.device.nic_attach_choices",
		"vm.device.bind_choices",
		"vm.device.iotype_choices",
	}

	// Explore dataset methods (for ZVol management)
	datasetMethods := []string{
		"pool.dataset.query",
		"pool.dataset.create",
		"pool.dataset.delete",
	}

	allMethods := append(vmMethods, deviceMethods...)
	allMethods = append(allMethods, datasetMethods...)

	for _, method := range allMethods {
		log.Printf("Exploring method: %s", method)
		methodInfo, err := exploreMethod(client, method)
		if err != nil {
			log.Printf("Error exploring %s: %v", method, err)
			continue
		}
		methods = append(methods, methodInfo)
		time.Sleep(200 * time.Millisecond) // Be nice to the API
	}

	// Get real VM examples if any exist
	log.Printf("Querying existing VMs for real examples...")
	vmExamples, err := getVMExamples(client)
	if err != nil {
		log.Printf("Could not get VM examples: %v", err)
	}

	// Get real dataset examples
	log.Printf("Querying datasets for real examples...")
	datasetExamples, err := getDatasetExamples(client)
	if err != nil {
		log.Printf("Could not get dataset examples: %v", err)
	}

	// Generate comprehensive documentation
	err = generateDeepMarkdown(methods, vmExamples, datasetExamples, config.OutputFile)
	if err != nil {
		log.Fatalf("Error generating markdown: %v", err)
	}

	log.Printf("Generated deep API documentation: %s", config.OutputFile)
	log.Printf("Explored %d methods", len(methods))
}

// exploreMethod attempts to call an API method and gather information about it
func exploreMethod(client *truenas.WorkingClient, method string) (APIMethodInfo, error) {
	methodInfo := APIMethodInfo{
		Method: method,
	}

	// Try calling with no parameters first
	result, err := client.Call(method, []interface{}{}, 30)
	if err != nil {
		// Store the error as an example - it often contains useful parameter info
		methodInfo.Example = err.Error()
		return methodInfo, nil
	}

	// If successful, store the response
	var response interface{}
	if err := json.Unmarshal(result, &response); err == nil {
		methodInfo.Response = response
	}

	// Try some common parameter patterns for methods that might need them
	if strings.Contains(method, "create") || strings.Contains(method, "update") {
		// These methods typically need parameters, so the error above is likely informative
		return methodInfo, nil
	}

	// For query methods, try with empty filter
	if strings.Contains(method, "query") {
		result, err := client.Call(method, []interface{}{map[string]interface{}{}}, 30)
		if err == nil {
			var response interface{}
			if err := json.Unmarshal(result, &response); err == nil {
				methodInfo.Response = response
				methodInfo.Parameters = map[string]interface{}{}
			}
		}
	}

	// For choice methods, they usually don't need parameters
	if strings.Contains(method, "choices") || strings.Contains(method, "options") {
		result, err := client.Call(method, []interface{}{}, 30)
		if err == nil {
			var response interface{}
			if err := json.Unmarshal(result, &response); err == nil {
				methodInfo.Response = response
			}
		}
	}

	return methodInfo, nil
}

func getVMExamples(client *truenas.WorkingClient) ([]interface{}, error) {
	// Try to get existing VMs as examples
	result, err := client.Call("vm.query", []interface{}{}, 30)
	if err != nil {
		return nil, err
	}

	// Parse the JSON-RPC response
	var jsonRPCResponse map[string]interface{}
	if err := json.Unmarshal(result, &jsonRPCResponse); err != nil {
		return nil, err
	}

	// Extract the result field
	resultField, exists := jsonRPCResponse["result"]
	if !exists {
		return nil, fmt.Errorf("no result field in response")
	}

	// Convert to slice of interfaces
	if vms, ok := resultField.([]interface{}); ok {
		return vms, nil
	}

	return nil, fmt.Errorf("unexpected result format")
}

func getDatasetExamples(client *truenas.WorkingClient) ([]interface{}, error) {
	// Try to get existing datasets as examples
	result, err := client.Call("pool.dataset.query", []interface{}{}, 30)
	if err != nil {
		return nil, err
	}

	// Parse the JSON-RPC response
	var jsonRPCResponse map[string]interface{}
	if err := json.Unmarshal(result, &jsonRPCResponse); err != nil {
		return nil, err
	}

	// Extract the result field
	resultField, exists := jsonRPCResponse["result"]
	if !exists {
		return nil, fmt.Errorf("no result field in response")
	}

	// Convert to slice of interfaces
	if datasets, ok := resultField.([]interface{}); ok {
		return datasets, nil
	}

	return nil, fmt.Errorf("unexpected result format")
}

func generateDeepMarkdown(methods []APIMethodInfo, vmExamples, datasetExamples []interface{}, outputFile string) error {
	var md strings.Builder

	md.WriteString("# TrueNAS VM API Deep Reference\n\n")
	md.WriteString("*Generated by deep API exploration with real API calls*\n\n")
	md.WriteString(fmt.Sprintf("**Generated on:** %s\n\n", time.Now().Format("2006-01-02 15:04:05")))

	// Table of Contents
	md.WriteString("## Table of Contents\n\n")
	md.WriteString("1. [Real VM Examples](#real-vm-examples)\n")
	md.WriteString("2. [Real Dataset Examples](#real-dataset-examples)\n")
	md.WriteString("3. [API Method Details](#api-method-details)\n")
	md.WriteString("4. [Device Structure Analysis](#device-structure-analysis)\n\n")

	// Real VM Examples
	md.WriteString("## Real VM Examples\n\n")
	if len(vmExamples) > 0 {
		md.WriteString("These are actual VMs from the TrueNAS system:\n\n")
		for i, vm := range vmExamples {
			if i >= 3 { // Limit to first 3 VMs
				break
			}
			vmJSON, _ := json.MarshalIndent(vm, "", "  ")
			md.WriteString(fmt.Sprintf("### VM Example %d\n\n", i+1))
			md.WriteString("```json\n")
			md.WriteString(string(vmJSON))
			md.WriteString("\n```\n\n")
		}
	} else {
		md.WriteString("No VMs found in the system.\n\n")
	}

	// Real Dataset Examples
	md.WriteString("## Real Dataset Examples\n\n")
	if len(datasetExamples) > 0 {
		md.WriteString("These are actual datasets from the TrueNAS system:\n\n")
		for i, dataset := range datasetExamples {
			if i >= 5 { // Limit to first 5 datasets
				break
			}
			datasetJSON, _ := json.MarshalIndent(dataset, "", "  ")
			md.WriteString(fmt.Sprintf("### Dataset Example %d\n\n", i+1))
			md.WriteString("```json\n")
			md.WriteString(string(datasetJSON))
			md.WriteString("\n```\n\n")
		}
	} else {
		md.WriteString("No datasets found in the system.\n\n")
	}

	// API Method Details
	md.WriteString("## API Method Details\n\n")
	for _, method := range methods {
		md.WriteString(fmt.Sprintf("### %s\n\n", method.Method))

		if method.Response != nil {
			responseJSON, _ := json.MarshalIndent(method.Response, "", "  ")
			md.WriteString("**Successful Response:**\n\n")
			md.WriteString("```json\n")
			md.WriteString(string(responseJSON))
			md.WriteString("\n```\n\n")

			// Show the parameters that worked
			if method.Parameters != nil {
				paramsJSON, _ := json.MarshalIndent(method.Parameters, "", "  ")
				md.WriteString("**Working Parameters:**\n\n")
				md.WriteString("```json\n")
				md.WriteString(string(paramsJSON))
				md.WriteString("\n```\n\n")
			}
		}

		if method.Example != nil {
			// Determine if this is an error or parameter discovery
			exampleStr := fmt.Sprintf("%v", method.Example)
			if strings.Contains(exampleStr, "missing") ||
			   strings.Contains(exampleStr, "required") {
				md.WriteString("**Parameter Requirements (discovered from errors):**\n\n")
			} else {
				md.WriteString("**Error/Additional Info:**\n\n")
			}

			md.WriteString("```\n")
			md.WriteString(exampleStr)
			md.WriteString("\n```\n\n")
		}

		md.WriteString("---\n\n")
	}

	return os.WriteFile(outputFile, []byte(md.String()), 0644)
}
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"truenas-vm-tools/truenas"
)

type Config struct {
	TrueNASHost   string
	TrueNASAPIKey string
	OutputFile    string
	NoSSL         bool
}



type APIMethodInfo struct {
	Method      string
	Description string
	Parameters  interface{}
	Example     interface{}
	Response    interface{}
}

func main() {
	var config Config
	flag.StringVar(&config.TrueNASHost, "truenas-host", "", "TrueNAS hostname or IP")
	flag.StringVar(&config.TrueNASAPIKey, "truenas-api-key", "", "TrueNAS API key")
	flag.StringVar(&config.OutputFile, "output", "docs/TRUENAS-VM-DEEP-API.md", "Output markdown file")
	flag.BoolVar(&config.NoSSL, "no-ssl", false, "Use ws:// instead of wss://")
	flag.Parse()

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



func exploreMethod(client *truenas.WorkingClient, method string) (APIMethodInfo, error) {
	methodInfo := APIMethodInfo{
		Method: method,
	}

	// Try different parameter combinations to understand the method
	testCases := []interface{}{
		[]interface{}{}, // Empty array
		[]interface{}{nil}, // Array with null
	}

	// For methods that we know need specific parameters, try some examples
	if strings.Contains(method, ".create") {
		// Try with a sample data object
		testCases = append(testCases, []interface{}{map[string]interface{}{
			"name": "test-vm",
			"memory": 1024,
			"vcpus": 1,
		}})
	} else if strings.Contains(method, ".update") {
		// Try with ID and data
		testCases = append(testCases, []interface{}{1, map[string]interface{}{
			"name": "updated-vm",
		}})
	} else if strings.Contains(method, ".delete") {
		// Try with just an ID
		testCases = append(testCases, []interface{}{999}) // Use non-existent ID
	} else if strings.Contains(method, ".start") || strings.Contains(method, ".stop") {
		// Try with just an ID
		testCases = append(testCases, []interface{}{999}) // Use non-existent ID
	}

	var lastError string
	for _, params := range testCases {
		// Use the working client to make the call
		result, err := client.Call(method, params, 10)
		if err != nil {
			lastError = err.Error()
			// Check if this is a "method not found" vs "wrong parameters" error
			if strings.Contains(err.Error(), "missing") ||
			   strings.Contains(err.Error(), "required") ||
			   strings.Contains(err.Error(), "does not exist") ||
			   strings.Contains(err.Error(), "not found") {
				// This is parameter discovery - record it but continue trying
				if methodInfo.Example == nil {
					methodInfo.Example = err.Error()
				}
				continue
			} else {
				// This might be a different kind of error, record it
				if methodInfo.Example == nil {
					methodInfo.Example = err.Error()
				}
			}
			continue
		}

		// Check if the result contains an error (even though the call didn't fail)
		var resultMap map[string]interface{}
		if err := json.Unmarshal(result, &resultMap); err == nil {
			if errorField, exists := resultMap["error"]; exists && errorField != nil {
				// This is an error response, treat it as parameter discovery
				if methodInfo.Example == nil {
					if errorMap, ok := errorField.(map[string]interface{}); ok {
						if reason, exists := errorMap["data"].(map[string]interface{})["reason"]; exists {
							methodInfo.Example = fmt.Sprintf("%v", reason)
						} else {
							methodInfo.Example = fmt.Sprintf("API Error: %v", errorField)
						}
					}
				}
				continue
			}
		}

		// If we got a truly successful response, record it
		methodInfo.Response = result
		methodInfo.Parameters = params
		break
	}

	// If we didn't get a successful response, record the last error
	if methodInfo.Response == nil && methodInfo.Example == nil && lastError != "" {
		methodInfo.Example = lastError
	}

	return methodInfo, nil
}

func getVMExamples(client *truenas.WorkingClient) ([]interface{}, error) {
	result, err := client.Call("vm.query", []interface{}{}, 30)
	if err != nil {
		return nil, err
	}

	// First try to unmarshal as the full JSON-RPC response
	var jsonRPCResponse map[string]interface{}
	if err := json.Unmarshal(result, &jsonRPCResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON-RPC response: %w", err)
	}

	// Extract the result field
	resultField, exists := jsonRPCResponse["result"]
	if !exists {
		return nil, fmt.Errorf("no result field in response")
	}

	// Convert result to JSON and then unmarshal as array
	resultJSON, err := json.Marshal(resultField)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result field: %w", err)
	}

	var vms []interface{}
	if err := json.Unmarshal(resultJSON, &vms); err != nil {
		// If it's not an array, try to handle it as a single object
		var singleVM interface{}
		if err2 := json.Unmarshal(resultJSON, &singleVM); err2 != nil {
			return nil, fmt.Errorf("failed to unmarshal as array or object: %w", err)
		}
		return []interface{}{singleVM}, nil
	}

	return vms, nil
}

func getDatasetExamples(client *truenas.WorkingClient) ([]interface{}, error) {
	result, err := client.Call("pool.dataset.query", []interface{}{}, 30)
	if err != nil {
		return nil, err
	}

	// First try to unmarshal as the full JSON-RPC response
	var jsonRPCResponse map[string]interface{}
	if err := json.Unmarshal(result, &jsonRPCResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON-RPC response: %w", err)
	}

	// Extract the result field
	resultField, exists := jsonRPCResponse["result"]
	if !exists {
		return nil, fmt.Errorf("no result field in response")
	}

	// Convert result to JSON and then unmarshal as array
	resultJSON, err := json.Marshal(resultField)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal result field: %w", err)
	}

	var datasets []interface{}
	if err := json.Unmarshal(resultJSON, &datasets); err != nil {
		// If it's not an array, try to handle it as a single object
		var singleDataset interface{}
		if err2 := json.Unmarshal(resultJSON, &singleDataset); err2 != nil {
			return nil, fmt.Errorf("failed to unmarshal as array or object: %w", err)
		}
		return []interface{}{singleDataset}, nil
	}

	return datasets, nil
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

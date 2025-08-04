package main

import (
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"truenas-vm-tools/truenas"
)

type Config struct {
	Action        string
	Name          string
	TrueNASHost   string
	TrueNASAPIKey string
	TrueNASPort   int
	NoSSL         bool
	Force         bool
	KeepZVol      bool
	StoragePool   string
}

func main() {
	var config Config

	// Parse command line flags
	flag.StringVar(&config.Action, "action", "", "Action to perform: list, start, stop, delete, info")
	flag.StringVar(&config.Name, "name", "", "VM name (required for start, stop, delete, info)")
	flag.StringVar(&config.TrueNASHost, "truenas-host", "", "TrueNAS hostname or IP (required)")
	flag.StringVar(&config.TrueNASAPIKey, "truenas-api-key", "", "TrueNAS API key (required)")
	flag.IntVar(&config.TrueNASPort, "truenas-port", 443, "TrueNAS port")
	flag.BoolVar(&config.NoSSL, "no-ssl", false, "Disable SSL/TLS")
	flag.BoolVar(&config.Force, "force", false, "Force stop VM (for stop action)")
	flag.BoolVar(&config.KeepZVol, "keep-zvol", false, "Keep ZVol when deleting VM")
	flag.StringVar(&config.StoragePool, "storage-pool", "tank", "Storage pool name")

	flag.Parse()

	// Validate required flags
	if config.Action == "" {
		log.Fatal("Action is required (list, start, stop, delete, info)")
	}
	if config.TrueNASHost == "" {
		log.Fatal("TrueNAS host is required")
	}
	if config.TrueNASAPIKey == "" {
		log.Fatal("TrueNAS API key is required")
	}

	// Validate action-specific requirements
	if (config.Action == "start" || config.Action == "stop" || config.Action == "delete" || config.Action == "info") && config.Name == "" {
		log.Fatalf("VM name is required for %s action", config.Action)
	}

	// Create TrueNAS client using the working client
	client := truenas.NewWorkingClient(config.TrueNASHost, config.TrueNASAPIKey, config.TrueNASPort, !config.NoSSL)

	// Connect to TrueNAS
	if err := client.Connect(); err != nil {
		log.Fatalf("Failed to connect to TrueNAS: %v", err)
	}
	defer client.Close()

	// Perform the requested action
	var err error
	switch config.Action {
	case "list":
		err = listVMs(client)
	case "start":
		err = startVM(client, config.Name)
	case "stop":
		err = stopVM(client, config.Name, config.Force)
	case "delete":
		err = deleteVM(client, config.Name, !config.KeepZVol, config.StoragePool)
	case "info":
		err = getVMInfo(client, config.Name)
	default:
		log.Fatalf("Unknown action: %s", config.Action)
	}

	if err != nil {
		log.Fatalf("Action failed: %v", err)
	}
}

func listVMs(client *truenas.WorkingClient) error {
	vms, err := client.QueryVMs(nil)
	if err != nil {
		return fmt.Errorf("failed to query VMs: %w", err)
	}

	if len(vms) == 0 {
		fmt.Println("No virtual machines found.")
		return nil
	}

	fmt.Printf("%-20s %-5s %-10s %-8s %-6s %-10s\n", "Name", "ID", "Status", "Memory", "vCPUs", "Autostart")
	fmt.Println(strings.Repeat("-", 70))

	for _, vm := range vms {
		status := "Stopped"
		if state, ok := vm.Status["state"].(string); ok && state == "RUNNING" {
			status = "Running"
		}

		autostart := "No"
		if vm.Autostart {
			autostart = "Yes"
		}

		fmt.Printf("%-20s %-5d %-10s %-8d %-6d %-10s\n",
			vm.Name, vm.ID, status, vm.Memory, vm.VCPUs, autostart)
	}

	return nil
}

func getVMByName(client *truenas.WorkingClient, name string) (*truenas.VM, error) {
	// Query all VMs and filter by name
	allVMs, err := client.QueryVMs(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query VMs: %w", err)
	}

	// Find VM with matching name
	for _, vm := range allVMs {
		if vm.Name == name {
			return &vm, nil
		}
	}

	return nil, fmt.Errorf("VM %s not found", name)
}

func startVM(client *truenas.WorkingClient, name string) error {
	vm, err := getVMByName(client, name)
	if err != nil {
		return err
	}

	// Check current status
	if state, ok := vm.Status["state"].(string); ok && state == "RUNNING" {
		log.Printf("VM %s is already running", name)
		return nil
	}

	log.Printf("Starting VM %s (ID: %d)", name, vm.ID)
	if err := client.StartVM(vm.ID); err != nil {
		return fmt.Errorf("failed to start VM %s: %w", name, err)
	}

	log.Printf("Successfully started VM %s", name)
	return nil
}

func stopVM(client *truenas.WorkingClient, name string, force bool) error {
	vm, err := getVMByName(client, name)
	if err != nil {
		return err
	}

	// Check current status
	if state, ok := vm.Status["state"].(string); ok && state != "RUNNING" {
		log.Printf("VM %s is not running", name)
		return nil
	}

	action := "Gracefully stopping"
	if force {
		action = "Force stopping"
	}

	log.Printf("%s VM %s (ID: %d)", action, name, vm.ID)
	if err := client.StopVM(vm.ID); err != nil {
		return fmt.Errorf("failed to stop VM %s: %w", name, err)
	}

	log.Printf("Successfully stopped VM %s", name)
	return nil
}

func deleteVM(client *truenas.WorkingClient, name string, deleteZVol bool, storagePool string) error {
	vm, err := getVMByName(client, name)
	if err != nil {
		return err
	}

	// Stop VM if running
	if state, ok := vm.Status["state"].(string); ok && state == "RUNNING" {
		log.Printf("Stopping VM %s before deletion", name)
		if err := stopVM(client, name, true); err != nil {
			return fmt.Errorf("failed to stop VM before deletion: %w", err)
		}
		// Give it a moment to stop
		time.Sleep(2 * time.Second)
	}

	// Delete the VM
	log.Printf("Deleting VM %s (ID: %d)", name, vm.ID)
	if err := client.DeleteVM(vm.ID); err != nil {
		return fmt.Errorf("failed to delete VM %s: %w", name, err)
	}

	// Delete associated ZVol if requested
	if deleteZVol {
		zvolName := fmt.Sprintf("%s/vms/%s", storagePool, name)
		log.Printf("Deleting ZVol %s", zvolName)
		if err := client.DeleteDataset(zvolName, true); err != nil {
			log.Printf("Warning: Failed to delete ZVol %s: %v", zvolName, err)
		} else {
			log.Printf("Deleted ZVol %s", zvolName)
		}
	}

	log.Printf("Successfully deleted VM %s", name)
	return nil
}

func getVMInfo(client *truenas.WorkingClient, name string) error {
	vm, err := getVMByName(client, name)
	if err != nil {
		return err
	}

	fmt.Printf("VM Information: %s\n", name)
	fmt.Println(strings.Repeat("=", 40))
	fmt.Printf("ID: %d\n", vm.ID)
	fmt.Printf("Name: %s\n", vm.Name)
	fmt.Printf("Description: %s\n", vm.Description)
	fmt.Printf("Memory: %d MB\n", vm.Memory)
	fmt.Printf("vCPUs: %d\n", vm.VCPUs)
	fmt.Printf("Bootloader: %s\n", vm.Bootloader)
	fmt.Printf("Autostart: %t\n", vm.Autostart)

	if state, ok := vm.Status["state"].(string); ok {
		fmt.Printf("Status: %s\n", state)
	}

	if pid, ok := vm.Status["pid"].(float64); ok {
		fmt.Printf("Process ID: %.0f\n", pid)
	}

	fmt.Println("\nDevices:")
	for i, device := range vm.Devices {
		fmt.Printf("  %d. Device: %+v\n", i+1, device)
	}

	return nil
}

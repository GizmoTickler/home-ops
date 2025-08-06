package main

import (
	"encoding/json"
	"flag"
	"log"

	"truenas-vm-tools/truenas"
)

func main() {
	var host = flag.String("truenas-host", "", "TrueNAS hostname or IP")
	var apiKey = flag.String("truenas-api-key", "", "TrueNAS API key")
	var port = flag.Int("port", 443, "TrueNAS port")
	var noSSL = flag.Bool("no-ssl", false, "Use HTTP instead of HTTPS")
	flag.Parse()

	if *host == "" || *apiKey == "" {
		log.Fatal("TrueNAS host and API key are required")
	}

	log.Printf("Testing TrueNAS Working Client...")

	// Create client
	client := truenas.NewWorkingClient(*host, *apiKey, *port, !*noSSL)

	// Connect and authenticate
	if err := client.Connect(); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	log.Printf("✅ Connection and authentication successful!")

	// Test VM query
	log.Printf("Testing VM query...")
	vms, err := client.QueryVMs(nil)
	if err != nil {
		log.Printf("❌ VM query failed: %v", err)
	} else {
		log.Printf("✅ VM query successful! Found %d VMs", len(vms))
		for _, vm := range vms {
			log.Printf("  - VM: %s (ID: %d, Status: %v)", vm.Name, vm.ID, vm.Status)
		}
	}

	// Test dataset query
	log.Printf("Testing dataset query...")
	datasets, err := client.QueryDatasets(nil)
	if err != nil {
		log.Printf("❌ Dataset query failed: %v", err)
	} else {
		log.Printf("✅ Dataset query successful! Found %d datasets", len(datasets))
		for i, dataset := range datasets {
			if i >= 5 { // Limit output
				log.Printf("  ... and %d more datasets", len(datasets)-5)
				break
			}
			log.Printf("  - Dataset: %s (Type: %s)", dataset.Name, dataset.Type)
		}
	}

	// Test VM configuration choices
	log.Printf("Testing VM configuration choices...")

	// Test bootloader options
	bootloaderOpts, err := client.GetVMBootloaderOptions()
	if err != nil {
		log.Printf("❌ Bootloader options failed: %v", err)
	} else {
		log.Printf("✅ Bootloader options successful!")
		if opts, ok := bootloaderOpts.([]interface{}); ok {
			log.Printf("  Available bootloaders: %v", opts)
		}
	}

	// Test CPU model choices
	cpuModels, err := client.GetVMCPUModelChoices()
	if err != nil {
		log.Printf("❌ CPU model choices failed: %v", err)
	} else {
		log.Printf("✅ CPU model choices successful!")
		if models, ok := cpuModels.(map[string]interface{}); ok {
			log.Printf("  Found %d CPU models", len(models))
		}
	}

	// Test random MAC generation
	mac, err := client.GetRandomMAC()
	if err != nil {
		log.Printf("❌ Random MAC generation failed: %v", err)
	} else {
		log.Printf("✅ Random MAC generation successful: %s", mac)
	}

	// Test available memory
	memory, err := client.GetAvailableMemory()
	if err != nil {
		log.Printf("❌ Available memory query failed: %v", err)
	} else {
		log.Printf("✅ Available memory query successful!")
		if memBytes, err := json.Marshal(memory); err == nil {
			log.Printf("  Memory info: %s", string(memBytes))
		}
	}

	// Test max VCPUs
	maxVCPUs, err := client.GetMaxSupportedVCPUs()
	if err != nil {
		log.Printf("❌ Max VCPUs query failed: %v", err)
	} else {
		log.Printf("✅ Max VCPUs query successful: %v", maxVCPUs)
	}

	// Test device choices
	log.Printf("Testing device configuration choices...")

	diskChoices, err := client.GetDeviceDiskChoices()
	if err != nil {
		log.Printf("❌ Disk choices failed: %v", err)
	} else {
		log.Printf("✅ Disk choices successful!")
		if choices, ok := diskChoices.(map[string]interface{}); ok {
			log.Printf("  Found %d disk options", len(choices))
		}
	}

	nicChoices, err := client.GetDeviceNICAttachChoices()
	if err != nil {
		log.Printf("❌ NIC attach choices failed: %v", err)
	} else {
		log.Printf("✅ NIC attach choices successful!")
		if choices, ok := nicChoices.(map[string]interface{}); ok {
			log.Printf("  Found %d network interfaces", len(choices))
			for name := range choices {
				log.Printf("    - %s", name)
			}
		}
	}

	log.Printf("🎉 All tests completed!")
}

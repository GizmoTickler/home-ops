package bootstrap

import (
	"fmt"
	"strings"
	"time"

	"homeops-cli/internal/common"
)

func waitForNodes(config *BootstrapConfig, logger *common.ColorLogger) error {
	if config.DryRun {
		logger.Info("[DRY RUN] Would wait for nodes to be ready")
		return nil
	}

	// First, wait for nodes to appear
	logger.Info("Waiting for nodes to become available...")
	if err := bootstrapWaitNodesAvailable(config, logger); err != nil {
		return err
	}

	// Check if nodes are already ready (re-bootstrap scenario)
	if ready, err := bootstrapCheckNodesReady(config, logger); err != nil {
		return fmt.Errorf("failed to check node readiness: %w", err)
	} else if ready {
		logger.Success("Nodes are already ready (CNI likely already installed)")
		return nil
	}

	// Wait for nodes to be in Ready=False state (fresh bootstrap sequence)
	logger.Info("Waiting for nodes to be in 'Ready=False' state...")
	if err := bootstrapWaitNodesReadyFalse(config, logger); err != nil {
		return err
	}

	return nil
}

// checkIfNodesReady checks if nodes are already in Ready=True state
func checkIfNodesReady(config *BootstrapConfig, logger *common.ColorLogger) (bool, error) {
	output, err := bootstrapKubectlOutput(config, "get", "nodes",
		"--output=jsonpath={range .items[*]}{.metadata.name}:{.status.conditions[?(@.type=='Ready')].status}{\"\\n\"}{end}")
	if err != nil {
		return false, fmt.Errorf("failed to check node ready status: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	allReady := true
	readyCount := 0
	totalNodes := 0

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) == 2 {
			totalNodes++
			nodeName := parts[0]
			readyStatus := parts[1]
			if readyStatus == "True" {
				readyCount++
				logger.Debug("Node %s is Ready=True", nodeName)
			} else {
				logger.Debug("Node %s is Ready=%s", nodeName, readyStatus)
				allReady = false
			}
		}
	}

	if allReady && readyCount > 0 {
		logger.Info("All %d nodes are already Ready=True", readyCount)
		return true, nil
	}

	logger.Debug("Nodes not all ready yet: %d/%d ready", readyCount, totalNodes)
	return false, nil
}

func waitForNodesAvailable(config *BootstrapConfig, logger *common.ColorLogger) error {
	checkInterval := bootstrapCheckIntervalSlow
	stallTimeout := bootstrapStallTimeout
	maxWait := bootstrapNodeMaxWait

	startTime := bootstrapNow()
	lastProgressTime := bootstrapNow()
	lastNodeCount := 0

	for {
		elapsed := bootstrapNow().Sub(startTime)

		if elapsed > maxWait {
			return fmt.Errorf("nodes not available after %v (max wait exceeded)", elapsed.Round(time.Second))
		}

		output, err := bootstrapKubectlOutput(config, "get", "nodes",
			"--output=jsonpath={.items[*].metadata.name}", "--no-headers")
		if err != nil {
			stallDuration := bootstrapNow().Sub(lastProgressTime)
			if stallDuration > stallTimeout {
				return fmt.Errorf("node discovery stalled: kubectl get nodes failed for %v: %w",
					stallDuration.Round(time.Second), err)
			}
			bootstrapSleep(checkInterval)
			continue
		}

		nodeNames := strings.Fields(strings.TrimSpace(string(output)))
		nodeCount := len(nodeNames)

		if nodeCount > 0 {
			logger.Success("Found %d nodes: %v (took %v)", nodeCount, nodeNames, elapsed.Round(time.Second))
			return nil
		}

		// Check for progress (node count change)
		if nodeCount != lastNodeCount {
			lastProgressTime = bootstrapNow()
			lastNodeCount = nodeCount
		}

		// Check for stall
		stallDuration := bootstrapNow().Sub(lastProgressTime)
		if stallDuration > stallTimeout {
			return fmt.Errorf("node discovery stalled: no progress for %v", stallDuration.Round(time.Second))
		}

		if int(elapsed.Seconds())%60 == 0 && elapsed.Seconds() > 0 {
			logger.Info("Waiting for nodes to appear: %v elapsed", elapsed.Round(time.Second))
		}

		bootstrapSleep(checkInterval)
	}
}

func waitForNodesReadyFalse(config *BootstrapConfig, logger *common.ColorLogger) error {
	checkInterval := bootstrapCheckIntervalSlow
	stallTimeout := bootstrapStallTimeout
	maxWait := bootstrapNodeMaxWait

	startTime := bootstrapNow()
	lastProgressTime := bootstrapNow()
	lastReadyFalseCount := 0

	for {
		elapsed := bootstrapNow().Sub(startTime)

		if elapsed > maxWait {
			return fmt.Errorf("nodes did not reach Ready=False state after %v (max wait exceeded)", elapsed.Round(time.Second))
		}

		output, err := bootstrapKubectlOutput(config, "get", "nodes",
			"--output=jsonpath={range .items[*]}{.metadata.name}:{.status.conditions[?(@.type==\"Ready\")].status}{\"\\n\"}{end}")
		if err != nil {
			stallDuration := bootstrapNow().Sub(lastProgressTime)
			if stallDuration > stallTimeout {
				return fmt.Errorf("node readiness stalled: kubectl get nodes failed for %v: %w",
					stallDuration.Round(time.Second), err)
			}
			bootstrapSleep(checkInterval)
			continue
		}

		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		allReadyFalse := true
		readyFalseCount := 0
		totalNodes := 0

		for _, line := range lines {
			if line == "" {
				continue
			}
			parts := strings.Split(line, ":")
			if len(parts) == 2 {
				totalNodes++
				readyStatus := parts[1]
				if readyStatus == "False" {
					readyFalseCount++
				} else {
					allReadyFalse = false
				}
			}
		}

		// Success: all nodes Ready=False
		if allReadyFalse && readyFalseCount > 0 {
			logger.Success("All %d nodes are in Ready=False state (took %v)", readyFalseCount, elapsed.Round(time.Second))
			return nil
		}

		// Check for progress
		if readyFalseCount > lastReadyFalseCount {
			logger.Debug("Progress: %d/%d nodes Ready=False (+%d)", readyFalseCount, totalNodes, readyFalseCount-lastReadyFalseCount)
			lastProgressTime = bootstrapNow()
			lastReadyFalseCount = readyFalseCount
		}

		// Check for stall
		stallDuration := bootstrapNow().Sub(lastProgressTime)
		if stallDuration > stallTimeout {
			return fmt.Errorf("node readiness stalled: no progress for %v (stuck at %d/%d Ready=False)",
				stallDuration.Round(time.Second), readyFalseCount, totalNodes)
		}

		if int(elapsed.Seconds())%60 == 0 && elapsed.Seconds() > 0 {
			logger.Info("Waiting for nodes: %d/%d Ready=False, %v elapsed", readyFalseCount, totalNodes, elapsed.Round(time.Second))
		}

		bootstrapSleep(checkInterval)
	}
}

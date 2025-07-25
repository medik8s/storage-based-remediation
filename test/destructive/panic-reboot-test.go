package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"
)

var (
	delaySeconds = flag.Int("delay", 10, "Delay in seconds before triggering panic")
	nodeName     = flag.String("node", "", "Node name where this test is running (for logging)")
)

func main() {
	flag.Parse()

	// Get node name from environment if not provided
	if *nodeName == "" {
		*nodeName = os.Getenv("NODE_NAME")
		if *nodeName == "" {
			*nodeName = "unknown"
		}
	}

	log.Printf("=== PANIC REBOOT TEST STARTING ===")
	log.Printf("Node: %s", *nodeName)
	log.Printf("Delay: %d seconds", *delaySeconds)
	log.Printf("Time: %s", time.Now().Format(time.RFC3339))
	log.Printf("This test will PANIC and should cause the node to REBOOT!")
	log.Printf("=====================================")

	// Show countdown
	for i := *delaySeconds; i > 0; i-- {
		log.Printf("Panic in %d seconds...", i)
		time.Sleep(1 * time.Second)
	}

	log.Printf("TRIGGERING PANIC NOW - NODE SHOULD REBOOT!")

	// Force a panic - this should trigger node reboot if running with proper privileges
	panic(fmt.Sprintf("DESTRUCTIVE TEST: Intentional panic to test node reboot on %s at %s",
		*nodeName, time.Now().Format(time.RFC3339)))
}

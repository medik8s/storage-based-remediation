package main

import (
	"flag"
	"log"
	"os"
	"time"
)

var (
	watchdogPath = flag.String("watchdog", "/dev/watchdog", "Path to watchdog device")
	petCount     = flag.Int("pets", 5, "Number of times to pet the watchdog before stopping")
	petInterval  = flag.Duration("interval", 2*time.Second, "Interval between watchdog pets")
	delaySeconds = flag.Int("delay", 10, "Delay in seconds before starting watchdog test")
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

	log.Printf("=== WATCHDOG REBOOT TEST STARTING ===")
	log.Printf("Node: %s", *nodeName)
	log.Printf("Watchdog: %s", *watchdogPath)
	log.Printf("Pet Count: %d", *petCount)
	log.Printf("Pet Interval: %v", *petInterval)
	log.Printf("Delay: %d seconds", *delaySeconds)
	log.Printf("Time: %s", time.Now().Format(time.RFC3339))
	log.Printf("This test will STOP PETTING WATCHDOG and should cause the node to REBOOT!")
	log.Printf("========================================")

	// Show countdown
	for i := *delaySeconds; i > 0; i-- {
		log.Printf("Starting watchdog test in %d seconds...", i)
		time.Sleep(1 * time.Second)
	}

	// Check if watchdog device exists
	if _, err := os.Stat(*watchdogPath); os.IsNotExist(err) {
		log.Fatalf("Watchdog device does not exist: %s", *watchdogPath)
	}

	log.Printf("Opening watchdog device: %s", *watchdogPath)

	// Open watchdog device for writing
	watchdog, err := os.OpenFile(*watchdogPath, os.O_WRONLY, 0)
	if err != nil {
		log.Fatalf("Failed to open watchdog device %s: %v", *watchdogPath, err)
	}
	defer func() {
		log.Printf("Closing watchdog device (this will start countdown to reboot!)")
		watchdog.Close()
	}()

	log.Printf("Successfully opened watchdog device")
	log.Printf("Starting to pet watchdog %d times with %v intervals", *petCount, *petInterval)

	// Pet the watchdog the specified number of times
	for i := 1; i <= *petCount; i++ {
		log.Printf("Petting watchdog #%d/%d", i, *petCount)

		// Write any byte to pet the watchdog
		_, err := watchdog.Write([]byte{1})
		if err != nil {
			log.Fatalf("Failed to pet watchdog: %v", err)
		}

		log.Printf("Successfully petted watchdog #%d", i)

		// Wait before next pet (except for last one)
		if i < *petCount {
			log.Printf("Waiting %v before next pet...", *petInterval)
			time.Sleep(*petInterval)
		}
	}

	log.Printf("Finished petting watchdog %d times", *petCount)
	log.Printf("STOPPING WATCHDOG PETTING NOW - NODE SHOULD REBOOT SOON!")
	log.Printf("Time: %s", time.Now().Format(time.RFC3339))

	// Don't pet anymore - let the watchdog timeout trigger reboot
	// The watchdog will typically timeout in 60 seconds if not petted
	log.Printf("Waiting for watchdog timeout to trigger node reboot...")
	log.Printf("Node %s should reboot within the watchdog timeout period", *nodeName)

	// Wait indefinitely - the node should reboot before this completes
	for i := 1; ; i++ {
		log.Printf("Still waiting for reboot... %d seconds since stopping pets", i*10)
		time.Sleep(10 * time.Second)

		// After 5 minutes, something is wrong
		if i >= 30 {
			log.Printf("ERROR: Node did not reboot after 5 minutes - watchdog may not be working")
			break
		}
	}

	log.Printf("CRITICAL: Test failed - node should have rebooted by now!")
}

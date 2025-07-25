package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"
)

var (
	delaySeconds = flag.Int("delay", 10, "Delay in seconds before triggering panic")
	nodeName     = flag.String("node", "", "Node name where this test is running (for logging)")
)

// executeSystemReboot attempts to reboot the system using multiple methods
func executeSystemReboot() error {
	log.Printf("Attempting system reboot via nsenter and systemctl...")

	// Try nsenter to access the host PID namespace and execute reboot
	cmd := exec.Command("nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", "--", "systemctl", "reboot", "--force", "--force")
	err := cmd.Run()
	if err != nil {
		log.Printf("nsenter systemctl reboot failed: %v", err)

		// Try direct systemctl if nsenter fails
		log.Printf("Trying direct systemctl reboot...")
		cmd = exec.Command("systemctl", "reboot", "--force", "--force")
		err = cmd.Run()
		if err != nil {
			log.Printf("Direct systemctl reboot failed: %v", err)

			// Try reboot command as last resort
			log.Printf("Trying reboot command...")
			cmd = exec.Command("reboot", "-f")
			err = cmd.Run()
			if err != nil {
				log.Printf("reboot -f failed: %v", err)
				return fmt.Errorf("all reboot methods failed: %v", err)
			}
		}
	}

	log.Printf("Reboot command executed successfully")
	return nil
}

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

	log.Printf("TRIGGERING SYSTEM REBOOT NOW - NODE SHOULD REBOOT!")

	// Execute system reboot via multiple methods to ensure it works
	log.Printf("Attempting emergency reboot via systemctl...")
	err := executeSystemReboot()
	if err != nil {
		log.Printf("System reboot failed: %v", err)
		log.Printf("Falling back to panic...")
		panic(fmt.Sprintf("DESTRUCTIVE TEST: System reboot failed, falling back to panic on %s at %s",
			*nodeName, time.Now().Format(time.RFC3339)))
	}
}

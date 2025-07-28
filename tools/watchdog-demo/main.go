/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/medik8s/sbd-operator/pkg/watchdog"
)

func main() {
	fmt.Println("Watchdog Demo - Testing Watchdog functionality...")

	// Try to open the default watchdog device
	watchdogPath := "/dev/watchdog"

	// Check if watchdog device exists before testing
	if _, err := os.Stat(watchdogPath); os.IsNotExist(err) {
		fmt.Printf("Watchdog device %s does not exist, using mock path for testing\n", watchdogPath)
		watchdogPath = "/tmp/mock_watchdog"

		// Create a mock file for testing
		mockFile, err := os.Create(watchdogPath)
		if err != nil {
			fmt.Printf("Failed to create mock watchdog file: %v\n", err)
			return
		}
		mockFile.Close()
		defer os.Remove(watchdogPath)
	}

	// Create a new watchdog instance
	wd, err := watchdog.New(watchdogPath)
	if err != nil {
		fmt.Printf("Failed to create watchdog: %v\n", err)
		return
	}
	defer wd.Close()

	fmt.Printf("Successfully opened watchdog device: %s\n", wd.Path())
	fmt.Printf("Watchdog is open: %t\n", wd.IsOpen())

	// Pet the watchdog a few times
	for i := 0; i < 3; i++ {
		fmt.Printf("Petting watchdog (attempt %d)...\n", i+1)
		err := wd.Pet()
		if err != nil {
			fmt.Printf("Failed to pet watchdog: %v\n", err)
			// Don't exit on pet failure for testing purposes
		} else {
			fmt.Println("Watchdog pet successful")
		}

		// Wait a bit between pets
		time.Sleep(1 * time.Second)
	}

	// Close the watchdog
	fmt.Println("Closing watchdog...")
	err = wd.Close()
	if err != nil {
		fmt.Printf("Failed to close watchdog: %v\n", err)
	} else {
		fmt.Println("Watchdog closed successfully")
	}

	fmt.Printf("Watchdog is open after close: %t\n", wd.IsOpen())
	fmt.Println("Watchdog demo completed successfully!")
}

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

package utils

import (
	"flag"
	"fmt"
	"time"
)

// Test configuration flags - these can be set via command line
var (
	now = time.Now().Unix()
	// Test execution control
	debugMode = flag.Bool("debug", false, "Enable debug output and additional logging")

	// Cluster configuration
	nodeSelector = flag.String(
		"node-selector",
		"",
		"Node selector for targeting specific nodes (e.g., 'node-role.kubernetes.io/worker=')",
	)

	// Test environment
	keepResources = flag.Bool("keep-resources", false, "Keep test resources after completion (useful for debugging)")
	artifactsDir  = flag.String(
		"artifacts-dir",
		fmt.Sprintf("test-%d", now),
		"Directory to store test artifacts (defaults to auto-generated)",
	)
	testID = flag.String("test-id", fmt.Sprintf("%d", now), "Test ID for identifying test runs")

	// AWS/Cloud specific (for disruption testing)
	testAWSRegion = flag.String("aws-region", "", "AWS region for disruption testing")
)

// TestFlags provides access to parsed command line flags
type TestFlags struct {
	DebugMode     bool
	NodeSelector  string
	KeepResources bool
	ArtifactsDir  string
	TestID        string
	AWSRegion     string
}

// GetTestFlags returns the current flag values
func GetTestFlags() *TestFlags {
	return &TestFlags{
		DebugMode:     *debugMode,
		NodeSelector:  *nodeSelector,
		KeepResources: *keepResources,
		ArtifactsDir:  *artifactsDir,
		TestID:        *testID,
		AWSRegion:     *testAWSRegion,
	}
}

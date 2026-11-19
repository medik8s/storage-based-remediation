//go:build !linux

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

package hostlock

import (
	"errors"
	"os"
)

// lockFile fails on platforms where the agent does not run. Failing is deliberate:
// Acquire turns it into ErrLocalAgentConflict, so an unsupported platform can never
// read as a held lock.
func lockFile(_ *os.File) error {
	return errors.New("advisory file locking is not supported on this platform")
}

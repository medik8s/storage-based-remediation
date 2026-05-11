package version

import (
	"fmt"
	"runtime"
)

// Build information that will be set via ldflags during build
var (
	// GitCommit is the git commit SHA
	GitCommit = "unknown"
	// GitDescribe is the output of git describe --tags --dirty
	GitDescribe = "unknown"
	// BuildDate is the date when the binary was built
	BuildDate = "unknown"
	// GoVersion is the Go version used to build the binary
	GoVersion = runtime.Version()
	// Platform is the OS/Architecture combination
	Platform = fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
)

// Info holds all the version information
type Info struct {
	GitCommit   string `json:"gitCommit"`
	GitDescribe string `json:"gitDescribe"`
	BuildDate   string `json:"buildDate"`
	GoVersion   string `json:"goVersion"`
	Platform    string `json:"platform"`
}

// Get returns the version information
func Get() Info {
	return Info{
		GitCommit:   GitCommit,
		GitDescribe: GitDescribe,
		BuildDate:   BuildDate,
		GoVersion:   GoVersion,
		Platform:    Platform,
	}
}

// String returns a formatted string with all version information
func (i Info) String() string {
	return fmt.Sprintf("GitDescribe=%s, GitCommit=%s, BuildDate=%s, GoVersion=%s, Platform=%s",
		i.GitDescribe, i.GitCommit, i.BuildDate, i.GoVersion, i.Platform)
}

// GetFormattedBuildInfo returns formatted build information for logging
func GetFormattedBuildInfo() string {
	info := Get()
	return fmt.Sprintf("Build Info: %s", info.String())
}

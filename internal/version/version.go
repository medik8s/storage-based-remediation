package version

import (
	"fmt"
	"runtime"
)

// Build information that will be set via ldflags during build
var (
	// Version is the operator release version, independent of image tags.
	Version = "5.9.0"
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
	Version     string `json:"version"`
	GitCommit   string `json:"gitCommit"`
	GitDescribe string `json:"gitDescribe"`
	BuildDate   string `json:"buildDate"`
	GoVersion   string `json:"goVersion"`
	Platform    string `json:"platform"`
}

// Get returns the version information
func Get() Info {
	return Info{
		Version:     Version,
		GitCommit:   GitCommit,
		GitDescribe: GitDescribe,
		BuildDate:   BuildDate,
		GoVersion:   GoVersion,
		Platform:    Platform,
	}
}

// String returns a formatted string with all version information
func (i Info) String() string {
	return fmt.Sprintf("Version=%s, GitDescribe=%s, GitCommit=%s, BuildDate=%s, GoVersion=%s, Platform=%s",
		i.Version, i.GitDescribe, i.GitCommit, i.BuildDate, i.GoVersion, i.Platform)
}

// GetFormattedBuildInfo returns formatted build information for logging
func GetFormattedBuildInfo() string {
	info := Get()
	return fmt.Sprintf("Build Info: %s", info.String())
}

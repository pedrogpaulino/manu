// Package buildinfo exposes metadata that identifies a Manu binary.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

const (
	// ModulePath is the canonical module path of Manu.
	ModulePath = "github.com/pedrogpaulino/manu"

	// ContractVersion identifies the structured result contract used by the
	// first runtime increments.
	ContractVersion = "v1alpha1"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

// Info identifies the binary, source revision, build toolchain, and result
// contract associated with an execution.
type Info struct {
	Version         string `json:"version"`
	Commit          string `json:"commit"`
	BuildDate       string `json:"build_date"`
	GoVersion       string `json:"go_version"`
	Module          string `json:"module"`
	ContractVersion string `json:"contract_version"`
}

// Current returns metadata for the running Manu binary. VCS metadata emitted
// by the Go toolchain is used when explicit build metadata is unavailable.
func Current() Info {
	info := Info{
		Version:         version,
		Commit:          commit,
		BuildDate:       buildDate,
		GoVersion:       runtime.Version(),
		Module:          ModulePath,
		ContractVersion: ContractVersion,
	}

	build, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	if build.GoVersion != "" {
		info.GoVersion = build.GoVersion
	}
	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			if info.Commit == "unknown" {
				info.Commit = setting.Value
			}
		case "vcs.time":
			if info.BuildDate == "unknown" {
				info.BuildDate = setting.Value
			}
		}
	}

	return info
}

// String formats the metadata as a concise human-readable line for the
// version command.
func (i Info) String() string {
	return fmt.Sprintf(
		"manu %s (commit %s, built %s, %s, contract %s)",
		i.Version,
		i.Commit,
		i.BuildDate,
		i.GoVersion,
		i.ContractVersion,
	)
}

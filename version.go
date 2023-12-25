package main

import (
	_ "embed"
	"fmt"
	"strings"
)

var (

	//go:embed VERSION
	fullVersion string

	Version, VersionPrerelease, _ = strings.Cut(strings.TrimSpace(fullVersion), "-")
	VersionMetadata               = ""

	GitCommit string
	BuildDate string = "1970-01-01T00:00:01Z"
)

// GetHumanVersion composes the parts of the version in a way that's suitable
// for displaying to humans.
func GetHumanVersion() string {
	version := Version
	release := VersionPrerelease
	metadata := VersionMetadata

	if release != "" {
		version += fmt.Sprintf("-%s", release)
	}

	if metadata != "" {
		version += fmt.Sprintf("+%s", metadata)
	}

	// Strip off any single quotes added by the git information.
	return strings.ReplaceAll(version, "'", "")
}

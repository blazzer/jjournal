package outbound

import (
	"fmt"
	"runtime/debug"
)

// Version is set with -ldflags -X journal/outbound.Version=...
var Version = ""

// BuildVersion returns Version, then the module version, then "dev".
func BuildVersion() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// UserAgent is the identity sent on every outbound request.
func UserAgent(contact string) string {
	if contact == "" {
		contact = "unset"
	}
	return fmt.Sprintf("Journal/%s (private reader; +%s)", BuildVersion(), contact)
}

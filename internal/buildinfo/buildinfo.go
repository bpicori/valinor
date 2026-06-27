package buildinfo

var (
	// These values are injected at build time by the release workflow.
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

const SandboxImageRepo = "valinor-sandbox"

// SandboxImage returns the sandbox image tag for this Valinor build.
func SandboxImage() string {
	if Version == "dev" {
		return SandboxImageRepo + ":dev"
	}
	return SandboxImageRepo + ":" + Version
}
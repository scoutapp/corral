package container

import (
	"os/exec"
	"strings"

	"github.com/jackrothrock/sandclaude/internal/config"
	"github.com/jackrothrock/sandclaude/internal/release"
)

// ImageName is the canonical (always-latest) tag every code path builds and runs.
const ImageName = "sandclaude-stable"

// ImageVersionLabel is the OCI label key stamped with the CLI version that built
// the image, so we can later detect a stale image (built by an older CLI) and
// warn — without forcing a rebuild. Read via `docker inspect`.
const ImageVersionLabel = "org.sandclaude.version"

// imageBuildTags returns the -t arguments for a build: always the plain
// `sandclaude-stable` (what everything runs), plus a version-pinned
// `sandclaude-stable:<version>` when the binary is a real release build (not the
// "dev" default) so an image can be traced back to the CLI that built it.
func imageBuildTags() []string {
	tags := []string{"-t", ImageName}
	if v := config.Version; v != "" && v != "dev" {
		tags = append(tags, "-t", ImageName+":"+v)
	}
	return tags
}

// imageVersionLabelArg returns the --label build argument stamping the current
// CLI version onto the image. Always applied (even for "dev") so the label
// always exists and the mismatch check has something to read.
func imageVersionLabelArg() []string {
	return []string{"--label", ImageVersionLabel + "=" + config.Version}
}

// ImageBuildStampArgs returns the label + tag docker-build arguments that stamp
// the image with the CLI version. Exported so the CLI `rebuild` path produces an
// identically-stamped image to EnsureImage. Does NOT include the build context.
func ImageBuildStampArgs() []string {
	args := imageVersionLabelArg()
	return append(args, imageBuildTags()...)
}

// ImageVersion returns the version label stamped on the local sandclaude-stable
// image, or "" if the image is absent or unlabeled (built before stamping
// existed). Best-effort: any docker error yields "".
func ImageVersion() string {
	out, err := exec.Command("docker", "image", "inspect",
		"--format", "{{ index .Config.Labels \""+ImageVersionLabel+"\" }}", ImageName).Output()
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(out))
	// docker prints "<no value>" for a missing label under this format.
	if v == "<no value>" {
		return ""
	}
	return v
}

// ImageStale reports whether the local image was built by an OLDER CLI than the
// one running now (its label version < config.Version), along with the image's
// recorded version. It never blocks — callers warn and continue. An unlabeled or
// absent image, a dev CLI, or an unparseable label all return (false, ...) so we
// only nag on a clear, real version regression.
func ImageStale() (stale bool, imageVer string) {
	imageVer = ImageVersion()
	if imageVer == "" {
		return false, imageVer // no image / pre-stamping build → nothing to compare
	}
	if config.Version == "dev" {
		return false, imageVer // dev CLI shouldn't nag about "newer than the image"
	}
	// Reuse the release semver comparison: the image is stale when the CLI
	// version is newer than the image's stamped version.
	return release.IsNewer(config.Version, imageVer), imageVer
}

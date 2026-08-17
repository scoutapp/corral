// Package dindcache manages reusable named DinD data caches.
//
// A project's inner Docker data root is a named volume (config.DindVolumeName).
// Building images and seeding data (e.g. a postgres volume) into it is slow, and
// today it's isolated per-workspace — a fresh project starts from nothing. A
// "cache" is a named volume a project can start FROM, so that work is done once:
//
//	corral-dind-cache-<slug>
//
// A cache is CREATED by snapshotting a project's current DinD volume (copy the
// project's corral-dind-<ws> volume into the cache volume). Projects then use it
// in one of two modes:
//
//   - COPY   the project's per-workspace volume is SEEDED from the cache once
//     (a fresh full copy); changes never touch the cache. Throwaway.
//   - SHARED the project mounts the cache volume directly; a migration writes
//     back and persists into the cache.
//
// There is no cheap copy-on-write here: the DinD data root is a named Docker
// volume (bind mounts were abandoned — they hit "lchown /proc: permission
// denied" on image layers), so cloning is a container-mediated `cp -a` between
// two mounted volumes. This is O(size) and slow on the vfs storage driver, but
// it's the only portable option.
package dindcache

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// cachePrefix is the docker-volume name prefix for every corral DinD cache.
// Kept distinct from the corral-dind- (no "cache") per-workspace volumes so a
// `name=corral-dind-cache-` filter never catches a project volume, and the
// uninstall/rebuild sweeps that match corral-dind- still catch caches too.
const cachePrefix = "corral-dind-cache-"

// dockerTimeout bounds read-only/administrative docker calls (list, inspect,
// create, rm). A wedged daemon — common after a privileged DinD container — can
// make even `volume ls` block forever; without a deadline that hangs the caller
// with no output. The copy step gets its own, much longer budget (see copyTimeout).
const dockerTimeout = 20 * time.Second

// copyTimeout bounds the container-mediated volume copy. A full `cp -a` of a
// DinD data root (built images + seeded volumes, vfs-expanded) can be gigabytes,
// so this is generous — but still bounded, so a stuck copy fails loudly instead
// of hanging a dashboard request forever.
const copyTimeout = 30 * time.Minute

// copyImage is the tiny image used as the vehicle for the volume copy. It only
// needs a shell and cp; alpine is already commonly cached. It is pulled through
// the same daemon the project uses, so it must be reachable (it's a Docker Hub
// library image — allowlisted in typical setups).
const copyImage = "alpine:3.20"

// validSlug gates cache names to a filesystem/volume-safe slug so the derived
// docker volume name is always legal and unsurprising. Mirrors the skill-name
// gate in automations/repo_skills.go: letters, digits, dash, underscore.
var validSlug = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Cache is a named DinD data cache.
type Cache struct {
	Name   string `json:"name"`   // the user-facing slug (no prefix)
	Volume string `json:"volume"` // the underlying docker volume name
	Bytes  int64  `json:"bytes"`  // on-disk size, best-effort (0 if unknown)
}

// ValidName reports whether name is a legal cache slug.
func ValidName(name string) bool { return validSlug.MatchString(name) }

// VolumeName returns the docker volume name backing a cache slug. It does NOT
// validate — callers that accept user input should ValidName first.
func VolumeName(name string) string { return cachePrefix + name }

// nameFromVolume returns the user-facing slug for a cache volume, or "" if vol
// isn't a cache volume.
func nameFromVolume(vol string) string {
	if !strings.HasPrefix(vol, cachePrefix) {
		return ""
	}
	return strings.TrimPrefix(vol, cachePrefix)
}

// dockerOut runs `docker <args>` with the given timeout, returning combined
// output. A deadline overrun is surfaced as a distinct error so callers can tell
// "docker is hung" from "docker said no".
func dockerOut(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("docker %s timed out after %s (daemon may be hung)", args[0], timeout)
	}
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return string(out), fmt.Errorf("docker %s: %v: %s", args[0], err, msg)
		}
		return string(out), fmt.Errorf("docker %s: %v", args[0], err)
	}
	return string(out), nil
}

// volumeExists reports whether a docker volume with the exact name exists.
func volumeExists(vol string) bool {
	out, err := dockerOut(dockerTimeout, "volume", "ls", "-q", "--filter", "name=^"+vol+"$")
	if err != nil {
		return false
	}
	for _, line := range strings.Fields(strings.TrimSpace(out)) {
		if line == vol {
			return true
		}
	}
	return false
}

// CopyVolume copies the full contents of src volume into dst volume, creating
// dst if needed. It mounts both into a throwaway container and runs `cp -a` so
// ownership, timestamps, and symlinks (including the /proc entries that defeat
// bind mounts) are preserved. src is mounted read-only; dst read-write.
//
// This is the single primitive both snapshot (project→cache) and COPY-mode seed
// (cache→project) are built on.
func CopyVolume(src, dst string) error {
	if src == "" || dst == "" {
		return fmt.Errorf("copy volume: src and dst are required")
	}
	if !volumeExists(src) {
		return fmt.Errorf("copy volume: source %q does not exist", src)
	}
	// Ensure dst exists (docker volume create is idempotent).
	if _, err := dockerOut(dockerTimeout, "volume", "create", dst); err != nil {
		return fmt.Errorf("copy volume: create dst: %w", err)
	}
	// cp -a preserves everything; the trailing "/." copies contents (not the dir
	// itself) so we don't nest /from inside /to.
	_, err := dockerOut(copyTimeout,
		"run", "--rm",
		"-v", src+":/from:ro",
		"-v", dst+":/to",
		copyImage,
		"sh", "-c", "cp -a /from/. /to/",
	)
	if err != nil {
		return fmt.Errorf("copy volume %s -> %s: %w", src, dst, err)
	}
	return nil
}

// List returns every DinD cache, with a best-effort on-disk size.
func List() ([]Cache, error) {
	out, err := dockerOut(dockerTimeout, "volume", "ls", "-q", "--filter", "name="+cachePrefix)
	if err != nil {
		return nil, err
	}
	var caches []Cache
	for _, vol := range strings.Fields(strings.TrimSpace(out)) {
		name := nameFromVolume(vol)
		if name == "" {
			continue
		}
		caches = append(caches, Cache{Name: name, Volume: vol, Bytes: volumeSize(vol)})
	}
	return caches, nil
}

// volumeSize returns a volume's on-disk size in bytes, best-effort (0 if it
// can't be determined). `docker system df -v` reports per-volume size; we parse
// the row for this volume. This is advisory (shown in the UI), never load-bearing.
func volumeSize(vol string) int64 {
	out, err := dockerOut(dockerTimeout, "system", "df", "-v",
		"--format", "{{range .Volumes}}{{.Name}}\t{{.Size}}\n{{end}}")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(fields) == 2 && fields[0] == vol {
			return parseHumanSize(fields[1])
		}
	}
	return 0
}

// CreateFromVolume snapshots a source volume (a project's corral-dind-<ws>
// volume) into a named cache. It validates the name, refuses to clobber an
// existing cache, and copies the source in.
func CreateFromVolume(name, srcVolume string) (Cache, error) {
	if !ValidName(name) {
		return Cache{}, fmt.Errorf("invalid cache name %q: use letters, digits, dashes, underscores (max 64)", name)
	}
	dst := VolumeName(name)
	if volumeExists(dst) {
		return Cache{}, fmt.Errorf("cache %q already exists", name)
	}
	if !volumeExists(srcVolume) {
		return Cache{}, fmt.Errorf("nothing to snapshot: this project has no DinD data yet (volume %q not found)", srcVolume)
	}
	if err := CopyVolume(srcVolume, dst); err != nil {
		// Clean up a partially-populated cache so a retry sees a clean slate.
		_, _ = dockerOut(dockerTimeout, "volume", "rm", "-f", dst)
		return Cache{}, err
	}
	return Cache{Name: name, Volume: dst, Bytes: volumeSize(dst)}, nil
}

// SeedInto copies a cache into a destination (project) volume for COPY mode.
// It's a no-op-safe wrapper that resolves the cache name to its volume and
// refuses unknown caches. The caller decides WHEN to seed (first start, empty
// project volume) — this just performs it.
func SeedInto(cacheName, dstVolume string) error {
	if !ValidName(cacheName) {
		return fmt.Errorf("invalid cache name %q", cacheName)
	}
	src := VolumeName(cacheName)
	if !volumeExists(src) {
		return fmt.Errorf("cache %q not found", cacheName)
	}
	return CopyVolume(src, dstVolume)
}

// Delete removes a cache volume. It force-detaches any container still holding
// it first (mirrors uninstall.removeDindVolumes) so a stopped project container
// can't block the deletion.
func Delete(name string) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid cache name %q", name)
	}
	vol := VolumeName(name)
	if !volumeExists(vol) {
		return fmt.Errorf("cache %q not found", name)
	}
	if _, err := dockerOut(dockerTimeout, "volume", "rm", "-f", vol); err != nil {
		return err
	}
	return nil
}

// Exists reports whether a cache with this name exists.
func Exists(name string) bool {
	return ValidName(name) && volumeExists(VolumeName(name))
}

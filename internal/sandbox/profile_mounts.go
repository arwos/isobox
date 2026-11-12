package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arwos/isobox/internal/profile"
)

type profileMount struct {
	source   string
	target   string
	writable bool
}

func resolveProfileMounts(configured []profile.Mount) ([]profileMount, error) {
	mounts := make([]profileMount, 0, len(configured))
	for index, mount := range configured {
		field := fmt.Sprintf("profile.mount[%d]", index)
		if !filepath.IsAbs(mount.Source) || filepath.Clean(mount.Source) != mount.Source {
			return nil, fmt.Errorf("%s.source must be a clean absolute path", field)
		}
		if !filepath.IsAbs(mount.Target) || filepath.Clean(mount.Target) != mount.Target {
			return nil, fmt.Errorf("%s.target must be a clean absolute path", field)
		}
		if mount.Source == string(filepath.Separator) || mount.Target == string(filepath.Separator) {
			return nil, fmt.Errorf("%s cannot mount the host root or replace the sandbox root", field)
		}
		if isReservedMountTarget(mount.Target) {
			return nil, fmt.Errorf("%s.target %q is reserved by the sandbox runtime", field, mount.Target)
		}

		source, err := filepath.EvalSymlinks(mount.Source)
		if err != nil {
			return nil, fmt.Errorf("%s.source %q is unavailable: %w", field, mount.Source, err)
		}
		info, err := os.Stat(source)
		if err != nil {
			return nil, fmt.Errorf("inspect %s.source %q: %w", field, source, err)
		}
		if source == string(filepath.Separator) {
			return nil, fmt.Errorf("%s.source resolves to the host root; choose a narrower path", field)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s.source %q must be a regular file or directory", field, mount.Source)
		}
		mounts = append(mounts, profileMount{
			source:   source,
			target:   mount.Target,
			writable: !mount.ReadOnly,
		})
	}
	return mounts, nil
}

func validateProfileMountTargets(mounts []profileMount) error {
	targets := make(map[string]struct{}, len(mounts))
	for _, mount := range mounts {
		if _, exists := targets[mount.target]; exists {
			return fmt.Errorf("multiple mounts target %q; choose unique destinations", mount.target)
		}
		targets[mount.target] = struct{}{}
	}
	return nil
}

func isReservedMountTarget(target string) bool {
	switch target {
	case "/proc", "/dev", "/tmp", "/run", "/var/tmp", "/root", "/home", sandboxHome:
		return true
	default:
		return false
	}
}

func sortProfileMounts(mounts []profileMount) {
	sort.SliceStable(mounts, func(i, j int) bool {
		depthI := strings.Count(mounts[i].target, string(filepath.Separator))
		depthJ := strings.Count(mounts[j].target, string(filepath.Separator))
		if depthI != depthJ {
			return depthI < depthJ
		}
		return mounts[i].target < mounts[j].target
	})
}

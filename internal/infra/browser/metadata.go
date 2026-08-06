package browser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// readMetadata loads the record written beside an install. A directory without
// one is not a complete install, so the caller treats the error as "missing".
func readMetadata(dir string) (Metadata, error) {
	raw, err := os.ReadFile(filepath.Join(dir, metadataFile)) //nolint:gosec // dir is our own cache path
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: %s", ErrNotInstalled, filepath.Base(dir))
	}

	var meta Metadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return Metadata{}, fmt.Errorf("%w: %s has unreadable metadata", ErrNotInstalled, filepath.Base(dir))
	}

	return meta, nil
}

// writeMetadata records an install. It is written last, so a directory only
// looks like a valid install once the extraction that produced it finished.
func writeMetadata(dir string, meta Metadata) error {
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding metadata: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, metadataFile), append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing metadata: %w", err)
	}

	return nil
}

// lessVersion orders two Chrome version strings (major.minor.build.patch)
// numerically, so 151.0.7922.76 sorts after 99.0.1.1 rather than before it as a
// lexical compare would have it.
func lessVersion(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")

	for i := range max(len(as), len(bs)) {
		av, bv := versionPart(as, i), versionPart(bs, i)
		if av != bv {
			return av < bv
		}
	}

	return false
}

// versionPart returns the i-th dotted component as a number, treating a missing
// or non-numeric component as 0 so an odd version string still orders sanely.
func versionPart(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, err := strconv.Atoi(parts[i])
	if err != nil {
		return 0
	}

	return n
}

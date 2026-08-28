package organize

import (
	"path/filepath"
	"strings"

	"github.com/Anastylosis/Custodian/internal/config"
)

// Filed reports whether path already sits under the organize template's
// base directory — the location tiebreak REBUILD.md §4.3 uses to settle a
// `certain` group without falling into review: a location-only check, not
// an exact target-path match, so a filed file with a stale resolution token
// in its name still counts. Meant to back keeper.Rules.Filed once that
// wiring lands; not called from keeper yet.
func Filed(path string, cfg *config.OrganizeConfig) bool {
	if cfg == nil || cfg.BaseDir == "" || path == "" {
		return false
	}
	base := filepath.Clean(cfg.BaseDir)
	p := filepath.Clean(path)
	return p == base || strings.HasPrefix(p, base+string(filepath.Separator))
}

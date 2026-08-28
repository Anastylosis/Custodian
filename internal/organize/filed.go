package organize

import (
	"path"
	"strings"

	"github.com/Anastylosis/Custodian/internal/config"
)

// Filed reports whether filePath already sits under the organize template's
// base directory — the location tiebreak REBUILD.md §4.3 uses to settle a
// `certain` group without falling into review: a location-only check, not
// an exact target-path match, so a filed file with a stale resolution token
// in its name still counts. Meant to back keeper.Rules.Filed once that
// wiring lands; not called from keeper yet.
//
// filePath and cfg.BaseDir are Stash paths, always "/"-separated, so this
// uses "path", not "path/filepath" (whose separator follows the OS running
// this code, not the OS Stash reports paths for).
func Filed(filePath string, cfg *config.OrganizeConfig) bool {
	if cfg == nil || cfg.BaseDir == "" || filePath == "" {
		return false
	}
	base := path.Clean(cfg.BaseDir)
	p := path.Clean(filePath)
	return p == base || strings.HasPrefix(p, base+"/")
}

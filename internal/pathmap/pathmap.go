// Package pathmap translates the paths Stash reports into paths the machine
// running stash-janitor can actually open.
//
// Stash always reports POSIX paths as seen from inside its own process — in a
// Docker deployment that is typically /data/..., which usually exists nowhere
// else. The same library might be reachable as:
//
//	/share/main/Videos/...        (on the NAS itself)
//	/mnt/nas/Videos/...       (Linux mount)
//	Z:\Videos\...                 (Windows mapped drive)
//	\\nas\main\Videos\...     (Windows UNC)
//
// A Mapper rewrites the prefix and re-joins with the separator style the
// destination uses, so the rest of the tool can stay path-agnostic.
package pathmap

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Rule maps one Stash-visible prefix onto a locally reachable one.
type Rule struct {
	// StashPrefix is the path as Stash reports it, e.g. "/data".
	StashPrefix string
	// LocalPrefix is where that same directory is reachable from here,
	// e.g. "/mnt/nas/Videos", "Z:\\Videos" or "\\\\nas\\main\\Videos".
	LocalPrefix string
}

// Mapper applies Rules longest-prefix-first.
type Mapper struct {
	rules []Rule
}

var winDriveRe = regexp.MustCompile(`^[A-Za-z]:`)

// isWindowsStyle reports whether a prefix looks like a Windows path — a drive
// letter (Z:), a UNC share (\\host\share), or anything containing a backslash.
func isWindowsStyle(p string) bool {
	return strings.HasPrefix(p, `\\`) || winDriveRe.MatchString(p) || strings.Contains(p, `\`)
}

// trimTrailingSep removes trailing separators so prefixes compare cleanly,
// while preserving a bare root ("/" or "Z:\") which must keep its separator.
func trimTrailingSep(p string) string {
	for len(p) > 1 && (strings.HasSuffix(p, "/") || strings.HasSuffix(p, `\`)) {
		// Keep "Z:\" and "\\" intact.
		if winDriveRe.MatchString(p) && len(p) == 3 {
			break
		}
		if p == `\\` {
			break
		}
		p = p[:len(p)-1]
	}
	return p
}

// New builds a Mapper. An empty rule set is valid and yields an identity
// mapper — the correct configuration when stash-janitor runs on the same
// filesystem Stash sees.
func New(rules []Rule) (*Mapper, error) {
	cleaned := make([]Rule, 0, len(rules))
	for i, r := range rules {
		s := trimTrailingSep(strings.TrimSpace(r.StashPrefix))
		l := trimTrailingSep(strings.TrimSpace(r.LocalPrefix))
		if s == "" || l == "" {
			return nil, fmt.Errorf("path_map rule %d: both stash and local prefixes are required (got %q → %q)", i+1, r.StashPrefix, r.LocalPrefix)
		}
		if !strings.HasPrefix(s, "/") {
			return nil, fmt.Errorf("path_map rule %d: stash prefix %q must be absolute and POSIX-style (Stash always reports paths like /data/...)", i+1, r.StashPrefix)
		}
		cleaned = append(cleaned, Rule{StashPrefix: s, LocalPrefix: l})
	}
	// Longest Stash prefix wins, so /data/archive beats /data.
	sort.SliceStable(cleaned, func(i, j int) bool {
		return len(cleaned[i].StashPrefix) > len(cleaned[j].StashPrefix)
	})
	return &Mapper{rules: cleaned}, nil
}

// Rules returns the mapper's rules, longest-prefix-first. Used by the doctor
// command to show what is configured.
func (m *Mapper) Rules() []Rule {
	if m == nil {
		return nil
	}
	out := make([]Rule, len(m.rules))
	copy(out, m.rules)
	return out
}

// hasPathPrefix reports whether p starts with prefix at a path boundary, so
// "/data" matches "/data/x" but not "/database/x".
func hasPathPrefix(p, prefix string) bool {
	if p == prefix {
		return true
	}
	if !strings.HasPrefix(p, prefix) {
		return false
	}
	rest := p[len(prefix):]
	return strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, `\`)
}

// ToLocal rewrites a Stash-reported path into a locally reachable one.
// With no rules configured the path is returned unchanged.
func (m *Mapper) ToLocal(stashPath string) (string, error) {
	if stashPath == "" {
		return "", fmt.Errorf("pathmap: empty path")
	}
	if m == nil || len(m.rules) == 0 {
		return stashPath, nil
	}
	for _, r := range m.rules {
		if !hasPathPrefix(stashPath, r.StashPrefix) {
			continue
		}
		rest := strings.TrimPrefix(stashPath[len(r.StashPrefix):], "/")
		return joinNative(r.LocalPrefix, rest), nil
	}
	return "", fmt.Errorf("pathmap: no path_map rule covers %q — add one mapping the Stash prefix to a directory this machine can reach", stashPath)
}

// ToStash rewrites a local path back into the path Stash would report.
// Used by the doctor command to round-trip a mapping.
func (m *Mapper) ToStash(localPath string) (string, error) {
	if localPath == "" {
		return "", fmt.Errorf("pathmap: empty path")
	}
	if m == nil || len(m.rules) == 0 {
		return localPath, nil
	}
	// Longest local prefix wins here, independent of Stash-prefix ordering.
	best := -1
	for i, r := range m.rules {
		if !hasLocalPrefix(localPath, r.LocalPrefix) {
			continue
		}
		if best == -1 || len(r.LocalPrefix) > len(m.rules[best].LocalPrefix) {
			best = i
		}
	}
	if best == -1 {
		return "", fmt.Errorf("pathmap: no path_map rule covers local path %q", localPath)
	}
	r := m.rules[best]
	rest := localPath[len(r.LocalPrefix):]
	rest = strings.TrimLeft(rest, `/\`)
	rest = strings.ReplaceAll(rest, `\`, "/")
	if rest == "" {
		return r.StashPrefix, nil
	}
	return r.StashPrefix + "/" + rest, nil
}

// hasLocalPrefix is hasPathPrefix with case-insensitivity for Windows-style
// prefixes, where Z:\Videos and z:\videos are the same directory.
func hasLocalPrefix(p, prefix string) bool {
	if isWindowsStyle(prefix) {
		return hasPathPrefix(strings.ToLower(p), strings.ToLower(prefix))
	}
	return hasPathPrefix(p, prefix)
}

// joinNative appends a POSIX-style relative path to a prefix using whichever
// separator that prefix already uses.
func joinNative(prefix, rest string) string {
	if rest == "" {
		return prefix
	}
	if isWindowsStyle(prefix) {
		rest = strings.ReplaceAll(rest, "/", `\`)
		if strings.HasSuffix(prefix, `\`) {
			return prefix + rest
		}
		return prefix + `\` + rest
	}
	if strings.HasSuffix(prefix, "/") {
		return prefix + rest
	}
	return prefix + "/" + rest
}

// Join appends a relative path to a directory using the directory's own
// separator style. Exposed because callers building sidecar destinations must
// not use path/filepath, whose separator follows the machine running the tool
// rather than the machine holding the files.
func Join(dir, name string) string { return joinNative(dir, name) }

// Dir returns everything before the final separator, honouring both styles.
func Dir(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i < 0 {
		return ""
	}
	if i == 0 {
		return "/"
	}
	return p[:i]
}

// Base returns the final path element, honouring both separator styles.
func Base(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i < 0 {
		return p
	}
	return p[i+1:]
}

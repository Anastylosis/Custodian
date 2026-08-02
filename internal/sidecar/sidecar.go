// Package sidecar identifies subtitle (and other companion) files that belong
// to a video, and computes where they must land when that video moves.
//
// Why this exists: Stash does not index subtitles as files. At scan time it
// looks next to each video for <stem>.srt / <stem>.<lang>.srt and attaches what
// it finds as a property of the scene. Captions therefore have no file ID, and
// Stash's moveFiles mutation — which takes ids: [ID!] — cannot move them. When
// `organize` moves a video through moveFiles, every sidecar is left behind and
// the caption silently disappears from the scene.
//
// This package is pure: it takes filenames and returns filenames. All I/O lives
// behind the vfs.FS interface so the same logic works over a local mount, a
// Windows share, or SSH.
package sidecar

import (
	"path"
	"sort"
	"strings"
)

// DefaultExtensions are the companion file types recognised by default.
// Stash itself only reads subtitle formats, but users often keep other
// sidecars next to a video and expect them to travel with it.
var DefaultExtensions = []string{"srt", "ass", "ssa", "vtt", "sub", "idx", "smi"}

// Move describes one sidecar that must follow its video.
type Move struct {
	// SrcBasename is the sidecar's current basename (no directory).
	SrcBasename string
	// DstBasename is the basename it must take next to the moved video.
	DstBasename string
	// Suffix is the portion after the video stem, e.g. ".en.srt" or ".srt".
	// Preserving this is what keeps multi-language captions working: a
	// video.en.srt must stay <newstem>.en.srt, not collapse to <newstem>.srt.
	Suffix string
}

// stemOf strips the final extension from a basename.
// "a.b.en.srt" → "a.b.en"; "movie.mp4" → "movie".
func stemOf(basename string) string {
	ext := path.Ext(basename)
	if ext == "" {
		return basename
	}
	return strings.TrimSuffix(basename, ext)
}

// normExt lowercases and strips a leading dot from an extension.
func normExt(e string) string {
	return strings.ToLower(strings.TrimPrefix(e, "."))
}

// extSet builds a lookup set from a caller-supplied extension list, falling
// back to DefaultExtensions when the list is empty.
func extSet(exts []string) map[string]bool {
	if len(exts) == 0 {
		exts = DefaultExtensions
	}
	m := make(map[string]bool, len(exts))
	for _, e := range exts {
		if e = normExt(e); e != "" {
			m[e] = true
		}
	}
	return m
}

// IsSidecarOf reports whether candidate is a companion file of videoBasename,
// and returns the suffix that follows the video's stem.
//
//	IsSidecarOf("Foo.mp4", "Foo.en.srt", nil) → (".en.srt", true)
//	IsSidecarOf("Foo.mp4", "Foo.srt",    nil) → (".srt",    true)
//	IsSidecarOf("Foo.mp4", "Foobar.srt", nil) → ("",        false)
//
// Matching is case-insensitive on the stem because SMB and macOS shares are
// routinely case-insensitive, and a caption named FOO.srt next to Foo.mp4 is
// the same file as far as those filesystems are concerned.
func IsSidecarOf(videoBasename, candidate string, exts []string) (string, bool) {
	stem := stemOf(videoBasename)
	if stem == "" || candidate == "" {
		return "", false
	}
	if len(candidate) <= len(stem) {
		return "", false
	}
	if !strings.EqualFold(candidate[:len(stem)], stem) {
		return "", false
	}
	suffix := candidate[len(stem):]
	// Must be a real suffix boundary: Foo.mp4 must not claim Foobar.srt.
	if !strings.HasPrefix(suffix, ".") {
		return "", false
	}
	if !extSet(exts)[normExt(path.Ext(candidate))] {
		return "", false
	}
	return suffix, true
}

// Plan computes the sidecar moves for one video.
//
// srcVideo and dstVideo are basenames. siblings is every entry in the video's
// current directory — including other videos, which matters: given
//
//	Foo.mp4, Foo.part2.mp4, Foo.part2.srt
//
// the file Foo.part2.srt is a valid suffix-match for BOTH "Foo" (suffix
// ".part2.srt") and "Foo.part2" (suffix ".srt"). It belongs to the longer,
// more specific stem. Without this check, moving Foo.mp4 would steal the other
// video's subtitle. Sidecars whose best owner is a different video are skipped.
//
// Results are sorted by source basename so output and tests are deterministic.
func Plan(srcVideo, dstVideo string, siblings []string, exts []string) []Move {
	srcStem := stemOf(srcVideo)
	dstStem := stemOf(dstVideo)
	if srcStem == "" || dstStem == "" {
		return nil
	}

	// Every other video-ish stem in the directory that could compete for a
	// sidecar. We only need stems longer than ours: a shorter stem can never
	// be a more specific owner.
	var rivals []string
	for _, s := range siblings {
		if strings.EqualFold(s, srcVideo) {
			continue
		}
		if _, isSide := IsSidecarOf(srcVideo, s, exts); isSide {
			continue // it's a sidecar, not a rival video
		}
		st := stemOf(s)
		if len(st) > len(srcStem) && strings.HasPrefix(strings.ToLower(st), strings.ToLower(srcStem)) {
			rivals = append(rivals, st)
		}
	}

	var out []Move
	for _, s := range siblings {
		suffix, ok := IsSidecarOf(srcVideo, s, exts)
		if !ok {
			continue
		}
		if ownedByRival(s, rivals, exts) {
			continue
		}
		out = append(out, Move{
			SrcBasename: s,
			DstBasename: dstStem + suffix,
			Suffix:      suffix,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SrcBasename < out[j].SrcBasename })
	return out
}

// ownedByRival reports whether candidate is a better match for some other,
// more specific video stem in the same directory.
func ownedByRival(candidate string, rivals []string, exts []string) bool {
	for _, r := range rivals {
		// Reuse the suffix test against the rival stem by giving it a
		// synthetic extension — only the stem portion matters.
		if _, ok := IsSidecarOf(r+".video", candidate, exts); ok {
			return true
		}
	}
	return false
}

package subs

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Wasylq/StashJanitor/internal/pathmap"
	"github.com/Wasylq/StashJanitor/internal/vfs"
)

// DefaultExtensions are the subtitle formats a folder scan collects.
var DefaultExtensions = []string{"srt", "ass", "ssa", "vtt", "sub", "smi"}

// Collect walks dir and returns every subtitle file it finds, with each file's
// runtime and language read from its contents.
//
// Reading every file is deliberate: the runtime is the signal that makes
// matching trustworthy, and the language determines the caption tag that lets
// several tracks coexist on one scene. Neither can be taken from the filename.
//
// A file that cannot be read is still returned, with zero runtime and empty
// language, so the report can show it rather than silently dropping it.
func Collect(ctx context.Context, fs vfs.FS, dir string, exts []string, recursive bool) ([]Subtitle, []string, error) {
	want := extSet(exts)
	var out []Subtitle
	var problems []string

	var walk func(string, int) error
	walk = func(d string, depth int) error {
		if depth > 32 {
			problems = append(problems, "stopped descending at "+d+": nesting too deep")
			return nil
		}
		entries, err := fs.ReadDir(ctx, d)
		if err != nil {
			return err
		}
		for _, e := range entries {
			p := pathmap.Join(d, e.Name)
			if e.IsDir {
				if recursive {
					if err := walk(p, depth+1); err != nil {
						problems = append(problems, fmt.Sprintf("%s: %v", p, err))
					}
				}
				continue
			}
			ext := strings.ToLower(strings.TrimPrefix(extOf(e.Name), "."))
			if !want[ext] {
				continue
			}
			sub := Subtitle{
				Path: p,
				Stem: strings.TrimSuffix(e.Name, extOf(e.Name)),
				Size: e.Size,
			}
			if err := readInto(ctx, fs, p, &sub); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", p, err))
			}
			out = append(out, sub)
		}
		return nil
	}

	if err := walk(dir, 0); err != nil {
		return nil, problems, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, problems, nil
}

// readInto fills a subtitle's runtime and language from its contents. The file
// is opened twice rather than buffered whole: subtitle files are small, and
// this keeps memory flat when scanning thousands of them.
func readInto(ctx context.Context, fs vfs.FS, p string, sub *Subtitle) error {
	rc, err := fs.Open(ctx, p)
	if err != nil {
		return err
	}
	if d, ok := Runtime(rc); ok {
		sub.Runtime = d
	}
	rc.Close()

	rc, err = fs.Open(ctx, p)
	if err != nil {
		return err
	}
	defer rc.Close()
	if lang, _, ok := DetectLanguage(rc); ok {
		sub.Lang = lang
	}
	return nil
}

func extOf(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return ""
	}
	return name[i:]
}

func extSet(exts []string) map[string]bool {
	if len(exts) == 0 {
		exts = DefaultExtensions
	}
	m := make(map[string]bool, len(exts))
	for _, e := range exts {
		e = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), "."))
		if e != "" {
			m[e] = true
		}
	}
	return m
}

// AlreadyAttached reports whether a subtitle already sits next to its video
// under a name Stash recognises — i.e. it is not orphaned at all.
//
// Stash attaches <stem>.srt and <stem>.<lang>.srt, so a subtitle whose stem
// (minus any language tag) matches a video in the same directory is already
// visible and needs no matching.
func AlreadyAttached(ctx context.Context, fs vfs.FS, sub Subtitle, videoExts map[string]bool) (bool, error) {
	dir := pathmap.Dir(sub.Path)
	entries, err := fs.ReadDir(ctx, dir)
	if err != nil {
		return false, err
	}
	stem := sub.Stem
	if base, lang, ok := splitLangSuffix(stem); ok {
		_ = lang
		stem = base
	}
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		ext := strings.ToLower(strings.TrimPrefix(extOf(e.Name), "."))
		if !videoExts[ext] {
			continue
		}
		if strings.EqualFold(strings.TrimSuffix(e.Name, extOf(e.Name)), stem) {
			return true, nil
		}
	}
	return false, nil
}

// knownLangs are the tags Stash reads from a caption filename.
var knownLangs = map[string]bool{
	"en": true, "es": true, "fr": true, "de": true, "it": true, "pt": true,
	"ru": true, "ja": true, "jp": true, "zh": true, "cn": true, "ko": true,
	"nl": true, "pl": true, "sv": true, "da": true, "fi": true, "no": true,
	"tr": true, "ar": true, "cs": true, "hu": true, "el": true, "uk": true,
	"eng": true, "spa": true, "jpn": true, "chi": true, "zho": true, "kor": true,
}

// splitLangSuffix separates a trailing language tag from a stem:
// "clip_1080p.en" → ("clip_1080p", "en", true).
func splitLangSuffix(stem string) (string, string, bool) {
	i := strings.LastIndex(stem, ".")
	if i < 0 {
		return stem, "", false
	}
	tag := strings.ToLower(stem[i+1:])
	if !knownLangs[tag] {
		return stem, "", false
	}
	return stem[:i], tag, true
}

// DefaultVideoExtensions is the set treated as "a video" when deciding whether
// a subtitle is already attached.
var DefaultVideoExtensions = map[string]bool{
	"mp4": true, "mkv": true, "avi": true, "wmv": true, "mov": true, "m4v": true,
	"webm": true, "flv": true, "mpg": true, "mpeg": true, "ts": true, "vob": true,
	"m2ts": true, "rm": true, "divx": true,
}

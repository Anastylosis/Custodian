package sidecar

import (
	"context"
	"errors"
	"fmt"

	"github.com/Anastylosis/Custodian/internal/pathmap"
	"github.com/Anastylosis/Custodian/internal/vfs"
)

// PlannedMove is one sidecar relocation, resolved in both path spaces.
// The Stash-space paths are what users recognise from reports and the Stash UI;
// the local ones are what actually gets renamed.
type PlannedMove struct {
	SrcStash string
	DstStash string
	SrcLocal string
	DstLocal string
	Suffix   string
}

// Result records the outcome of applying a set of PlannedMoves.
type Result struct {
	Moved   []PlannedMove
	Skipped []Skip
	Failed  []Failure
}

// Skip is a sidecar deliberately left in place.
type Skip struct {
	Move   PlannedMove
	Reason string
}

// Failure is a sidecar whose move errored.
type Failure struct {
	Move PlannedMove
	Err  error
}

// Mover resolves and relocates the sidecars belonging to a video.
//
// It is safe for the FS to be nil-free but the Mover itself may be nil: callers
// that have sidecar support disabled pass a nil *Mover, and every method
// tolerates that by doing nothing. This keeps the call sites in organize free
// of feature-flag branching.
type Mover struct {
	// fs is the library filesystem: where videos live and sidecars must land.
	fs vfs.FS
	// src is where the files being moved are read from. It defaults to fs,
	// which is right for organize — a sidecar always starts beside its video.
	// `subs` overrides it, because loose subtitles routinely sit on a
	// different machine from the library.
	src  vfs.FS
	mp   *pathmap.Mapper
	exts []string
}

// NewMover builds a Mover. exts may be nil to use DefaultExtensions.
func NewMover(fs vfs.FS, mp *pathmap.Mapper, exts []string) (*Mover, error) {
	if fs == nil {
		return nil, errors.New("sidecar: nil filesystem")
	}
	return &Mover{fs: fs, src: fs, mp: mp, exts: exts}, nil
}

// WithSource returns a copy that reads its source files from src instead of
// from the library filesystem. Moves then stream across the two backends.
func (mv *Mover) WithSource(src vfs.FS) *Mover {
	if mv == nil || src == nil {
		return mv
	}
	cp := *mv
	cp.src = src
	return &cp
}

// SourceFS reports where source files are read from.
func (mv *Mover) SourceFS() vfs.FS {
	if mv == nil {
		return nil
	}
	return mv.src
}

// Describe reports the backend in use, for diagnostics.
func (mv *Mover) Describe() string {
	if mv == nil {
		return "disabled"
	}
	return mv.fs.Describe()
}

// Plan lists the sidecars next to srcVideo and computes where each must land
// beside dstVideo. Both arguments are Stash-space paths (/data/...).
//
// Plan must be called BEFORE the video is moved: it inspects the source
// directory, and reading it afterwards would miss the context needed to tell
// competing videos apart. Computing it up front is also what lets a dry run
// report sidecars accurately.
//
// A nil Mover returns no moves and no error.
func (mv *Mover) Plan(ctx context.Context, srcVideo, dstVideo string) ([]PlannedMove, error) {
	if mv == nil {
		return nil, nil
	}
	srcDir, srcBase := pathmap.Dir(srcVideo), pathmap.Base(srcVideo)
	dstDir, dstBase := pathmap.Dir(dstVideo), pathmap.Base(dstVideo)
	if srcBase == "" || dstBase == "" {
		return nil, fmt.Errorf("sidecar: cannot parse %q → %q", srcVideo, dstVideo)
	}

	srcDirLocal, err := mv.mp.ToLocal(srcDir)
	if err != nil {
		return nil, err
	}
	dstDirLocal, err := mv.mp.ToLocal(dstDir)
	if err != nil {
		return nil, err
	}

	entries, err := mv.fs.ReadDir(ctx, srcDirLocal)
	if err != nil {
		return nil, err
	}
	siblings := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir {
			siblings = append(siblings, e.Name)
		}
	}
	moves := Plan(srcBase, dstBase, siblings, mv.exts)

	out := make([]PlannedMove, 0, len(moves))
	for _, m := range moves {
		out = append(out, PlannedMove{
			SrcStash: pathmap.Join(srcDir, m.SrcBasename),
			DstStash: pathmap.Join(dstDir, m.DstBasename),
			SrcLocal: pathmap.Join(srcDirLocal, m.SrcBasename),
			DstLocal: pathmap.Join(dstDirLocal, m.DstBasename),
			Suffix:   m.Suffix,
		})
	}
	return out, nil
}

// Apply performs the planned moves.
//
// A sidecar whose destination already exists is skipped rather than
// overwritten: a clobbered subtitle cannot be recovered, while one left behind
// can be moved later. Failures are collected instead of aborting, so one
// unwritable file cannot strand the rest of a batch.
func (mv *Mover) Apply(ctx context.Context, moves []PlannedMove) Result {
	var res Result
	if mv == nil || len(moves) == 0 {
		return res
	}
	for _, m := range moves {
		exists, err := mv.fs.Exists(ctx, m.DstLocal)
		if err != nil {
			res.Failed = append(res.Failed, Failure{Move: m, Err: err})
			continue
		}
		if exists {
			res.Skipped = append(res.Skipped, Skip{Move: m, Reason: "destination already exists"})
			continue
		}
		if err := vfs.Move(ctx, mv.src, mv.fs, m.SrcLocal, m.DstLocal); err != nil {
			switch {
			case errors.Is(err, vfs.ErrExist):
				res.Skipped = append(res.Skipped, Skip{Move: m, Reason: "destination already exists"})
			case errors.Is(err, vfs.ErrNotExist):
				res.Skipped = append(res.Skipped, Skip{Move: m, Reason: "source vanished before the move"})
			default:
				res.Failed = append(res.Failed, Failure{Move: m, Err: err})
			}
			continue
		}
		res.Moved = append(res.Moved, m)
	}
	return res
}

// Total returns how many sidecars the result accounts for.
func (r Result) Total() int { return len(r.Moved) + len(r.Skipped) + len(r.Failed) }

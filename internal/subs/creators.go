package subs

import (
	"context"
	"fmt"

	stash "github.com/Anastylosis/stash-go"
)

// CreatorNames returns every performer and studio name in the library, plus
// their aliases.
//
// Subtitle matching uses these to tell "who made this" apart from "what it is
// called". Deriving the list from Stash rather than hard-coding it means it
// covers the whole library, stays correct as the library grows, and keeps
// personal names out of committed source.
//
// Blank entries are dropped. Stash lets an alias list hold an empty string,
// and a few in any real library do; an empty string is not a name, and
// letting one into a matching vocabulary is the kind of value that matches
// everything.
func CreatorNames(ctx context.Context, client *stash.Client) ([]string, error) {
	const perPage = 1000
	var out []string

	add := func(names ...string) {
		for _, n := range names {
			if n != "" {
				out = append(out, n)
			}
		}
	}

	for page := 1; ; page++ {
		performers, count, err := client.FindPerformers(ctx, stash.PerformerFilter{}, page, perPage)
		if err != nil {
			return nil, fmt.Errorf("findPerformers(page=%d): %w", page, err)
		}
		if len(performers) == 0 {
			break
		}
		for _, p := range performers {
			add(p.Name)
			add(p.Aliases...)
		}
		if page*perPage >= count {
			break
		}
	}

	for page := 1; ; page++ {
		studios, count, err := client.Studios(ctx, page, perPage)
		if err != nil {
			return nil, fmt.Errorf("findStudios(page=%d): %w", page, err)
		}
		if len(studios) == 0 {
			break
		}
		for _, s := range studios {
			add(s.Name)
			add(s.Aliases...)
		}
		if page*perPage >= count {
			break
		}
	}
	return out, nil
}

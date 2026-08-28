package reclaim

import (
	"slices"

	stash "github.com/Anastylosis/stash-go"
)

// fieldSnapshot is the JSON shape recorded in a "union" mutation's
// from_value/to_value (REBUILD.md §4.4's Reversal paragraph): the keeper's
// values for exactly the fields the union set, before and after. Pointer
// fields distinguish "the union left this alone" (nil, omitted from the
// JSON) from "the union set it to empty" (non-nil, pointing at "").
type fieldSnapshot struct {
	Title        *string         `json:"title,omitempty"`
	Code         *string         `json:"code,omitempty"`
	Date         *string         `json:"date,omitempty"`
	Details      *string         `json:"details,omitempty"`
	Director     *string         `json:"director,omitempty"`
	StudioID     *string         `json:"studio_id,omitempty"`
	Rating100    *int            `json:"rating100,omitempty"`
	Organized    *bool           `json:"organized,omitempty"`
	URLs         []string        `json:"urls,omitempty"`
	TagIDs       []string        `json:"tag_ids,omitempty"`
	PerformerIDs []string        `json:"performer_ids,omitempty"`
	GalleryIDs   []string        `json:"gallery_ids,omitempty"`
	StashIDs     []stash.StashID `json:"stash_ids,omitempty"`
}

// snapshotBefore reads the keeper's current values for exactly the named
// fields — called before UpdateScene sends the union, so it is the state
// undo needs to restore.
func snapshotBefore(keeper *stash.Scene, fields []string) fieldSnapshot {
	has := func(name string) bool { return slices.Contains(fields, name) }

	var s fieldSnapshot
	if has("title") {
		v := keeper.Title
		s.Title = &v
	}
	if has("code") {
		v := keeper.Code
		s.Code = &v
	}
	if has("date") {
		v := keeper.Date
		s.Date = &v
	}
	if has("details") {
		v := keeper.Details
		s.Details = &v
	}
	if has("director") {
		v := keeper.Director
		s.Director = &v
	}
	if has("studio_id") {
		v := ""
		if keeper.Studio != nil {
			v = keeper.Studio.ID
		}
		s.StudioID = &v
	}
	if has("rating100") {
		s.Rating100 = keeper.Rating100
	}
	if has("organized") {
		v := keeper.Organized
		s.Organized = &v
	}
	if has("urls") {
		s.URLs = append([]string(nil), keeper.URLs...)
	}
	if has("tag_ids") {
		s.TagIDs = tagIDsOf(keeper.Tags)
	}
	if has("performer_ids") {
		s.PerformerIDs = performerIDsOf(keeper.Performers)
	}
	if has("gallery_ids") {
		s.GalleryIDs = galleryIDsOf(keeper.Galleries)
	}
	if has("stash_ids") {
		s.StashIDs = append([]stash.StashID(nil), keeper.StashIDs...)
	}
	return s
}

// snapshotAfter reads the union's new values straight off the SceneUpdate it
// computed — every field there is already exactly what the union set.
func snapshotAfter(u stash.SceneUpdate) fieldSnapshot {
	return fieldSnapshot{
		Title: u.Title, Code: u.Code, Date: u.Date, Details: u.Details, Director: u.Director,
		StudioID: u.StudioID, Rating100: u.Rating100, Organized: u.Organized,
		URLs: u.URLs, TagIDs: u.TagIDs, PerformerIDs: u.PerformerIDs, GalleryIDs: u.GalleryIDs,
		StashIDs: u.StashIDs,
	}
}

// fieldValue reads one named field out of a snapshot as a plain value, for
// the changes JSON — nil when the snapshot does not carry that field.
func fieldValue(s fieldSnapshot, name string) any {
	switch name {
	case "title":
		return derefStr(s.Title)
	case "code":
		return derefStr(s.Code)
	case "date":
		return derefStr(s.Date)
	case "details":
		return derefStr(s.Details)
	case "director":
		return derefStr(s.Director)
	case "studio_id":
		return derefStr(s.StudioID)
	case "rating100":
		if s.Rating100 == nil {
			return nil
		}
		return *s.Rating100
	case "organized":
		if s.Organized == nil {
			return nil
		}
		return *s.Organized
	case "urls":
		return s.URLs
	case "tag_ids":
		return s.TagIDs
	case "performer_ids":
		return s.PerformerIDs
	case "gallery_ids":
		return s.GalleryIDs
	case "stash_ids":
		return s.StashIDs
	default:
		return nil
	}
}

func derefStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func tagIDsOf(tags []stash.Tag) []string {
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = t.ID
	}
	return out
}

func performerIDsOf(performers []stash.Performer) []string {
	out := make([]string, len(performers))
	for i, p := range performers {
		out[i] = p.ID
	}
	return out
}

func galleryIDsOf(galleries []stash.Gallery) []string {
	out := make([]string, len(galleries))
	for i, g := range galleries {
		out[i] = g.ID
	}
	return out
}

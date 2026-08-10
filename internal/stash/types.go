package stash

import upstream "github.com/Anastylosis/stash-go"

// The scene types are the shared client's — one definition, decoded from one
// selection set (upstream.SceneFields), which the queries in queries.go paste
// into their own GraphQL. Custodian's own input and result shapes follow.
type (
	// Scene is a Stash scene with its files, tags, performers, studio,
	// stash-box IDs and galleries.
	Scene = upstream.Scene
	// VideoFile is one video file backing a scene, with its fingerprints.
	VideoFile = upstream.File
	// Fingerprint is a single content hash for a file (oshash, md5, phash).
	Fingerprint = upstream.Fingerprint
	// Tag is a Stash tag.
	Tag = upstream.Tag
	// Performer is a Stash performer.
	Performer = upstream.Performer
	// Studio is a Stash studio.
	Studio = upstream.Studio
	// StashID associates a scene with an entry in a stash-box endpoint.
	StashID = upstream.StashID
	// Gallery is a gallery attached to a scene, used for the merge union.
	Gallery = upstream.Gallery
)

// FindScenesResult is the return type of findScenes, used for paginated
// enumeration in workflow B.
type FindScenesResult struct {
	Count  int     `json:"count"`
	Scenes []Scene `json:"scenes"`
}

// StashBoxConfig is one entry from configuration.general.stashBoxes —
// the list of stash-box endpoints the user has configured in Stash.
type StashBoxConfig struct {
	Endpoint string `json:"endpoint"`
	Name     string `json:"name"`
}

// ScrapedScene mirrors Stash's ScrapedScene type, the result of a
// scrapeMultiScenes / scrapeSingleScene call. We only carry the fields the
// orphans report needs.
type ScrapedScene struct {
	RemoteSiteID string             `json:"remote_site_id"`
	Title        string             `json:"title"`
	Date         string             `json:"date"`
	URLs         []string           `json:"urls"`
	Studio       *ScrapedStudio     `json:"studio"`
	Performers   []ScrapedPerformer `json:"performers"`
}

// ScrapedStudio is the studio sub-object on a ScrapedScene.
type ScrapedStudio struct {
	Name         string `json:"name"`
	RemoteSiteID string `json:"remote_site_id"`
}

// ScrapedPerformer is one entry in ScrapedScene.Performers.
type ScrapedPerformer struct {
	Name         string `json:"name"`
	RemoteSiteID string `json:"remote_site_id"`
}

// CriterionModifier mirrors the GraphQL enum used in scene filters.
type CriterionModifier string

const (
	ModifierEquals      CriterionModifier = "EQUALS"
	ModifierNotEquals   CriterionModifier = "NOT_EQUALS"
	ModifierGreaterThan CriterionModifier = "GREATER_THAN"
	ModifierLessThan    CriterionModifier = "LESS_THAN"
)

// BulkUpdateIDMode mirrors the GraphQL enum for bulk-update ID semantics.
type BulkUpdateIDMode string

const (
	BulkUpdateAdd    BulkUpdateIDMode = "ADD"
	BulkUpdateRemove BulkUpdateIDMode = "REMOVE"
	BulkUpdateSet    BulkUpdateIDMode = "SET"
)

// BulkUpdateIDs is the input shape for bulkSceneUpdate's tag_ids/etc fields.
// We always use Mode=ADD when tagging dedup losers/keepers so we don't
// accidentally drop existing tags.
type BulkUpdateIDs struct {
	IDs  []string         `json:"ids"`
	Mode BulkUpdateIDMode `json:"mode"`
}

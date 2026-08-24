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

// StashBoxConfig is one stash-box endpoint the user has configured in Stash.
type StashBoxConfig = upstream.StashBox

// The scraped shapes are the shared client's. Custodian reads a handful of
// fields off them — the remote id, title, date, studio and performers — and
// the shared type carries those plus the ones other consumers need.
type (
	// ScrapedScene is one candidate a stash-box returned for a scene.
	ScrapedScene = upstream.ScrapedScene
	// ScrapedStudio is the studio sub-object on a ScrapedScene.
	ScrapedStudio = upstream.ScrapedStudio
	// ScrapedPerformer is one entry in ScrapedScene.Performers.
	ScrapedPerformer = upstream.ScrapedPerformer
)

package ledger

import (
	"context"
	"database/sql"
	"fmt"

	stash "github.com/Anastylosis/stash-go"
)

// UpsertScenes writes one page of scenes into the ledger in a single
// transaction, replacing each scene's child tables wholesale.
func (l *Ledger) UpsertScenes(ctx context.Context, runID int64, scenes []stash.Scene) error {
	if len(scenes) == 0 {
		return nil
	}

	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ledger: beginning upsert transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmts, err := prepareUpsertStatements(ctx, tx)
	if err != nil {
		return err
	}
	defer stmts.close()

	for _, s := range scenes {
		if err := stmts.upsertScene(ctx, runID, s); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ledger: committing upsert: %w", err)
	}
	return nil
}

// upsertStatements are reused across every scene in one UpsertScenes call.
type upsertStatements struct {
	scene        *sql.Stmt
	delPerformer *sql.Stmt
	delTag       *sql.Stmt
	delURL       *sql.Stmt
	delStashID   *sql.Stmt
	insPerformer *sql.Stmt
	insTag       *sql.Stmt
	insURL       *sql.Stmt
	insStashID   *sql.Stmt
	file         *sql.Stmt
}

func prepareUpsertStatements(ctx context.Context, tx *sql.Tx) (*upsertStatements, error) {
	prep := func(query string) (*sql.Stmt, error) { return tx.PrepareContext(ctx, query) }

	var s upsertStatements
	var err error
	for _, step := range []struct {
		dst   **sql.Stmt
		query string
	}{
		{&s.scene, `INSERT INTO scene(id, title, code, date, studio_id, studio_name,
				organized, rating100, play_count, updated_at, seen_run, gone_run)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
			ON CONFLICT(id) DO UPDATE SET
				title = excluded.title, code = excluded.code, date = excluded.date,
				studio_id = excluded.studio_id, studio_name = excluded.studio_name,
				organized = excluded.organized, rating100 = excluded.rating100,
				play_count = excluded.play_count, updated_at = excluded.updated_at,
				seen_run = excluded.seen_run, gone_run = NULL`},
		{&s.delPerformer, `DELETE FROM scene_performer WHERE scene_id = ?`},
		{&s.delTag, `DELETE FROM scene_tag WHERE scene_id = ?`},
		{&s.delURL, `DELETE FROM scene_url WHERE scene_id = ?`},
		{&s.delStashID, `DELETE FROM scene_stash_id WHERE scene_id = ?`},
		{&s.insPerformer, `INSERT INTO scene_performer(scene_id, performer_id, name) VALUES(?, ?, ?)`},
		{&s.insTag, `INSERT INTO scene_tag(scene_id, tag_id, name) VALUES(?, ?, ?)`},
		{&s.insURL, `INSERT INTO scene_url(scene_id, url) VALUES(?, ?)`},
		{&s.insStashID, `INSERT INTO scene_stash_id(scene_id, endpoint, stash_id) VALUES(?, ?, ?)`},
		{&s.file, `INSERT INTO file(id, scene_id, path, basename, size, mod_time, width, height,
				duration, video_codec, audio_codec, frame_rate, bit_rate, is_primary,
				oshash, md5, phash, seen_run, gone_run)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
			ON CONFLICT(id) DO UPDATE SET
				scene_id = excluded.scene_id, path = excluded.path, basename = excluded.basename,
				size = excluded.size, mod_time = excluded.mod_time, width = excluded.width,
				height = excluded.height, duration = excluded.duration,
				video_codec = excluded.video_codec, audio_codec = excluded.audio_codec,
				frame_rate = excluded.frame_rate, bit_rate = excluded.bit_rate,
				is_primary = excluded.is_primary, oshash = excluded.oshash, md5 = excluded.md5,
				phash = excluded.phash, seen_run = excluded.seen_run, gone_run = NULL`},
	} {
		*step.dst, err = prep(step.query)
		if err != nil {
			s.close()
			return nil, fmt.Errorf("ledger: preparing upsert statements: %w", err)
		}
	}
	return &s, nil
}

func (s *upsertStatements) close() {
	for _, stmt := range []*sql.Stmt{
		s.scene, s.delPerformer, s.delTag, s.delURL, s.delStashID,
		s.insPerformer, s.insTag, s.insURL, s.insStashID, s.file,
	} {
		if stmt != nil {
			_ = stmt.Close()
		}
	}
}

func (s *upsertStatements) upsertScene(ctx context.Context, runID int64, sc stash.Scene) error {
	studioID, studioName := "", ""
	if sc.Studio != nil {
		studioID, studioName = sc.Studio.ID, sc.Studio.Name
	}

	if _, err := s.scene.ExecContext(ctx, sc.ID, sc.Title, sc.Code, sc.Date, studioID, studioName,
		boolToInt(sc.Organized), sc.Rating100, sc.PlayCount, sc.UpdatedAt, runID); err != nil {
		return fmt.Errorf("ledger: upserting scene %s: %w", sc.ID, err)
	}

	if err := s.replaceChildren(ctx, sc); err != nil {
		return err
	}

	for i, f := range sc.Files {
		oshash := fingerprintPtr(f, "oshash")
		md5 := fingerprintPtr(f, "md5")
		phash := fingerprintPtr(f, "phash")
		if _, err := s.file.ExecContext(ctx, f.ID, sc.ID, f.Path, f.Basename, f.Size, f.ModTime,
			f.Width, f.Height, f.Duration, f.VideoCodec, f.AudioCodec, f.FrameRate, f.BitRate,
			boolToInt(i == 0), oshash, md5, phash, runID); err != nil {
			return fmt.Errorf("ledger: upserting file %s (scene %s): %w", f.ID, sc.ID, err)
		}
	}
	return nil
}

func (s *upsertStatements) replaceChildren(ctx context.Context, sc stash.Scene) error {
	if _, err := s.delPerformer.ExecContext(ctx, sc.ID); err != nil {
		return fmt.Errorf("ledger: clearing performers for scene %s: %w", sc.ID, err)
	}
	for _, p := range sc.Performers {
		if _, err := s.insPerformer.ExecContext(ctx, sc.ID, p.ID, p.Name); err != nil {
			return fmt.Errorf("ledger: adding performer to scene %s: %w", sc.ID, err)
		}
	}

	if _, err := s.delTag.ExecContext(ctx, sc.ID); err != nil {
		return fmt.Errorf("ledger: clearing tags for scene %s: %w", sc.ID, err)
	}
	for _, t := range sc.Tags {
		if _, err := s.insTag.ExecContext(ctx, sc.ID, t.ID, t.Name); err != nil {
			return fmt.Errorf("ledger: adding tag to scene %s: %w", sc.ID, err)
		}
	}

	if _, err := s.delURL.ExecContext(ctx, sc.ID); err != nil {
		return fmt.Errorf("ledger: clearing urls for scene %s: %w", sc.ID, err)
	}
	for _, u := range sc.URLs {
		if _, err := s.insURL.ExecContext(ctx, sc.ID, u); err != nil {
			return fmt.Errorf("ledger: adding url to scene %s: %w", sc.ID, err)
		}
	}

	if _, err := s.delStashID.ExecContext(ctx, sc.ID); err != nil {
		return fmt.Errorf("ledger: clearing stash_ids for scene %s: %w", sc.ID, err)
	}
	for _, id := range sc.StashIDs {
		if _, err := s.insStashID.ExecContext(ctx, sc.ID, id.Endpoint, id.ID); err != nil {
			return fmt.Errorf("ledger: adding stash_id to scene %s: %w", sc.ID, err)
		}
	}
	return nil
}

// TombstoneUnseen marks scenes and files with seen_run < runID as gone.
// Only meaningful after a full sync.
func (l *Ledger) TombstoneUnseen(ctx context.Context, runID int64) (scenes, files int, err error) {
	sceneRes, err := l.db.ExecContext(ctx,
		`UPDATE scene SET gone_run = ? WHERE seen_run < ? AND gone_run IS NULL`, runID, runID)
	if err != nil {
		return 0, 0, fmt.Errorf("ledger: tombstoning scenes: %w", err)
	}
	sceneCount, err := sceneRes.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("ledger: counting tombstoned scenes: %w", err)
	}

	fileRes, err := l.db.ExecContext(ctx,
		`UPDATE file SET gone_run = ? WHERE seen_run < ? AND gone_run IS NULL`, runID, runID)
	if err != nil {
		return 0, 0, fmt.Errorf("ledger: tombstoning files: %w", err)
	}
	fileCount, err := fileRes.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("ledger: counting tombstoned files: %w", err)
	}

	return int(sceneCount), int(fileCount), nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func fingerprintPtr(f stash.File, kind string) *string {
	if v, ok := f.Fingerprint(kind); ok {
		return &v
	}
	return nil
}

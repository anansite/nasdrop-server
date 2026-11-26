package db

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
	mu sync.RWMutex
}

func Open(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db failed: %w", err)
	}

	// 启用 WAL 模式提高读写并发性能与死锁超时保护
	_, _ = db.Exec("PRAGMA journal_mode=WAL;")
	_, _ = db.Exec("PRAGMA synchronous=NORMAL;")
	_, _ = db.Exec("PRAGMA busy_timeout=5000;")
	_, _ = db.Exec("PRAGMA cache_size=-2000;")
	_, _ = db.Exec("PRAGMA foreign_keys=ON;")

	schema := `
	CREATE TABLE IF NOT EXISTS photo_metadata (
		path TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		dir TEXT NOT NULL,
		size INTEGER NOT NULL DEFAULT 0,
		modified INTEGER NOT NULL DEFAULT 0,
		taken_at INTEGER NOT NULL DEFAULT 0,
		width INTEGER NOT NULL DEFAULT 0,
		height INTEGER NOT NULL DEFAULT 0,
		has_thumb INTEGER NOT NULL DEFAULT 0,
		thumb_id TEXT NOT NULL DEFAULT '',
		voice_id TEXT NOT NULL DEFAULT '',
		voice_duration_ms INTEGER NOT NULL DEFAULT 0,
		memo TEXT NOT NULL DEFAULT '',
		lat REAL NOT NULL DEFAULT 0,
		lng REAL NOT NULL DEFAULT 0,
		location_name TEXT NOT NULL DEFAULT '',
		updated_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_photo_dir ON photo_metadata(dir);
	CREATE INDEX IF NOT EXISTS idx_photo_taken ON photo_metadata(taken_at);
	CREATE INDEX IF NOT EXISTS idx_photo_dir_taken ON photo_metadata(dir, taken_at DESC);

	CREATE TABLE IF NOT EXISTS photo_groups (
		id TEXT PRIMARY KEY,
		album_dir TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL,
		memo TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_photo_groups_album ON photo_groups(album_dir);

	CREATE TABLE IF NOT EXISTS photo_group_items (
		group_id TEXT NOT NULL,
		photo_path TEXT NOT NULL,
		sort_order INTEGER NOT NULL DEFAULT 0,
		added_at INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (group_id, photo_path),
		FOREIGN KEY (group_id) REFERENCES photo_groups(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_group_items_path ON photo_group_items(photo_path);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("create schema failed: %w", err)
	}
	// 兼容旧库平滑迁移新增扩展字段
	_, _ = db.Exec("ALTER TABLE photo_metadata ADD COLUMN thumb_id TEXT NOT NULL DEFAULT ''")
	_, _ = db.Exec("ALTER TABLE photo_metadata ADD COLUMN voice_id TEXT NOT NULL DEFAULT ''")
	_, _ = db.Exec("ALTER TABLE photo_metadata ADD COLUMN voice_duration_ms INTEGER NOT NULL DEFAULT 0")
	_, _ = db.Exec("ALTER TABLE photo_metadata ADD COLUMN memo TEXT NOT NULL DEFAULT ''")
	_, _ = db.Exec("ALTER TABLE photo_metadata ADD COLUMN lat REAL NOT NULL DEFAULT 0")
	_, _ = db.Exec("ALTER TABLE photo_metadata ADD COLUMN lng REAL NOT NULL DEFAULT 0")
	_, _ = db.Exec("ALTER TABLE photo_metadata ADD COLUMN location_name TEXT NOT NULL DEFAULT ''")

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Upsert(meta *PhotoMetadata) error {
	return s.UpsertBatch([]PhotoMetadata{*meta})
}

func (s *Store) UpsertBatch(metas []PhotoMetadata) error {
	if len(metas) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `
	INSERT INTO photo_metadata (path, name, dir, size, modified, taken_at, width, height, has_thumb, thumb_id, voice_id, voice_duration_ms, memo, lat, lng, location_name, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(path) DO UPDATE SET
		name=excluded.name,
		dir=excluded.dir,
		size=excluded.size,
		modified=excluded.modified,
		taken_at=excluded.taken_at,
		width=excluded.width,
		height=excluded.height,
		has_thumb=CASE WHEN excluded.has_thumb = 1 OR excluded.thumb_id != '' THEN 1 ELSE photo_metadata.has_thumb END,
		thumb_id=CASE WHEN excluded.thumb_id != '' THEN excluded.thumb_id ELSE photo_metadata.thumb_id END,
		voice_id=CASE WHEN excluded.voice_id != '' THEN excluded.voice_id ELSE photo_metadata.voice_id END,
		voice_duration_ms=CASE WHEN excluded.voice_duration_ms > 0 THEN excluded.voice_duration_ms ELSE photo_metadata.voice_duration_ms END,
		memo=CASE WHEN excluded.memo != '' THEN excluded.memo ELSE photo_metadata.memo END,
		lat=CASE WHEN excluded.lat != 0 THEN excluded.lat ELSE photo_metadata.lat END,
		lng=CASE WHEN excluded.lng != 0 THEN excluded.lng ELSE photo_metadata.lng END,
		location_name=CASE WHEN excluded.location_name != '' THEN excluded.location_name ELSE photo_metadata.location_name END,
		updated_at=excluded.updated_at;
	`
	stmt, err := tx.Prepare(query)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().UnixMilli()
	for i := range metas {
		m := &metas[i]
		hasThumbInt := 0
		if m.HasThumb {
			hasThumbInt = 1
		}
		updatedAt := m.UpdatedAt
		if updatedAt == 0 {
			updatedAt = now
		}
		if _, err := stmt.Exec(
			strings.Trim(m.Path, "/"),
			m.Name,
			strings.Trim(m.Dir, "/"),
			m.Size,
			m.Modified,
			m.TakenAt,
			m.Width,
			m.Height,
			hasThumbInt,
			strings.TrimSpace(m.ThumbID),
			strings.TrimSpace(m.VoiceID),
			m.VoiceDurationMs,
			m.Memo,
			m.Lat,
			m.Lng,
			strings.TrimSpace(m.LocationName),
			updatedAt,
		); err != nil {
			return err
		}
		if m.ClearVoice {
			if _, err := tx.Exec(`UPDATE photo_metadata SET voice_id='', voice_duration_ms=0, updated_at=? WHERE path=?`, updatedAt, strings.Trim(m.Path, "/")); err != nil {
				return err
			}
		}
		if m.ClearMemo {
			if _, err := tx.Exec(`UPDATE photo_metadata SET memo='', updated_at=? WHERE path=?`, updatedAt, strings.Trim(m.Path, "/")); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) Delete(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	clean := strings.Trim(path, "/")
	_, _ = s.db.Exec("DELETE FROM photo_group_items WHERE photo_path = ?", clean)
	_, err := s.db.Exec("DELETE FROM photo_metadata WHERE path = ?", clean)
	return err
}

func (s *Store) DeleteDir(dirPrefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	clean := strings.Trim(dirPrefix, "/")
	if clean == "" {
		_, _ = s.db.Exec("DELETE FROM photo_group_items")
		_, err := s.db.Exec("DELETE FROM photo_metadata")
		return err
	}
	_, _ = s.db.Exec("DELETE FROM photo_group_items WHERE photo_path = ? OR photo_path LIKE ?", clean, clean+"/%")
	_, err := s.db.Exec("DELETE FROM photo_metadata WHERE path = ? OR path LIKE ?", clean, clean+"/%")
	return err
}

func (s *Store) Move(oldPath, newPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldClean := strings.Trim(oldPath, "/")
	newClean := strings.Trim(newPath, "/")

	newName := newClean
	newDir := ""
	if idx := strings.LastIndex(newClean, "/"); idx >= 0 {
		newDir = newClean[:idx]
		newName = newClean[idx+1:]
	}

	_, _ = s.db.Exec("UPDATE photo_group_items SET photo_path = ? WHERE photo_path = ?", newClean, oldClean)
	query := `
	UPDATE photo_metadata 
	SET path = ?, name = ?, dir = ?, updated_at = ?
	WHERE path = ?;
	`
	_, err := s.db.Exec(query, newClean, newName, newDir, time.Now().UnixMilli(), oldClean)
	return err
}

func (s *Store) MoveDir(oldPrefix, newPrefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldClean := strings.Trim(oldPrefix, "/")
	newClean := strings.Trim(newPrefix, "/")

	rows, err := s.db.Query("SELECT path, name, dir FROM photo_metadata WHERE path = ? OR path LIKE ?", oldClean, oldClean+"/%")
	if err != nil {
		return err
	}
	defer rows.Close()

	type moveItem struct {
		oldP string
		newP string
		newN string
		newD string
	}
	var moves []moveItem

	for rows.Next() {
		var p, n, d string
		if err := rows.Scan(&p, &n, &d); err != nil {
			continue
		}
		replaced := newClean + strings.TrimPrefix(p, oldClean)
		newD := ""
		newN := replaced
		if idx := strings.LastIndex(replaced, "/"); idx >= 0 {
			newD = replaced[:idx]
			newN = replaced[idx+1:]
		}
		moves = append(moves, moveItem{oldP: p, newP: replaced, newN: newN, newD: newD})
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("UPDATE photo_metadata SET path = ?, name = ?, dir = ?, updated_at = ? WHERE path = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().UnixMilli()
	for _, m := range moves {
		if _, err := stmt.Exec(m.newP, m.newN, m.newD, now, m.oldP); err != nil {
			log.Printf("[DB] MoveDir update error for %s: %v", m.oldP, err)
		}
	}

	return tx.Commit()
}

func (s *Store) GetTimeline(limit, offset int, dir string) ([]PhotoMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var rows *sql.Rows
	var err error

	cleanDir := strings.Trim(dir, "/")
	if cleanDir == "" {
		rows, err = s.db.Query(`
			SELECT path, name, dir, size, modified, taken_at, width, height, has_thumb, thumb_id, voice_id, voice_duration_ms, memo, lat, lng, location_name, updated_at
			FROM photo_metadata
			ORDER BY taken_at DESC
			LIMIT ? OFFSET ?
		`, limit, offset)
	} else {
		rows, err = s.db.Query(`
			SELECT path, name, dir, size, modified, taken_at, width, height, has_thumb, thumb_id, voice_id, voice_duration_ms, memo, lat, lng, location_name, updated_at
			FROM photo_metadata
			WHERE dir = ? OR dir LIKE ?
			ORDER BY taken_at DESC
			LIMIT ? OFFSET ?
		`, cleanDir, cleanDir+"/%", limit, offset)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []PhotoMetadata
	for rows.Next() {
		var m PhotoMetadata
		var hasThumbInt int
		if err := rows.Scan(&m.Path, &m.Name, &m.Dir, &m.Size, &m.Modified, &m.TakenAt, &m.Width, &m.Height, &hasThumbInt, &m.ThumbID, &m.VoiceID, &m.VoiceDurationMs, &m.Memo, &m.Lat, &m.Lng, &m.LocationName, &m.UpdatedAt); err != nil {
			continue
		}
		m.HasThumb = hasThumbInt == 1
		list = append(list, m)
	}
	return list, nil
}

type Stats struct {
	Photos   int `json:"photos"`
	Videos   int `json:"videos"`
	Albums   int `json:"albums"`
	Memories int `json:"memories"`
}

func (s *Store) GetStats() (Stats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var st Stats
	rows, err := s.db.Query(`SELECT path, dir, voice_id, memo FROM photo_metadata`)
	if err != nil {
		return st, err
	}
	defer rows.Close()

	albumSet := make(map[string]bool)
	for rows.Next() {
		var p, dir, voiceID, memo string
		if err := rows.Scan(&p, &dir, &voiceID, &memo); err != nil {
			continue
		}
		lower := strings.ToLower(p)
		if strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".mov") || strings.HasSuffix(lower, ".mkv") ||
			strings.HasSuffix(lower, ".avi") || strings.HasSuffix(lower, ".webm") || strings.HasSuffix(lower, ".m4v") ||
			strings.HasSuffix(lower, ".3gp") {
			st.Videos++
		} else {
			st.Photos++
		}
		if dir != "" {
			albumSet[dir] = true
		}
		if voiceID != "" || memo != "" {
			st.Memories++
		}
	}
	st.Albums = len(albumSet)
	return st, nil
}

func (s *Store) GetTimelineCount(dir string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cleanDir := strings.Trim(dir, "/")
	var total int
	var err error
	if cleanDir == "" {
		err = s.db.QueryRow(`SELECT COUNT(*) FROM photo_metadata`).Scan(&total)
	} else {
		err = s.db.QueryRow(`SELECT COUNT(*) FROM photo_metadata WHERE dir = ? OR dir LIKE ?`, cleanDir, cleanDir+"/%").Scan(&total)
	}
	return total, err
}

func (s *Store) SyncDir(dir string, live []PhotoMetadata) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cleanDir := strings.Trim(dir, "/")
	liveMap := make(map[string]bool, len(live))
	for _, m := range live {
		liveMap[strings.Trim(m.Path, "/")] = true
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO photo_metadata (path, name, dir, size, modified, taken_at, width, height, has_thumb, thumb_id, voice_id, voice_duration_ms, memo, lat, lng, location_name, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, '', '', 0, '', 0, 0, '', ?)
	`)
	if err != nil {
		return 0, 0, err
	}
	defer stmt.Close()

	now := time.Now().UnixMilli()
	added := 0
	for _, m := range live {
		res, err := stmt.Exec(strings.Trim(m.Path, "/"), m.Name, strings.Trim(m.Dir, "/"), m.Size, m.Modified, m.TakenAt, now)
		if err != nil {
			return 0, 0, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}

	var rows *sql.Rows
	if cleanDir == "" {
		rows, err = tx.Query(`SELECT path FROM photo_metadata`)
	} else {
		rows, err = tx.Query(`SELECT path FROM photo_metadata WHERE dir = ? OR dir LIKE ?`, cleanDir, cleanDir+"/%")
	}
	if err != nil {
		return 0, 0, err
	}
	var toDelete []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil && !liveMap[p] {
			toDelete = append(toDelete, p)
		}
	}
	rows.Close()

	for _, p := range toDelete {
		_, _ = tx.Exec(`DELETE FROM photo_metadata WHERE path = ?`, p)
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return added, len(toDelete), nil
}

func (s *Store) GetThumbID(path string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var id string
	_ = s.db.QueryRow(`SELECT thumb_id FROM photo_metadata WHERE path = ?`, strings.Trim(path, "/")).Scan(&id)
	return id
}

// GetMemories 查询历史年份在今日（或指定月日）拍摄的照片与视频列表
func (s *Store) GetMemories(monthDay string, currentYear string, limit int) ([]PhotoMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT path, name, dir, size, modified, taken_at, width, height, has_thumb, thumb_id, voice_id, voice_duration_ms, memo, lat, lng, location_name, updated_at
		FROM photo_metadata
		WHERE taken_at > 0
		  AND strftime('%m-%d', datetime(taken_at / 1000, 'unixepoch', 'localtime')) = ?
		  AND strftime('%Y', datetime(taken_at / 1000, 'unixepoch', 'localtime')) < ?
		ORDER BY taken_at DESC
		LIMIT ?;
	`
	rows, err := s.db.Query(query, monthDay, currentYear, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []PhotoMetadata
	for rows.Next() {
		var m PhotoMetadata
		var hasThumbInt int
		if err := rows.Scan(&m.Path, &m.Name, &m.Dir, &m.Size, &m.Modified, &m.TakenAt, &m.Width, &m.Height, &hasThumbInt, &m.ThumbID, &m.VoiceID, &m.VoiceDurationMs, &m.Memo, &m.Lat, &m.Lng, &m.LocationName, &m.UpdatedAt); err != nil {
			continue
		}
		m.HasThumb = hasThumbInt == 1
		list = append(list, m)
	}
	return list, nil
}

func (s *Store) Reconcile(dir string, livePaths []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cleanDir := strings.Trim(dir, "/")
	rows, err := s.db.Query("SELECT path FROM photo_metadata WHERE dir = ?", cleanDir)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	liveMap := make(map[string]bool)
	for _, p := range livePaths {
		liveMap[strings.Trim(p, "/")] = true
	}

	var toDelete []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil {
			if !liveMap[p] {
				toDelete = append(toDelete, p)
			}
		}
	}

	if len(toDelete) > 0 {
		tx, err := s.db.Begin()
		if err == nil {
			defer tx.Rollback()
			stmt, err := tx.Prepare("DELETE FROM photo_metadata WHERE path = ?")
			if err == nil {
				defer stmt.Close()
				for _, p := range toDelete {
					_, _ = stmt.Exec(p)
				}
				_ = tx.Commit()
			}
		}
	}
	return toDelete, nil
}

// ==================== 相册串联组 (Photo Groups) CRUD ====================

func (s *Store) ListGroups(albumDir string) ([]PhotoGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.listGroupsLocked(albumDir)
}

func (s *Store) listGroupsLocked(albumDir string) ([]PhotoGroup, error) {
	cleanAlbum := strings.Trim(albumDir, "/")
	var rows *sql.Rows
	var err error
	if cleanAlbum == "" {
		rows, err = s.db.Query(`SELECT id, album_dir, title, memo, created_at, updated_at FROM photo_groups ORDER BY updated_at DESC`)
	} else {
		rows, err = s.db.Query(`SELECT id, album_dir, title, memo, created_at, updated_at FROM photo_groups WHERE album_dir = ? ORDER BY updated_at DESC`, cleanAlbum)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := make([]PhotoGroup, 0)
	idxByID := make(map[string]int)
	for rows.Next() {
		var g PhotoGroup
		if err := rows.Scan(&g.ID, &g.AlbumDir, &g.Title, &g.Memo, &g.CreatedAt, &g.UpdatedAt); err != nil {
			continue
		}
		g.Items = []string{}
		idxByID[g.ID] = len(groups)
		groups = append(groups, g)
	}

	if len(groups) == 0 {
		return groups, nil
	}

	itemRows, err := s.db.Query(`SELECT group_id, photo_path FROM photo_group_items ORDER BY sort_order ASC, added_at ASC`)
	if err != nil {
		return groups, nil
	}
	defer itemRows.Close()

	for itemRows.Next() {
		var gid, p string
		if err := itemRows.Scan(&gid, &p); err != nil {
			continue
		}
		if i, ok := idxByID[gid]; ok {
			groups[i].Items = append(groups[i].Items, p)
		}
	}
	return groups, nil
}

func (s *Store) getGroupByIDLocked(id string) (*PhotoGroup, error) {
	var g PhotoGroup
	err := s.db.QueryRow(`SELECT id, album_dir, title, memo, created_at, updated_at FROM photo_groups WHERE id = ?`, id).
		Scan(&g.ID, &g.AlbumDir, &g.Title, &g.Memo, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		return nil, err
	}
	g.Items = []string{}
	rows, err := s.db.Query(`SELECT photo_path FROM photo_group_items WHERE group_id = ? ORDER BY sort_order ASC, added_at ASC`, id)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err == nil {
				g.Items = append(g.Items, p)
			}
		}
	}
	return &g, nil
}

func (s *Store) CreateGroup(g *PhotoGroup) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	if g.ID == "" {
		g.ID = fmt.Sprintf("grp_%d", now)
	}
	if g.CreatedAt == 0 {
		g.CreatedAt = now
	}
	g.UpdatedAt = now
	g.AlbumDir = strings.Trim(g.AlbumDir, "/")

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO photo_groups (id, album_dir, title, memo, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		g.ID, g.AlbumDir, g.Title, g.Memo, g.CreatedAt, g.UpdatedAt,
	); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO photo_group_items (group_id, photo_path, sort_order, added_at) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	cleanItems := make([]string, 0, len(g.Items))
	for i, rawPath := range g.Items {
		p := strings.Trim(rawPath, "/")
		if p == "" {
			continue
		}
		cleanItems = append(cleanItems, p)
		_, _ = stmt.Exec(g.ID, p, i, now)
	}
	g.Items = cleanItems

	return tx.Commit()
}

func (s *Store) UpdateGroup(id string, title *string, memo *string, items []string, replaceItems bool) (*PhotoGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, err := s.getGroupByIDLocked(id)
	if err != nil {
		return nil, err
	}

	newTitle := cur.Title
	if title != nil {
		newTitle = *title
	}
	newMemo := cur.Memo
	if memo != nil {
		newMemo = *memo
	}
	now := time.Now().UnixMilli()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE photo_groups SET title = ?, memo = ?, updated_at = ? WHERE id = ?`, newTitle, newMemo, now, id); err != nil {
		return nil, err
	}

	if replaceItems {
		if _, err := tx.Exec(`DELETE FROM photo_group_items WHERE group_id = ?`, id); err != nil {
			return nil, err
		}
		stmt, err := tx.Prepare(`INSERT OR IGNORE INTO photo_group_items (group_id, photo_path, sort_order, added_at) VALUES (?, ?, ?, ?)`)
		if err != nil {
			return nil, err
		}
		defer stmt.Close()
		for i, rawPath := range items {
			p := strings.Trim(rawPath, "/")
			if p == "" {
				continue
			}
			_, _ = stmt.Exec(id, p, i, now)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getGroupByIDLocked(id)
}

func (s *Store) AppendGroupItems(groupID string, items []string) (*PhotoGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.getGroupByIDLocked(groupID); err != nil {
		return nil, err
	}

	var maxOrder int
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(sort_order), -1) FROM photo_group_items WHERE group_id = ?`, groupID).Scan(&maxOrder)

	now := time.Now().UnixMilli()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO photo_group_items (group_id, photo_path, sort_order, added_at) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	order := maxOrder + 1
	for _, rawPath := range items {
		p := strings.Trim(rawPath, "/")
		if p == "" {
			continue
		}
		if res, err := stmt.Exec(groupID, p, order, now); err == nil {
			if aff, _ := res.RowsAffected(); aff > 0 {
				order++
			}
		}
	}
	_, _ = tx.Exec(`UPDATE photo_groups SET updated_at = ? WHERE id = ?`, now, groupID)

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getGroupByIDLocked(groupID)
}

func (s *Store) RemoveGroupItem(groupID, photoPath string) (*PhotoGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	clean := strings.Trim(photoPath, "/")
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`DELETE FROM photo_group_items WHERE group_id = ? AND photo_path = ?`, groupID, clean); err != nil {
		return nil, err
	}
	_, _ = s.db.Exec(`UPDATE photo_groups SET updated_at = ? WHERE id = ?`, now, groupID)
	return s.getGroupByIDLocked(groupID)
}

func (s *Store) DeleteGroup(groupID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM photo_groups WHERE id = ?`, groupID)
	return err
}

package db

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

var groupIDSeq atomic.Uint64

type Store struct {
	db *sql.DB
	mu sync.RWMutex
}

func Open(dbPath string) (*Store, error) {
	// 通过 DSN 参数设置 PRAGMAs，确保连接池中所有连接都生效
	dsn := dbPath +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=cache_size(-2000)" +
		"&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db failed: %w", err)
	}

	// 限制连接数：SQLite 单写者模型，多连接无意义且会丢失 PRAGMA 状态
	db.SetMaxOpenConns(2)

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
		db.Close()
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

// escapeLike 转义 SQL LIKE 中的 %、_ 和 \ 通配符，防止路径名含特殊字符时匹配错误
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
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
		name=CASE WHEN excluded.name != '' THEN excluded.name ELSE photo_metadata.name END,
		dir=CASE WHEN excluded.dir != '' OR photo_metadata.dir = '' THEN excluded.dir ELSE photo_metadata.dir END,
		size=CASE WHEN excluded.size > 0 THEN excluded.size ELSE photo_metadata.size END,
		modified=CASE WHEN excluded.modified > 0 THEN excluded.modified ELSE photo_metadata.modified END,
		taken_at=CASE WHEN excluded.taken_at > 0 THEN excluded.taken_at ELSE photo_metadata.taken_at END,
		width=CASE WHEN excluded.width > 0 THEN excluded.width ELSE photo_metadata.width END,
		height=CASE WHEN excluded.height > 0 THEN excluded.height ELSE photo_metadata.height END,
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
	if clean == "" {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM photo_group_items WHERE photo_path = ?", clean); err != nil {
		log.Printf("[DB] Delete group_items for %s: %v", clean, err)
	}
	if _, err := tx.Exec("DELETE FROM photo_metadata WHERE path = ?", clean); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteDir(dirPrefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	clean := strings.Trim(dirPrefix, "/")
	if clean == "" {
		return nil // 拒绝清空全库
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	escaped := escapeLike(clean)
	if _, err := tx.Exec("DELETE FROM photo_group_items WHERE photo_path = ? OR photo_path LIKE ? ESCAPE '\\'", clean, escaped+"/%"); err != nil {
		log.Printf("[DB] DeleteDir group_items for %s: %v", clean, err)
	}
	// 同步删除该目录下的 photo_groups
	if _, err := tx.Exec("DELETE FROM photo_groups WHERE album_dir = ? OR album_dir LIKE ? ESCAPE '\\'", clean, escaped+"/%"); err != nil {
		log.Printf("[DB] DeleteDir groups for %s: %v", clean, err)
	}
	if _, err := tx.Exec("DELETE FROM photo_metadata WHERE path = ? OR path LIKE ? ESCAPE '\\'", clean, escaped+"/%"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Move(oldPath, newPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldClean := strings.Trim(oldPath, "/")
	newClean := strings.Trim(newPath, "/")
	if oldClean == "" || newClean == "" || oldClean == newClean {
		return nil
	}

	newName := newClean
	newDir := ""
	if idx := strings.LastIndex(newClean, "/"); idx >= 0 {
		newDir = newClean[:idx]
		newName = newClean[idx+1:]
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()
	// 处理 Overwrite: T 覆盖目标：若目标已存在于集锦或元数据表，先清理冲突项避免 UNIQUE 约束失败
	if _, err := tx.Exec("DELETE FROM photo_group_items WHERE photo_path = ? AND group_id IN (SELECT group_id FROM photo_group_items WHERE photo_path = ?)", newClean, oldClean); err != nil {
		log.Printf("[DB] Move dedup group_items %s->%s: %v", oldClean, newClean, err)
	}
	if _, err := tx.Exec("UPDATE photo_group_items SET photo_path = ? WHERE photo_path = ?", newClean, oldClean); err != nil {
		log.Printf("[DB] Move group_items %s->%s: %v", oldClean, newClean, err)
	}
	_, _ = tx.Exec("DELETE FROM photo_metadata WHERE path = ?", newClean)
	if _, err := tx.Exec(`UPDATE photo_metadata SET path = ?, name = ?, dir = ?, updated_at = ? WHERE path = ?`,
		newClean, newName, newDir, now, oldClean); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MoveDir(oldPrefix, newPrefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldClean := strings.Trim(oldPrefix, "/")
	newClean := strings.Trim(newPrefix, "/")
	if oldClean == "" || newClean == "" || oldClean == newClean {
		return nil
	}

	escaped := escapeLike(oldClean)
	rows, err := s.db.Query("SELECT path, name, dir FROM photo_metadata WHERE path = ? OR path LIKE ? ESCAPE '\\'", oldClean, escaped+"/%")
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
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()

	// 1. 迁移 photo_metadata 路径
	stmt, err := tx.Prepare("UPDATE photo_metadata SET path = ?, name = ?, dir = ?, updated_at = ? WHERE path = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, m := range moves {
		_, _ = tx.Exec("DELETE FROM photo_metadata WHERE path = ?", m.newP)
		if _, err := stmt.Exec(m.newP, m.newN, m.newD, now, m.oldP); err != nil {
			log.Printf("[DB] MoveDir update error for %s: %v", m.oldP, err)
		}
	}

	// 2. 迁移 photo_groups.album_dir（使用 ESCAPE 安全的 LIKE）
	if _, err := tx.Exec(`UPDATE photo_groups SET album_dir = ?, updated_at = ? WHERE album_dir = ?`,
		newClean, now, oldClean); err != nil {
		log.Printf("[DB] MoveDir update groups exact %s: %v", oldClean, err)
	}
	// 子目录 album_dir 前缀替换
	gRows, err := tx.Query(`SELECT id, album_dir FROM photo_groups WHERE album_dir LIKE ? ESCAPE '\'`, escaped+"/%")
	if err == nil {
		type gm struct {
			id  string
			dir string
		}
		var gMoves []gm
		for gRows.Next() {
			var gid, adir string
			if err := gRows.Scan(&gid, &adir); err != nil {
				continue
			}
			gMoves = append(gMoves, gm{id: gid, dir: newClean + strings.TrimPrefix(adir, oldClean)})
		}
		gRows.Close()
		for _, g := range gMoves {
			if _, err := tx.Exec(`UPDATE photo_groups SET album_dir = ?, updated_at = ? WHERE id = ?`, g.dir, now, g.id); err != nil {
				log.Printf("[DB] MoveDir update group %s album_dir: %v", g.id, err)
			}
		}
	}

	// 3. 迁移 photo_group_items.photo_path
	iRows, err := tx.Query(`SELECT group_id, photo_path FROM photo_group_items WHERE photo_path = ? OR photo_path LIKE ? ESCAPE '\'`, oldClean, escaped+"/%")
	if err == nil {
		type im struct {
			gid  string
			oldP string
			newP string
		}
		var iMoves []im
		for iRows.Next() {
			var gid, pp string
			if err := iRows.Scan(&gid, &pp); err != nil {
				continue
			}
			iMoves = append(iMoves, im{gid: gid, oldP: pp, newP: newClean + strings.TrimPrefix(pp, oldClean)})
		}
		iRows.Close()
		for _, m := range iMoves {
			_, _ = tx.Exec(`DELETE FROM photo_group_items WHERE group_id = ? AND photo_path = ?`, m.gid, m.newP)
			if _, err := tx.Exec(`UPDATE photo_group_items SET photo_path = ? WHERE group_id = ? AND photo_path = ?`, m.newP, m.gid, m.oldP); err != nil {
				log.Printf("[DB] MoveDir update group_item %s/%s: %v", m.gid, m.oldP, err)
			}
		}
	}

	return tx.Commit()
}

func (s *Store) GetTimeline(limit, offset int, dir string, sortBy string) ([]PhotoMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	orderClause := "ORDER BY taken_at DESC, path ASC"
	if sortBy == "modified" {
		orderClause = "ORDER BY modified DESC, updated_at DESC, path ASC"
	}

	var rows *sql.Rows
	var err error

	cleanDir := strings.Trim(dir, "/")
	if cleanDir == "" {
		rows, err = s.db.Query(`
			SELECT path, name, dir, size, modified, taken_at, width, height, has_thumb, thumb_id, voice_id, voice_duration_ms, memo, lat, lng, location_name, updated_at
			FROM photo_metadata
			`+orderClause+`
			LIMIT ? OFFSET ?
		`, limit, offset)
	} else {
		escaped := escapeLike(cleanDir)
		rows, err = s.db.Query(`
			SELECT path, name, dir, size, modified, taken_at, width, height, has_thumb, thumb_id, voice_id, voice_duration_ms, memo, lat, lng, location_name, updated_at
			FROM photo_metadata
			WHERE dir = ? OR dir LIKE ? ESCAPE '\'
			`+orderClause+`
			LIMIT ? OFFSET ?
		`, cleanDir, escaped+"/%", limit, offset)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]PhotoMetadata, 0) // 保证 JSON 序列化为 [] 而非 null
	for rows.Next() {
		var m PhotoMetadata
		var hasThumbInt int
		if err := rows.Scan(&m.Path, &m.Name, &m.Dir, &m.Size, &m.Modified, &m.TakenAt, &m.Width, &m.Height, &hasThumbInt, &m.ThumbID, &m.VoiceID, &m.VoiceDurationMs, &m.Memo, &m.Lat, &m.Lng, &m.LocationName, &m.UpdatedAt); err != nil {
			continue
		}
		m.HasThumb = hasThumbInt == 1
		list = append(list, m)
	}
	if err := rows.Err(); err != nil {
		return list, err
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
	videoCond := `lower(path) LIKE '%.mp4' OR lower(path) LIKE '%.mov' OR lower(path) LIKE '%.mkv' OR ` +
		`lower(path) LIKE '%.avi' OR lower(path) LIKE '%.webm' OR lower(path) LIKE '%.m4v' OR lower(path) LIKE '%.3gp' OR ` +
		`lower(path) LIKE '%.ts' OR lower(path) LIKE '%.flv' OR lower(path) LIKE '%.rmvb' OR lower(path) LIKE '%.wmv'`
	query := `SELECT
		COALESCE(SUM(CASE WHEN ` + videoCond + ` THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN NOT (` + videoCond + `) THEN 1 ELSE 0 END), 0),
		COALESCE(COUNT(DISTINCT CASE WHEN dir <> '' THEN dir END), 0),
		COALESCE(SUM(CASE WHEN voice_id <> '' OR memo <> '' THEN 1 ELSE 0 END), 0)
		FROM photo_metadata`
	if err := s.db.QueryRow(query).Scan(&st.Videos, &st.Photos, &st.Albums, &st.Memories); err != nil {
		return st, err
	}
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
		escaped := escapeLike(cleanDir)
		err = s.db.QueryRow(`SELECT COUNT(*) FROM photo_metadata WHERE dir = ? OR dir LIKE ? ESCAPE '\'`, cleanDir, escaped+"/%").Scan(&total)
	}
	return total, err
}

func (s *Store) SyncDir(dir string, live []PhotoMetadata, purgeAllowed bool) (int, int, error) {
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
		escaped := escapeLike(cleanDir)
		rows, err = tx.Query(`SELECT path FROM photo_metadata WHERE dir = ? OR dir LIKE ? ESCAPE '\'`, cleanDir, escaped+"/%")
	}
	if err != nil {
		return 0, 0, err
	}
	toDelete := []string{}
	if purgeAllowed {
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err == nil && !liveMap[p] {
				toDelete = append(toDelete, p)
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	if purgeAllowed && len(toDelete) > 0 {
		delMeta, _ := tx.Prepare(`DELETE FROM photo_metadata WHERE path = ?`)
		delItem, _ := tx.Prepare(`DELETE FROM photo_group_items WHERE photo_path = ?`)
		if delMeta != nil {
			defer delMeta.Close()
		}
		if delItem != nil {
			defer delItem.Close()
		}
		for _, p := range toDelete {
			if delItem != nil {
				_, _ = delItem.Exec(p)
			}
			if delMeta != nil {
				_, _ = delMeta.Exec(p)
			}
		}
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

	list := make([]PhotoMetadata, 0)
	for rows.Next() {
		var m PhotoMetadata
		var hasThumbInt int
		if err := rows.Scan(&m.Path, &m.Name, &m.Dir, &m.Size, &m.Modified, &m.TakenAt, &m.Width, &m.Height, &hasThumbInt, &m.ThumbID, &m.VoiceID, &m.VoiceDurationMs, &m.Memo, &m.Lat, &m.Lng, &m.LocationName, &m.UpdatedAt); err != nil {
			continue
		}
		m.HasThumb = hasThumbInt == 1
		list = append(list, m)
	}
	if err := rows.Err(); err != nil {
		return list, err
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
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(toDelete) > 0 {
		tx, err := s.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		delMeta, err := tx.Prepare("DELETE FROM photo_metadata WHERE path = ?")
		if err != nil {
			return nil, err
		}
		defer delMeta.Close()
		delItem, err := tx.Prepare("DELETE FROM photo_group_items WHERE photo_path = ?")
		if err != nil {
			return nil, err
		}
		defer delItem.Close()
		for _, p := range toDelete {
			_, _ = delItem.Exec(p)
			_, _ = delMeta.Exec(p)
		}
		if err := tx.Commit(); err != nil {
			return nil, err
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
	var groupIDs []string
	for rows.Next() {
		var g PhotoGroup
		if err := rows.Scan(&g.ID, &g.AlbumDir, &g.Title, &g.Memo, &g.CreatedAt, &g.UpdatedAt); err != nil {
			continue
		}
		g.Items = []string{}
		idxByID[g.ID] = len(groups)
		groupIDs = append(groupIDs, g.ID)
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return groups, err
	}

	if len(groups) == 0 {
		return groups, nil
	}

	// 仅查询当前结果集中的 group_items，避免全表扫描
	placeholders := make([]string, len(groupIDs))
	args := make([]any, len(groupIDs))
	for i, id := range groupIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	itemQuery := `SELECT group_id, photo_path FROM photo_group_items WHERE group_id IN (` + strings.Join(placeholders, ",") + `) ORDER BY sort_order ASC, added_at ASC`
	itemRows, err := s.db.Query(itemQuery, args...)
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
	_ = itemRows.Err()
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
		g.ID = fmt.Sprintf("grp_%d_%d", now, groupIDSeq.Add(1))
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
	seen := make(map[string]bool, len(g.Items))
	for i, rawPath := range g.Items {
		p := strings.Trim(rawPath, "/")
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
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

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM photo_group_items WHERE group_id = ? AND photo_path = ?`, groupID, clean); err != nil {
		return nil, err
	}
	_, _ = tx.Exec(`UPDATE photo_groups SET updated_at = ? WHERE id = ?`, now, groupID)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getGroupByIDLocked(groupID)
}

func (s *Store) DeleteGroup(groupID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM photo_groups WHERE id = ?`, groupID)
	return err
}

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

	// 启用 WAL 模式提高读写并发性能
	_, _ = db.Exec("PRAGMA journal_mode=WAL;")
	_, _ = db.Exec("PRAGMA synchronous=NORMAL;")

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
		updated_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_photo_dir ON photo_metadata(dir);
	CREATE INDEX IF NOT EXISTS idx_photo_taken ON photo_metadata(taken_at);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("create schema failed: %w", err)
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Upsert(meta *PhotoMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
	INSERT INTO photo_metadata (path, name, dir, size, modified, taken_at, width, height, has_thumb, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(path) DO UPDATE SET
		name=excluded.name,
		dir=excluded.dir,
		size=excluded.size,
		modified=excluded.modified,
		taken_at=excluded.taken_at,
		width=excluded.width,
		height=excluded.height,
		has_thumb=excluded.has_thumb,
		updated_at=excluded.updated_at;
	`
	hasThumbInt := 0
	if meta.HasThumb {
		hasThumbInt = 1
	}
	updatedAt := meta.UpdatedAt
	if updatedAt == 0 {
		updatedAt = time.Now().UnixMilli()
	}

	_, err := s.db.Exec(query,
		strings.Trim(meta.Path, "/"),
		meta.Name,
		strings.Trim(meta.Dir, "/"),
		meta.Size,
		meta.Modified,
		meta.TakenAt,
		meta.Width,
		meta.Height,
		hasThumbInt,
		updatedAt,
	)
	return err
}

func (s *Store) Delete(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	clean := strings.Trim(path, "/")
	_, err := s.db.Exec("DELETE FROM photo_metadata WHERE path = ?", clean)
	return err
}

func (s *Store) DeleteDir(dirPrefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	clean := strings.Trim(dirPrefix, "/")
	if clean == "" {
		_, err := s.db.Exec("DELETE FROM photo_metadata")
		return err
	}
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
			SELECT path, name, dir, size, modified, taken_at, width, height, has_thumb, updated_at
			FROM photo_metadata
			ORDER BY taken_at DESC
			LIMIT ? OFFSET ?
		`, limit, offset)
	} else {
		rows, err = s.db.Query(`
			SELECT path, name, dir, size, modified, taken_at, width, height, has_thumb, updated_at
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
		if err := rows.Scan(&m.Path, &m.Name, &m.Dir, &m.Size, &m.Modified, &m.TakenAt, &m.Width, &m.Height, &hasThumbInt, &m.UpdatedAt); err != nil {
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

	for _, p := range toDelete {
		_, _ = s.db.Exec("DELETE FROM photo_metadata WHERE path = ?", p)
	}
	return toDelete, nil
}

package api

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/anansite/nasdrop-server/internal/db"
	"github.com/anansite/nasdrop-server/internal/thumb"
)

const maxJSONBodyBytes = 4 << 20 // 4MB 请求体上限，防止低内存路由容器 OOM

type Router struct {
	store     *db.Store
	dataDir   string
	thumbDir  string
	startTime time.Time
}

func NewRouter(store *db.Store, dataDir, thumbDir string) *Router {
	return &Router{store: store, dataDir: dataDir, thumbDir: thumbDir, startTime: time.Now()}
}

func (rt *Router) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/ping", rt.handlePing)
	mux.HandleFunc("/api/system", rt.handleSystem)
	mux.HandleFunc("/api/metadata", rt.handleMetadata)
	mux.HandleFunc("/api/timeline", rt.handleTimeline)
	mux.HandleFunc("/api/sync-dir", rt.handleSyncDir)
	mux.HandleFunc("/api/stats", rt.handleStats)
	mux.HandleFunc("/api/memories", rt.handleMemories)
	mux.HandleFunc("/api/reconcile", rt.handleReconcile)
	mux.HandleFunc("/api/thumbnail", rt.handleThumbnail)
	mux.HandleFunc("/api/file-hash", rt.handleFileHash)
	mux.HandleFunc("/api/groups", rt.handleGroups)
	mux.HandleFunc("/api/groups/update", rt.handleGroupsUpdate)
	mux.HandleFunc("/api/groups/append", rt.handleGroupsAppend)
	mux.HandleFunc("/api/groups/remove-item", rt.handleGroupsRemoveItem)
	mux.HandleFunc("/api/groups/delete", rt.handleGroupsDelete)
}

func (rt *Router) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "Missing path query parameter", http.StatusBadRequest)
		return
	}
	cleanPath := filepath.Clean(filepath.FromSlash(strings.Trim(path, "/")))
	if cleanPath == "." || cleanPath == "" || strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		http.Error(w, "Invalid path: path traversal detected", http.StatusBadRequest)
		return
	}
	absData, err := filepath.Abs(rt.dataDir)
	if err != nil {
		http.Error(w, "Server data dir error", http.StatusInternalServerError)
		return
	}

	// 1. 优先读取 NAS 根目录公共缩略图库 .nasdrop_thumbs/<thumb_id>.jpg（彻底禁止服务端解码补齐）
	if thumbID := rt.store.GetThumbID(filepath.ToSlash(cleanPath)); thumbID != "" {
		safeID := filepath.Base(thumbID)
		sharedThumb := filepath.Join(absData, ".nasdrop_thumbs", safeID+".jpg")
		if info, err := os.Stat(sharedThumb); err == nil && !info.IsDir() && info.Size() > 0 {
			thumb.ServeThumbnail(w, r, sharedThumb)
			return
		}
	}

	// 2. 兼容存量旧目录 .thumbnails/<filename>（仅直接读取磁盘文件，禁止服务端解码）
	parent := filepath.Dir(cleanPath)
	filename := filepath.Base(cleanPath)
	var legacyThumb string
	if parent == "." || parent == "" {
		legacyThumb = filepath.Join(absData, ".thumbnails", filename)
	} else {
		legacyThumb = filepath.Join(absData, parent, ".thumbnails", filename)
	}
	if info, err := os.Stat(legacyThumb); err == nil && !info.IsDir() && info.Size() > 0 {
		thumb.ServeThumbnail(w, r, legacyThumb)
		return
	}

	http.Error(w, "Thumbnail not uploaded", http.StatusNotFound)
}

func (rt *Router) handleFileHash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "Missing path query parameter", http.StatusBadRequest)
		return
	}
	cleanPath := filepath.Clean(filepath.FromSlash(strings.Trim(path, "/")))
	if cleanPath == "." || cleanPath == "" || strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		http.Error(w, "Invalid path: path traversal detected", http.StatusBadRequest)
		return
	}
	absData, err := filepath.Abs(rt.dataDir)
	if err != nil {
		http.Error(w, "Server data dir error", http.StatusInternalServerError)
		return
	}
	fullPath := filepath.Join(absData, cleanPath)
	if !strings.HasPrefix(fullPath, absData+string(filepath.Separator)) {
		http.Error(w, "Invalid path: outside data directory", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	f, err := os.Open(fullPath)
	if err != nil {
		http.Error(w, "Open file failed", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		http.Error(w, "Hash file failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"path":   filepath.ToSlash(cleanPath),
		"size":   info.Size(),
		"sha256": hex.EncodeToString(h.Sum(nil)),
	})
}

func (rt *Router) handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"service": "nasdrop-server",
		"version": "1.0.0",
	})
}

// 接收手机端上报的照片元数据（拍摄时间、宽高、伴生缩略图状态等）
// 服务端本身不解析图片，全量持久化到 SQLite 数据库中
func (rt *Router) handleMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)

	var metas []db.PhotoMetadata
	// 支持单项或批量数组
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(raw) == 0 {
		http.Error(w, "Empty body", http.StatusBadRequest)
		return
	}
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &metas); err != nil {
			http.Error(w, "Invalid array: "+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		var single db.PhotoMetadata
		if err := json.Unmarshal(raw, &single); err != nil {
			http.Error(w, "Invalid object: "+err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(single.Path) == "" {
			http.Error(w, "Missing required field: path", http.StatusBadRequest)
			return
		}
		metas = append(metas, single)
	}
	// 过滤掉空路径条目
	filtered := metas[:0]
	for _, m := range metas {
		if strings.TrimSpace(m.Path) != "" {
			filtered = append(filtered, m)
		}
	}
	metas = filtered
	if len(metas) == 0 {
		http.Error(w, "No valid metadata items", http.StatusBadRequest)
		return
	}

	if err := rt.store.UpsertBatch(metas); err != nil {
		http.Error(w, "Failed to upsert batch: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"saved":  len(metas),
	})
}

// 获取全局按真实拍摄时间（taken_at DESC）排好序的时间轴列表
func (rt *Router) handleTimeline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := 100
	offset := 0
	dir := r.URL.Query().Get("dir")
	sortBy := r.URL.Query().Get("sort")

	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
			if l > 500 {
				limit = 500
			} else {
				limit = l
			}
		}
	}
	if oStr := r.URL.Query().Get("offset"); oStr != "" {
		if o, err := strconv.Atoi(oStr); err == nil && o >= 0 {
			offset = o
		}
	}

	list, err := rt.store.GetTimeline(limit, offset, dir, sortBy)
	if err != nil {
		http.Error(w, "Query timeline failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	total, _ := rt.store.GetTimelineCount(dir)
	hasMore := offset+len(list) < total

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "ok",
		"count":    len(list),
		"total":    total,
		"has_more": hasMore,
		"items":    list,
	})
}

func (rt *Router) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	st, err := rt.store.GetStats()
	if err != nil {
		http.Error(w, "Query stats failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "ok",
		"photos":   st.Photos,
		"videos":   st.Videos,
		"albums":   st.Albums,
		"memories": st.Memories,
	})
}

func (rt *Router) handleSyncDir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dir := strings.Trim(r.URL.Query().Get("dir"), "/")
	scanRoot := rt.dataDir
	if dir != "" {
		// 防止路径穿越和扫描内部隐藏目录
		cleanDir := filepath.Clean(filepath.FromSlash(dir))
		if cleanDir == "." || cleanDir == "" || strings.HasPrefix(cleanDir, "..") || filepath.IsAbs(cleanDir) || strings.HasPrefix(filepath.Base(cleanDir), ".") {
			http.Error(w, "Invalid dir: path traversal or hidden directory detected", http.StatusBadRequest)
			return
		}
		// 检查每段路径是否包含隐藏目录
		for _, seg := range strings.Split(cleanDir, string(filepath.Separator)) {
			if strings.HasPrefix(seg, ".") {
				http.Error(w, "Invalid dir: hidden directory not allowed", http.StatusBadRequest)
				return
			}
		}
		absData, err := filepath.Abs(rt.dataDir)
		if err != nil {
			http.Error(w, "Server data dir error", http.StatusInternalServerError)
			return
		}
		scanRoot = filepath.Join(absData, cleanDir)
		if !strings.HasPrefix(scanRoot, absData+string(filepath.Separator)) && scanRoot != absData {
			http.Error(w, "Invalid dir: outside data directory", http.StatusBadRequest)
			return
		}
		dir = filepath.ToSlash(cleanDir) // 规范化传给 DB 的 dir
	}

	mediaExts := map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true, ".heic": true, ".heif": true, ".avif": true, ".svg": true,
		".mp4": true, ".mkv": true, ".mov": true, ".webm": true, ".avi": true, ".m4v": true, ".3gp": true,
		".ts": true, ".flv": true, ".rmvb": true, ".wmv": true,
	}

	var live []db.PhotoMetadata
	// 扫描不完整（有目录/文件读失败）时不能 purge：那些文件只是没被看到，不是被删了，
	// 照常删除会整行 DELETE（thumb_id 一起丢），表现为缩略图「无故消失」。
	incomplete := false
	_ = filepath.WalkDir(scanRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			incomplete = true
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != scanRoot && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !mediaExts[ext] {
			return nil
		}
		rel, err := filepath.Rel(rt.dataDir, p)
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		parentSlash := ""
		if idx := strings.LastIndex(relSlash, "/"); idx > 0 {
			parentSlash = relSlash[:idx]
		}
		info, err := d.Info()
		if err != nil {
			// 单个文件 stat 失败同样会让它缺失于 live，不能因此判定为已删除
			incomplete = true
			return nil
		}
		modMs := info.ModTime().UnixMilli()
		takenAt := parseTakenAtFromName(name, modMs)
		live = append(live, db.PhotoMetadata{
			Path:     relSlash,
			Name:     name,
			Dir:      parentSlash,
			Size:     info.Size(),
			Modified: modMs,
			TakenAt:  takenAt,
		})
		return nil
	})

	added, purged, err := rt.store.SyncDir(dir, live, !incomplete)
	if err != nil {
		http.Error(w, "Sync dir failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":        "ok",
		"dir":           dir,
		"total":         len(live),
		"added":         added,
		"purged":        purged,
		"purge_skipped": incomplete,
	})
}

func parseTakenAtFromName(name string, fallbackMs int64) int64 {
	// 匹配常见 YYYYMMDD_HHMMSS
	for i := 0; i+15 <= len(name); i++ {
		if name[i] >= '1' && name[i] <= '2' && name[i+8] == '_' {
			seg := name[i : i+15]
			if t, err := time.ParseInLocation("20060102_150405", seg, time.Local); err == nil {
				return t.UnixMilli()
			}
		}
	}
	// 匹配 13 位毫秒时间戳（如 wx_camera_1795... / 1795335229888.png）
	digits := 0
	start := -1
	for i := 0; i <= len(name); i++ {
		if i < len(name) && name[i] >= '0' && name[i] <= '9' {
			if digits == 0 {
				start = i
			}
			digits++
		} else {
			if digits == 13 && start >= 0 {
				if ms, err := strconv.ParseInt(name[start:start+13], 10, 64); err == nil && ms > 1000000000000 {
					return ms
				}
			}
			digits = 0
			start = -1
		}
	}
	return fallbackMs
}

// 获取“那年今天”历史同一天的照片与视频列表
func (rt *Router) handleMemories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	now := time.Now()
	monthDay := now.Format("01-02")
	currentYear := now.Format("2006")

	if d := r.URL.Query().Get("date"); d != "" && len(d) == 5 && strings.Contains(d, "-") {
		monthDay = d
	}

	limit := 300
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 && l <= 1000 {
			limit = l
		}
	}

	items, err := rt.store.GetMemories(monthDay, currentYear, limit)
	if err != nil {
		http.Error(w, "Query memories failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"date":   monthDay,
		"count":  len(items),
		"items":  items,
	})
}

// 差量对齐：客户端报告某目录下当前存在的实际路径列表，服务端清理已不存在的项
func (rt *Router) handleReconcile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)

	var req struct {
		Dir       string   `json:"dir"`
		LivePaths []string `json:"live_paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	purged, err := rt.store.Reconcile(strings.Trim(req.Dir, "/"), req.LivePaths)
	if err != nil {
		http.Error(w, "Reconcile failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"purged": purged,
	})
}

func (rt *Router) handleGroups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		album := r.URL.Query().Get("album")
		groups, err := rt.store.ListGroups(album)
		if err != nil {
			http.Error(w, "List groups failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"groups": groups,
		})
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
		var req db.PhotoGroup
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Title) == "" {
			req.Title = "照片串联组"
		}
		if err := rt.store.CreateGroup(&req); err != nil {
			http.Error(w, "Create group failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"group":  req,
		})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (rt *Router) handleGroupsUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req struct {
		ID           string   `json:"id"`
		Title        *string  `json:"title"`
		Memo         *string  `json:"memo"`
		Items        []string `json:"items"`
		ReplaceItems bool     `json:"replace_items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	updated, err := rt.store.UpdateGroup(req.ID, req.Title, req.Memo, req.Items, req.ReplaceItems)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Group not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Update group failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"group":  updated,
	})
}

func (rt *Router) handleGroupsAppend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req struct {
		GroupID string   `json:"group_id"`
		Items   []string `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GroupID == "" {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	updated, err := rt.store.AppendGroupItems(req.GroupID, req.Items)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Group not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Append items failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"group":  updated,
	})
}

func (rt *Router) handleGroupsRemoveItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req struct {
		GroupID   string `json:"group_id"`
		PhotoPath string `json:"photo_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GroupID == "" || req.PhotoPath == "" {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	updated, err := rt.store.RemoveGroupItem(req.GroupID, req.PhotoPath)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Group not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Remove item failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"group":  updated,
	})
}

func (rt *Router) handleGroupsDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	id := req.ID
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	if id == "" {
		http.Error(w, "Missing group id", http.StatusBadRequest)
		return
	}
	if err := rt.store.DeleteGroup(id); err != nil {
		http.Error(w, "Delete group failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
	})
}

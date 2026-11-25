package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/anansite/nasdrop-server/internal/db"
	"github.com/anansite/nasdrop-server/internal/thumb"
)

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
	mux.HandleFunc("/api/memories", rt.handleMemories)
	mux.HandleFunc("/api/reconcile", rt.handleReconcile)
	mux.HandleFunc("/api/thumbnail", rt.handleThumbnail)
	mux.HandleFunc("/api/groups", rt.handleGroups)
	mux.HandleFunc("/api/groups/update", rt.handleGroupsUpdate)
	mux.HandleFunc("/api/groups/append", rt.handleGroupsAppend)
	mux.HandleFunc("/api/groups/remove-item", rt.handleGroupsRemoveItem)
	mux.HandleFunc("/api/groups/delete", rt.handleGroupsDelete)
}

func (rt *Router) handleThumbnail(w http.ResponseWriter, r *http.Request) {
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
	dim := 320
	if dStr := r.URL.Query().Get("dim"); dStr != "" {
		if d, err := strconv.Atoi(dStr); err == nil && d > 0 && d <= 1024 {
			dim = d
		}
	}
	thumbFile, err := thumb.GetOrGenerate(rt.dataDir, rt.thumbDir, cleanPath, dim)
	if err != nil {
		http.Error(w, "Thumbnail not available: "+err.Error(), http.StatusNotFound)
		return
	}
	thumb.ServeThumbnail(w, r, thumbFile)
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

	var metas []db.PhotoMetadata
	// 支持单项或批量数组
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
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
		metas = append(metas, single)
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

	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 && l <= 500 {
			limit = l
		}
	}
	if oStr := r.URL.Query().Get("offset"); oStr != "" {
		if o, err := strconv.Atoi(oStr); err == nil && o >= 0 {
			offset = o
		}
	}

	list, err := rt.store.GetTimeline(limit, offset, dir)
	if err != nil {
		http.Error(w, "Query timeline failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"count":  len(list),
		"items":  list,
	})
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

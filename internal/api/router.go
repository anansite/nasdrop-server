package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/anansite/nasdrop-server/internal/db"
	"github.com/anansite/nasdrop-server/internal/thumb"
)

type Router struct {
	store    *db.Store
	dataDir  string
	thumbDir string
}

func NewRouter(store *db.Store, dataDir, thumbDir string) *Router {
	return &Router{store: store, dataDir: dataDir, thumbDir: thumbDir}
}

func (rt *Router) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/ping", rt.handlePing)
	mux.HandleFunc("/api/metadata", rt.handleMetadata)
	mux.HandleFunc("/api/timeline", rt.handleTimeline)
	mux.HandleFunc("/api/reconcile", rt.handleReconcile)
	mux.HandleFunc("/api/thumbnail", rt.handleThumbnail)
}

func (rt *Router) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "Missing path query parameter", http.StatusBadRequest)
		return
	}
	dim := 320
	if dStr := r.URL.Query().Get("dim"); dStr != "" {
		if d, err := strconv.Atoi(dStr); err == nil && d > 0 && d <= 1024 {
			dim = d
		}
	}
	thumbFile, err := thumb.GetOrGenerate(rt.dataDir, rt.thumbDir, path, dim)
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

	for i := range metas {
		if err := rt.store.Upsert(&metas[i]); err != nil {
			http.Error(w, "Failed to upsert: "+err.Error(), http.StatusInternalServerError)
			return
		}
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

package webdav

import (
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/anansite/nasdrop-server/internal/db"
	"github.com/anansite/nasdrop-server/internal/thumb"
	"golang.org/x/net/webdav"
)

type HookedHandler struct {
	dav      *webdav.Handler
	store    *db.Store
	thumbDir string
}

func NewHookedHandler(prefix string, rootDir string, thumbDir string, store *db.Store) *HookedHandler {
	return &HookedHandler{
		dav: &webdav.Handler{
			Prefix:     prefix,
			FileSystem: webdav.Dir(rootDir),
			LockSystem: webdav.NewMemLS(),
		},
		store:    store,
		thumbDir: thumbDir,
	}
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rec *statusRecorder) WriteHeader(code int) {
	rec.statusCode = code
	rec.ResponseWriter.WriteHeader(code)
}

// Flush 保留 http.Flusher 接口兼容，避免中间件丢失流式能力
func (rec *statusRecorder) Flush() {
	if f, ok := rec.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// cleanReqPath 对 WebDAV 请求路径做规范化清理（path.Clean + 去首尾斜线 + 解码）
func cleanReqPath(prefix, rawPath string) string {
	p := strings.TrimPrefix(rawPath, prefix)
	p = path.Clean("/" + p) // 规范化: 合并 // 和 .
	p = strings.Trim(p, "/")
	return p
}

// isHiddenDir 判断是否为内部保留目录，不应触发 DB 操作
func isHiddenDir(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

func (h *HookedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.Method
	reqPath := cleanReqPath(h.dav.Prefix, r.URL.Path)

	rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
	h.dav.ServeHTTP(rec, r)

	// 仅当 WebDAV 操作完全成功时联动 DB（排除 207 Multi-Status 部分失败）
	// 且路径非空（防止误删全库）且非隐藏内部目录
	if rec.statusCode >= 200 && rec.statusCode < 300 && rec.statusCode != 207 && reqPath != "" && !isHiddenDir(reqPath) {
		switch method {
		case "DELETE":
			log.Printf("[WebDAV Hook] DELETE: %s", reqPath)
			_ = h.store.Delete(reqPath)
			_ = h.store.DeleteDir(reqPath)
			thumb.DeleteCache(h.thumbDir, reqPath)

		case "PUT":
			// 覆盖上传或新上传时，清除旧缓存避免陈旧显示
			thumb.DeleteCache(h.thumbDir, reqPath)

		case "MOVE":
			destHeader := r.Header.Get("Destination")
			if destHeader != "" {
				destURL, err := url.Parse(destHeader)
				if err == nil {
					destPath := cleanReqPath(h.dav.Prefix, destURL.Path)
					if destPath != "" && !isHiddenDir(destPath) {
						log.Printf("[WebDAV Hook] MOVE: %s -> %s", reqPath, destPath)
						_ = h.store.Move(reqPath, destPath)
						_ = h.store.MoveDir(reqPath, destPath)
						thumb.DeleteCache(h.thumbDir, reqPath)
						thumb.DeleteCache(h.thumbDir, destPath)
					}
				}
			}
		}
	}
}

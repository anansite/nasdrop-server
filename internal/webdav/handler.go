package webdav

import (
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/anansite/nasdrop-server/internal/db"
	"golang.org/x/net/webdav"
)

type HookedHandler struct {
	dav   *webdav.Handler
	store *db.Store
}

func NewHookedHandler(prefix string, rootDir string, store *db.Store) *HookedHandler {
	return &HookedHandler{
		dav: &webdav.Handler{
			Prefix:     prefix,
			FileSystem: webdav.Dir(rootDir),
			LockSystem: webdav.NewMemLS(),
		},
		store: store,
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

func (h *HookedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.Method
	reqPath := strings.TrimPrefix(r.URL.Path, h.dav.Prefix)
	reqPath = strings.Trim(reqPath, "/")

	rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
	h.dav.ServeHTTP(rec, r)

	// 当 WebDAV 操作成功时，自动联动 SQLite 数据库进行生命周期同步
	if rec.statusCode >= 200 && rec.statusCode < 300 {
		switch method {
		case "DELETE":
			log.Printf("[WebDAV Hook] DELETE: %s", reqPath)
			_ = h.store.Delete(reqPath)
			_ = h.store.DeleteDir(reqPath)

		case "MOVE":
			destHeader := r.Header.Get("Destination")
			if destHeader != "" {
				destURL, err := url.Parse(destHeader)
				if err == nil {
					destPath := strings.TrimPrefix(destURL.Path, h.dav.Prefix)
					destPath = strings.Trim(destPath, "/")
					log.Printf("[WebDAV Hook] MOVE: %s -> %s", reqPath, destPath)
					_ = h.store.Move(reqPath, destPath)
					_ = h.store.MoveDir(reqPath, destPath)
				}
			}
		}
	}
}

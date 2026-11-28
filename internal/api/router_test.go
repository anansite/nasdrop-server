package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/anansite/nasdrop-server/internal/db"
)

func newTestRouterWithDir(t *testing.T) (http.Handler, string) {
	t.Helper()
	tmp := t.TempDir()
	store, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rt := NewRouter(store, tmp, filepath.Join(tmp, "thumbs"))
	mux := http.NewServeMux()
	rt.Register(mux)
	return mux, tmp
}

func newTestRouter(t *testing.T) http.Handler {
	h, _ := newTestRouterWithDir(t)
	return h
}

func TestRouterSecurityAndStatusCodes(t *testing.T) {
	h := newTestRouter(t)

	cases := []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{"GET", "/api/sync-dir?dir=../etc", "", http.StatusBadRequest},
		{"GET", "/api/sync-dir?dir=.nasdrop_thumbs", "", http.StatusBadRequest},
		{"GET", "/api/sync-dir?dir=Photos/.hidden", "", http.StatusBadRequest},
		{"POST", "/api/thumbnail?path=Photos/a.jpg", "", http.StatusMethodNotAllowed},
		{"POST", "/api/metadata", "{}", http.StatusBadRequest},
		{"POST", "/api/groups/update", `{"id":"missing","title":"x"}`, http.StatusNotFound},
		{"POST", "/api/groups/append", `{"group_id":"missing","items":["a.jpg"]}`, http.StatusNotFound},
		{"GET", "/api/groups?album=empty", "", http.StatusOK},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s %s -> status %d, want %d (body=%s)", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestSyncDirIndexesStoryAudiosWithoutInflatingPhotoStats(t *testing.T) {
	h, root := newTestRouterWithDir(t)
	storyDir := filepath.Join(root, "Photos", "故事")
	if err := os.MkdirAll(storyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"story.m4a", "song.mp3", "cover.jpg"} {
		if err := os.WriteFile(filepath.Join(storyDir, name), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	syncRec := httptest.NewRecorder()
	h.ServeHTTP(syncRec, httptest.NewRequest("POST", "/api/sync-dir", nil))
	if syncRec.Code != http.StatusOK {
		t.Fatalf("sync-dir failed: %d %s", syncRec.Code, syncRec.Body.String())
	}

	albumRec := httptest.NewRecorder()
	h.ServeHTTP(albumRec, httptest.NewRequest("GET", "/api/timeline?dir=Photos/%E6%95%85%E4%BA%8B", nil))
	var albumResp struct {
		Items []db.PhotoMetadata `json:"items"`
	}
	if err := json.Unmarshal(albumRec.Body.Bytes(), &albumResp); err != nil {
		t.Fatal(err)
	}
	if len(albumResp.Items) != 3 {
		t.Fatalf("expected 3 items in story album, got %d (%+v)", len(albumResp.Items), albumResp.Items)
	}

	rootRec := httptest.NewRecorder()
	h.ServeHTTP(rootRec, httptest.NewRequest("GET", "/api/timeline", nil))
	var rootResp struct {
		Items []db.PhotoMetadata `json:"items"`
	}
	if err := json.Unmarshal(rootRec.Body.Bytes(), &rootResp); err != nil {
		t.Fatal(err)
	}
	if len(rootResp.Items) != 1 || rootResp.Items[0].Name != "cover.jpg" {
		t.Fatalf("expected root timeline to exclude audio files, got %+v", rootResp.Items)
	}

	statsRec := httptest.NewRecorder()
	h.ServeHTTP(statsRec, httptest.NewRequest("GET", "/api/stats", nil))
	var statsResp struct {
		Photos int `json:"photos"`
		Videos int `json:"videos"`
	}
	if err := json.Unmarshal(statsRec.Body.Bytes(), &statsResp); err != nil {
		t.Fatal(err)
	}
	if statsResp.Photos != 1 || statsResp.Videos != 0 {
		t.Fatalf("expected photos=1 videos=0, got %+v", statsResp)
	}
}

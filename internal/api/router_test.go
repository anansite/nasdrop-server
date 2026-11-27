package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/anansite/nasdrop-server/internal/db"
)

func newTestRouter(t *testing.T) http.Handler {
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
	return mux
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

package db

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMoveDirMigratesGroupsAndItemsAndEscapesLike(t *testing.T) {
	s := openTestStore(t)

	err := s.UpsertBatch([]PhotoMetadata{
		{Path: "Photos/Trip_1/a.jpg", Name: "a.jpg", Dir: "Photos/Trip_1", Size: 100, Modified: 200, TakenAt: 150, ThumbID: "t1"},
		{Path: "Photos/TripA1/b.jpg", Name: "b.jpg", Dir: "Photos/TripA1", Size: 200, Modified: 300, TakenAt: 250, ThumbID: "t2"},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	grp := &PhotoGroup{
		AlbumDir: "Photos/Trip_1",
		Title:    "My Group",
		Items:    []string{"Photos/Trip_1/a.jpg", "Photos/Trip_1/a.jpg"},
	}
	if err := s.CreateGroup(grp); err != nil {
		t.Fatalf("create group: %v", err)
	}
	if len(grp.Items) != 1 {
		t.Fatalf("expected deduped items=1, got %d", len(grp.Items))
	}

	if err := s.MoveDir("Photos/Trip_1", "Photos/Renamed"); err != nil {
		t.Fatalf("MoveDir: %v", err)
	}

	// Neighbor folder TripA1 must NOT be affected by underscore wildcard
	siblingTimeline, err := s.GetTimeline(10, 0, "Photos/TripA1", "")
	if err != nil || len(siblingTimeline) != 1 || siblingTimeline[0].Path != "Photos/TripA1/b.jpg" {
		t.Fatalf("sibling corrupted: %+v, err=%v", siblingTimeline, err)
	}

	renamedTimeline, err := s.GetTimeline(10, 0, "Photos/Renamed", "")
	if err != nil || len(renamedTimeline) != 1 || renamedTimeline[0].Path != "Photos/Renamed/a.jpg" {
		t.Fatalf("renamed missing: %+v, err=%v", renamedTimeline, err)
	}

	groups, err := s.ListGroups("Photos/Renamed")
	if err != nil || len(groups) != 1 || len(groups[0].Items) != 1 || groups[0].Items[0] != "Photos/Renamed/a.jpg" {
		t.Fatalf("group migration failed: %+v, err=%v", groups, err)
	}
}

func TestUpsertPartialPreservesExistingAndTimelineSortModified(t *testing.T) {
	s := openTestStore(t)

	if err := s.UpsertBatch([]PhotoMetadata{
		{Path: "Photos/a.jpg", Name: "a.jpg", Dir: "Photos", Size: 1234, Modified: 5000, TakenAt: 1000, ThumbID: "t_a"},
		{Path: "Photos/b.jpg", Name: "b.jpg", Dir: "Photos", Size: 5678, Modified: 9000, TakenAt: 500, ThumbID: "t_b"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Partial memo update with zero size/modified/taken_at must preserve original values
	if err := s.Upsert(&PhotoMetadata{Path: "Photos/b.jpg", Memo: "nice shot"}); err != nil {
		t.Fatalf("partial upsert: %v", err)
	}

	byMod, err := s.GetTimeline(10, 0, "", "modified")
	if err != nil || len(byMod) != 2 {
		t.Fatalf("GetTimeline sort=modified failed: %v, len=%d", err, len(byMod))
	}
	if byMod[0].Path != "Photos/b.jpg" || byMod[0].Size != 5678 || byMod[0].Memo != "nice shot" {
		t.Fatalf("unexpected first item or lost fields: %+v", byMod[0])
	}
}

func TestMoveOverwriteDoesNotFailUniqueConstraint(t *testing.T) {
	s := openTestStore(t)
	_ = s.UpsertBatch([]PhotoMetadata{
		{Path: "Photos/src.jpg", Name: "src.jpg", Dir: "Photos", ThumbID: "t_src"},
		{Path: "Photos/dst.jpg", Name: "dst.jpg", Dir: "Photos", ThumbID: "t_dst"},
	})
	if err := s.Move("Photos/src.jpg", "Photos/dst.jpg"); err != nil {
		t.Fatalf("Move overwrite failed: %v", err)
	}
	if id := s.GetThumbID("Photos/dst.jpg"); id != "t_src" {
		t.Fatalf("expected thumb t_src after overwrite, got %s", id)
	}
}

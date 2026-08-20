package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testTime = time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)

func newStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := OpenStore(dir, 6)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// writeLog puts raw content into links.jsonl, for the replay cases that a
// clean shutdown can never produce.
func writeLog(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, dataDirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, linksFileName), []byte(content), dataFilePerm); err != nil {
		t.Fatal(err)
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Count(b, []byte("\n"))
}

func TestCreateAndGet(t *testing.T) {
	s := newStore(t, t.TempDir())

	l, err := s.Create("", "https://example.com", "a note", nil, testTime)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(l.Code) != 6 {
		t.Errorf("generated code %q has length %d, want 6", l.Code, len(l.Code))
	}
	got, ok := s.Get(l.Code)
	if !ok {
		t.Fatalf("Get(%q) found nothing", l.Code)
	}
	if got.URL != "https://example.com" || got.Note != "a note" {
		t.Errorf("Get returned %+v", got)
	}
	if !got.CreatedAt.Equal(testTime) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, testTime)
	}
}

func TestCreateRejectsTakenCode(t *testing.T) {
	s := newStore(t, t.TempDir())

	if _, err := s.Create("docs", "https://example.com", "", nil, testTime); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err := s.Create("docs", "https://other.example", "", nil, testTime)
	if err != ErrCodeTaken {
		t.Errorf("second Create returned %v, want ErrCodeTaken", err)
	}
}

func TestUpdatePreservesCreatedAt(t *testing.T) {
	s := newStore(t, t.TempDir())
	later := testTime.Add(48 * time.Hour)

	if _, err := s.Create("docs", "https://example.com", "old", nil, testTime); err != nil {
		t.Fatal(err)
	}
	l, err := s.Update("docs", "https://new.example", "new", nil, later)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !l.CreatedAt.Equal(testTime) {
		t.Errorf("CreatedAt = %v, want the original %v", l.CreatedAt, testTime)
	}
	if !l.UpdatedAt.Equal(later) {
		t.Errorf("UpdatedAt = %v, want %v", l.UpdatedAt, later)
	}
	if l.URL != "https://new.example" || l.Note != "new" {
		t.Errorf("Update left %+v", l)
	}
}

func TestUpdateAndDeleteRejectUnknownCode(t *testing.T) {
	s := newStore(t, t.TempDir())

	if _, err := s.Update("missing", "https://example.com", "", nil, testTime); err != ErrNotFound {
		t.Errorf("Update returned %v, want ErrNotFound", err)
	}
	if err := s.Delete("missing", testTime); err != ErrNotFound {
		t.Errorf("Delete returned %v, want ErrNotFound", err)
	}
}

func TestReplayLastRecordWins(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)

	if _, err := s.Create("docs", "https://one.example", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("docs", "https://two.example", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("docs", "https://three.example", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	s.Close()

	reopened := newStore(t, dir)
	l, ok := reopened.Get("docs")
	if !ok {
		t.Fatal("docs is missing after reopen")
	}
	if l.URL != "https://three.example" {
		t.Errorf("URL = %q, want the last record", l.URL)
	}
}

func TestReplayHonoursTombstone(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)

	if _, err := s.Create("gone", "https://example.com", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("kept", "https://example.com", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("gone", testTime); err != nil {
		t.Fatal(err)
	}
	s.Close()

	reopened := newStore(t, dir)
	if _, ok := reopened.Get("gone"); ok {
		t.Error("a deleted link came back after reopen")
	}
	if _, ok := reopened.Get("kept"); !ok {
		t.Error("a live link vanished after reopen")
	}
}

func TestReplayResurrectsAfterTombstone(t *testing.T) {
	// A code deleted and then claimed again must end up live: the create
	// record comes after the tombstone, and the last record wins.
	dir := t.TempDir()
	writeLog(t, dir, strings.Join([]string{
		`{"code":"docs","url":"https://one.example","created_at":"2026-08-21T09:00:00Z"}`,
		`{"code":"docs","url":"https://one.example","created_at":"2026-08-21T09:00:00Z","deleted":true}`,
		`{"code":"docs","url":"https://two.example","created_at":"2026-08-21T10:00:00Z"}`,
		"",
	}, "\n"))

	s := newStore(t, dir)
	l, ok := s.Get("docs")
	if !ok {
		t.Fatal("docs is missing, want the record that follows the tombstone")
	}
	if l.URL != "https://two.example" {
		t.Errorf("URL = %q, want https://two.example", l.URL)
	}
}

func TestReplayDropsTruncatedFinalLine(t *testing.T) {
	// A process killed mid-append leaves a partial last line. Startup must
	// drop it rather than refuse to run.
	dir := t.TempDir()
	writeLog(t, dir, strings.Join([]string{
		`{"code":"good","url":"https://example.com","created_at":"2026-08-21T09:00:00Z"}`,
		`{"code":"partial","url":"https://exam`,
	}, "\n"))

	s := newStore(t, dir)
	if _, ok := s.Get("good"); !ok {
		t.Error("the complete record was lost")
	}
	if _, ok := s.Get("partial"); ok {
		t.Error("the truncated record was loaded")
	}
}

func TestReplayRejectsCorruptEarlierLine(t *testing.T) {
	// Damage anywhere but the last line is corruption, not a crash. Starting
	// anyway would silently discard a link.
	dir := t.TempDir()
	writeLog(t, dir, strings.Join([]string{
		`{"code":"good","url":"https://exam`,
		`{"code":"also-good","url":"https://example.com","created_at":"2026-08-21T09:00:00Z"}`,
		"",
	}, "\n"))

	if _, err := OpenStore(dir, 6); err == nil {
		t.Fatal("OpenStore accepted a corrupt record, want an error")
	}
}

func TestReplayRejectsRecordWithoutCode(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, `{"url":"https://example.com"}`+"\n")

	if _, err := OpenStore(dir, 6); err == nil {
		t.Fatal("OpenStore accepted a record with no code, want an error")
	}
}

func TestReplaySkipsBlankLines(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "\n"+`{"code":"docs","url":"https://example.com","created_at":"2026-08-21T09:00:00Z"}`+"\n\n")

	s := newStore(t, dir)
	if _, ok := s.Get("docs"); !ok {
		t.Error("a blank line hid the record that follows it")
	}
}

func TestCompactionOnOpen(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	// 40 records for one code: 40 > 2*1+16, so opening must rewrite the file.
	for i := 0; i < 40; i++ {
		b.WriteString(`{"code":"docs","url":"https://example.com/`)
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(`","created_at":"2026-08-21T09:00:00Z"}` + "\n")
	}
	writeLog(t, dir, b.String())

	s := newStore(t, dir)
	if got := countLines(t, filepath.Join(dir, linksFileName)); got != 1 {
		t.Errorf("log has %d records after compaction, want 1", got)
	}
	if _, ok := s.Get("docs"); !ok {
		t.Error("compaction dropped the live link")
	}
	// Appending must still work against the rewritten file.
	if _, err := s.Create("second", "https://example.com", "", nil, testTime); err != nil {
		t.Fatalf("Create after compaction: %v", err)
	}
	if got := countLines(t, filepath.Join(dir, linksFileName)); got != 2 {
		t.Errorf("log has %d records after one append, want 2", got)
	}
}

func TestNoCompactionForSmallLog(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	for i := 0; i < 5; i++ {
		if _, err := s.Create("", "https://example.com", "", nil, testTime); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	newStore(t, dir)
	if got := countLines(t, filepath.Join(dir, linksFileName)); got != 5 {
		t.Errorf("log has %d records, want the original 5 left alone", got)
	}
}

func TestClicksSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	clicked := testTime.Add(90 * time.Minute)

	if _, err := s.Create("docs", "https://example.com", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	s.RecordClick("docs", clicked)
	s.RecordClick("docs", clicked)
	s.RecordClick("docs", clicked)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := newStore(t, dir)
	n, last := reopened.Clicks("docs")
	if n != 3 {
		t.Errorf("clicks = %d, want 3", n)
	}
	if !last.Equal(clicked) {
		t.Errorf("last click = %v, want %v", last, clicked)
	}
}

func TestRecordClickIgnoresUnknownCode(t *testing.T) {
	s := newStore(t, t.TempDir())
	s.RecordClick("missing", testTime)
	if n, _ := s.Clicks("missing"); n != 0 {
		t.Errorf("clicks = %d, want 0", n)
	}
}

func TestDeleteDropsClicks(t *testing.T) {
	// A code can be claimed again after deletion, so its counts must not
	// carry over to the new link.
	dir := t.TempDir()
	s := newStore(t, dir)

	if _, err := s.Create("docs", "https://example.com", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	s.RecordClick("docs", testTime)
	if err := s.Delete("docs", testTime); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("docs", "https://new.example", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Clicks("docs"); n != 0 {
		t.Errorf("clicks = %d after reclaiming the code, want 0", n)
	}
	s.Close()

	reopened := newStore(t, dir)
	if n, _ := reopened.Clicks("docs"); n != 0 {
		t.Errorf("clicks = %d after reopen, want 0", n)
	}
}

func TestFlushClicksSkipsCleanStore(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	if _, err := s.Create("docs", "https://example.com", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	if err := s.FlushClicks(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, clicksFileName)); !os.IsNotExist(err) {
		t.Error("a store with no clicks wrote clicks.json")
	}
}

func TestListIsNewestFirst(t *testing.T) {
	s := newStore(t, t.TempDir())
	for i, code := range []string{"oldest", "middle", "newest"} {
		at := testTime.Add(time.Duration(i) * time.Hour)
		if _, err := s.Create(code, "https://example.com", "", nil, at); err != nil {
			t.Fatal(err)
		}
	}
	s.RecordClick("middle", testTime)

	got := s.List()
	want := []string{"newest", "middle", "oldest"}
	if len(got) != len(want) {
		t.Fatalf("List returned %d links, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Code != want[i] {
			t.Errorf("List()[%d] = %q, want %q", i, got[i].Code, want[i])
		}
	}
	if got[1].Clicks != 1 {
		t.Errorf("List did not join click counts: middle has %d", got[1].Clicks)
	}
}

func TestClicksFileIgnoresDeletedCodes(t *testing.T) {
	// clicks.json can name a code the link log no longer holds, if the flush
	// happened before the delete was written. Loading must not recreate it.
	dir := t.TempDir()
	writeLog(t, dir, `{"code":"docs","url":"https://example.com","created_at":"2026-08-21T09:00:00Z"}`+"\n")
	if err := os.WriteFile(filepath.Join(dir, clicksFileName),
		[]byte(`{"docs":{"clicks":7,"last":1755766800},"stale":{"clicks":99}}`), dataFilePerm); err != nil {
		t.Fatal(err)
	}

	s := newStore(t, dir)
	if n, _ := s.Clicks("docs"); n != 7 {
		t.Errorf("clicks = %d, want 7", n)
	}
	if _, ok := s.Get("stale"); ok {
		t.Error("clicks.json created a link that the log does not hold")
	}
	if len(s.List()) != 1 {
		t.Errorf("List returned %d links, want 1", len(s.List()))
	}
}

func TestWriteFileAtomicLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data.json")
	if err := writeFileAtomic(target, []byte(`{"a":1}`), dataFilePerm); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "data.json" {
		t.Errorf("directory holds %v, want only data.json", entries)
	}
}

func TestConcurrentClicksAndWrites(t *testing.T) {
	// The redirect path takes a read lock while admin writes take the write
	// lock. Run both hard under -race to prove the split is sound.
	s := newStore(t, t.TempDir())
	if _, err := s.Create("hot", "https://example.com", "", nil, testTime); err != nil {
		t.Fatal(err)
	}

	const readers, hits = 8, 500
	done := make(chan struct{})
	for i := 0; i < readers; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < hits; j++ {
				if _, ok := s.Get("hot"); ok {
					s.RecordClick("hot", testTime)
				}
			}
		}()
	}
	go func() {
		defer func() { done <- struct{}{} }()
		for j := 0; j < 50; j++ {
			if _, err := s.Create("", "https://example.com", "", nil, testTime); err != nil {
				t.Errorf("Create: %v", err)
				return
			}
			s.List()
			if err := s.FlushClicks(); err != nil {
				t.Errorf("FlushClicks: %v", err)
				return
			}
		}
	}()
	for i := 0; i < readers+1; i++ {
		<-done
	}

	if n, _ := s.Clicks("hot"); n != readers*hits {
		t.Errorf("clicks = %d, want %d", n, readers*hits)
	}
}

func TestCreateRecordOmitsTheZeroUpdatedAt(t *testing.T) {
	// omitempty never skips a struct, so the tag has to be omitzero. The log
	// is the durable format: junk written today must be tolerated forever.
	dir := t.TempDir()
	s := newStore(t, dir)
	if _, err := s.Create("docs", "https://example.com", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	s.Close()

	b, err := os.ReadFile(filepath.Join(dir, linksFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "updated_at") {
		t.Errorf("a created record carries updated_at:\n%s", b)
	}
	if !strings.Contains(string(b), "created_at") {
		t.Errorf("a created record is missing created_at:\n%s", b)
	}
}

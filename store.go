package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	linksFileName  = "links.jsonl"
	clicksFileName = "clicks.json"

	dataDirPerm  = 0o700
	dataFilePerm = 0o600
)

// ErrCodeTaken is returned when a hand-picked alias is already in use.
var ErrCodeTaken = errors.New("code is already in use")

// ErrNotFound is returned when no live link has the given code.
var ErrNotFound = errors.New("no such link")

// Store holds every link in memory and persists changes to two files in dir.
//
// links.jsonl is append-only and fsynced on every write, because link edits
// are rare. clicks.json is a snapshot rewritten whole on a timer, because a
// counter is last-write-wins state rather than an event: appending counters to
// a log would be pure garbage growth.
type Store struct {
	dir     string
	codeLen int

	mu    sync.RWMutex
	links map[string]*Link
	// clicks is parallel to links rather than a field on Link so that a
	// redirect needs only a read lock and an atomic increment. Concurrent
	// redirects then never block each other and never touch the disk.
	clicks map[string]*clickCount

	logFile *os.File

	// dirty marks unflushed click counts.
	dirty atomic.Bool
}

// clickCount is the hot part of a link. last holds Unix seconds, or zero when
// the link has never been followed.
type clickCount struct {
	n    atomic.Int64
	last atomic.Int64
}

// clickRecord is one entry in clicks.json.
type clickRecord struct {
	Clicks int64 `json:"clicks"`
	Last   int64 `json:"last,omitempty"`
}

// LinkView is a Link joined with its click counters, for display.
type LinkView struct {
	Link
	Clicks    int64
	LastClick time.Time
}

// OpenStore loads the store in dir, creating the directory if needed. New
// links get codeLen random characters.
func OpenStore(dir string, codeLen int) (*Store, error) {
	if codeLen < 1 {
		codeLen = defaultCodeLen
	}
	if err := os.MkdirAll(dir, dataDirPerm); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	s := &Store{
		dir:     dir,
		codeLen: codeLen,
		links:   make(map[string]*Link),
		clicks:  make(map[string]*clickCount),
	}

	records, err := s.replay()
	if err != nil {
		return nil, err
	}
	if err := s.loadClicks(); err != nil {
		return nil, err
	}

	// Compact when the log holds far more records than live links. The
	// constant keeps a small store from rewriting itself on every restart.
	if records > 2*len(s.links)+16 {
		if err := s.compact(); err != nil {
			return nil, fmt.Errorf("compact %s: %w", linksFileName, err)
		}
		log.Printf("store: compacted %s from %d records to %d links", linksFileName, records, len(s.links))
	}

	f, err := os.OpenFile(s.path(linksFileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, dataFilePerm)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", linksFileName, err)
	}
	s.logFile = f
	return s, nil
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

// replay rebuilds the link map from links.jsonl and returns the number of
// records read.
//
// A truncated final line means the process died mid-append, so replay drops it
// and carries on. A parse failure on any earlier line is corruption, not a
// crash, and stops startup rather than silently discarding a link.
func (s *Store) replay() (int, error) {
	f, err := os.Open(s.path(linksFileName))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", linksFileName, err)
	}
	defer f.Close()

	// bufio.Reader rather than bufio.Scanner: Scanner caps a line at 64 KiB
	// and cannot distinguish a final line without a newline from a complete
	// one, which is exactly the truncation this needs to detect.
	r := bufio.NewReader(f)
	records := 0
	for lineNo := 1; ; lineNo++ {
		line, err := r.ReadString('\n')
		if errors.Is(err, io.EOF) {
			if strings.TrimSpace(line) != "" {
				log.Printf("store: %s line %d is truncated, dropping it", linksFileName, lineNo)
			}
			break
		}
		if err != nil {
			return 0, fmt.Errorf("read %s: %w", linksFileName, err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var l Link
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			return 0, fmt.Errorf("%s line %d: %w", linksFileName, lineNo, err)
		}
		if l.Code == "" {
			return 0, fmt.Errorf("%s line %d: record has no code", linksFileName, lineNo)
		}
		records++
		s.links[l.Code] = &l
	}

	// Tombstones had to stay in the map through replay so that a later create
	// record could win. Drop them now that ordering is resolved.
	for code, l := range s.links {
		if l.Deleted {
			delete(s.links, code)
		}
	}
	for code := range s.links {
		s.clicks[code] = &clickCount{}
	}
	return records, nil
}

// loadClicks overlays clicks.json onto the counters. A missing file means no
// link has ever been followed.
func (s *Store) loadClicks() error {
	b, err := os.ReadFile(s.path(clicksFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", clicksFileName, err)
	}
	var saved map[string]clickRecord
	if err := json.Unmarshal(b, &saved); err != nil {
		return fmt.Errorf("parse %s: %w", clicksFileName, err)
	}
	for code, rec := range saved {
		c, ok := s.clicks[code]
		if !ok {
			// The link was deleted after the last flush.
			continue
		}
		c.n.Store(rec.Clicks)
		c.last.Store(rec.Last)
	}
	return nil
}

// append writes one record to the log and flushes it to disk. The caller holds
// s.mu for writing.
func (s *Store) append(l *Link) error {
	b, err := json.Marshal(l)
	if err != nil {
		return fmt.Errorf("encode link %q: %w", l.Code, err)
	}
	b = append(b, '\n')
	if _, err := s.logFile.Write(b); err != nil {
		return fmt.Errorf("write %s: %w", linksFileName, err)
	}
	if err := s.logFile.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", linksFileName, err)
	}
	return nil
}

// compact rewrites links.jsonl with one record per live link.
func (s *Store) compact() error {
	var buf strings.Builder
	for _, l := range s.links {
		b, err := json.Marshal(l)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return writeFileAtomic(s.path(linksFileName), []byte(buf.String()), dataFilePerm)
}

// Get returns the live link for code. A link past its expiry is still
// returned; the caller decides what an expired link means.
func (s *Store) Get(code string) (*Link, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.links[code]
	return l, ok
}

// List returns every live link with its click counts, newest first.
func (s *Store) List() []LinkView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]LinkView, 0, len(s.links))
	for code, l := range s.links {
		v := LinkView{Link: *l}
		if c := s.clicks[code]; c != nil {
			v.Clicks = c.n.Load()
			if unix := c.last.Load(); unix > 0 {
				v.LastClick = time.Unix(unix, 0).UTC()
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].Code < out[j].Code
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// Create stores a new link. An empty code means generate one. The URL must
// already have passed validateURL.
func (s *Store) Create(code, rawURL, note string, expires *time.Time, now time.Time) (*Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if code == "" {
		var err error
		code, err = s.freeCode()
		if err != nil {
			return nil, err
		}
	} else if _, taken := s.links[code]; taken {
		return nil, ErrCodeTaken
	}

	l := &Link{
		Code:      code,
		URL:       rawURL,
		Note:      note,
		CreatedAt: now.UTC().Truncate(time.Second),
		ExpiresAt: expires,
	}
	if err := s.append(l); err != nil {
		return nil, err
	}
	s.links[code] = l
	s.clicks[code] = &clickCount{}
	return l, nil
}

// freeCode returns an unused random code. The caller holds s.mu for writing,
// so no other writer can claim the code between the check and the insert.
func (s *Store) freeCode() (string, error) {
	const attempts = 5
	for i := 0; i < attempts; i++ {
		code, err := randomCode(s.codeLen)
		if err != nil {
			return "", err
		}
		if _, taken := s.links[code]; !taken {
			return code, nil
		}
	}
	return "", fmt.Errorf("no free code after %d attempts, raise the code length", attempts)
}

// Update changes an existing link. CreatedAt is preserved.
func (s *Store) Update(code, rawURL, note string, expires *time.Time, now time.Time) (*Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old, ok := s.links[code]
	if !ok {
		return nil, ErrNotFound
	}
	l := &Link{
		Code:      code,
		URL:       rawURL,
		Note:      note,
		CreatedAt: old.CreatedAt,
		UpdatedAt: now.UTC().Truncate(time.Second),
		ExpiresAt: expires,
	}
	if err := s.append(l); err != nil {
		return nil, err
	}
	s.links[code] = l
	return l, nil
}

// Delete appends a tombstone and drops the link and its click count. The
// counts go too, because the code can be claimed again later.
func (s *Store) Delete(code string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	old, ok := s.links[code]
	if !ok {
		return ErrNotFound
	}
	tomb := &Link{
		Code:      code,
		URL:       old.URL,
		CreatedAt: old.CreatedAt,
		UpdatedAt: now.UTC().Truncate(time.Second),
		Deleted:   true,
	}
	if err := s.append(tomb); err != nil {
		return err
	}
	delete(s.links, code)
	delete(s.clicks, code)
	s.dirty.Store(true)
	return nil
}

// RecordClick counts one visit. It is called on every redirect, so it takes
// only a read lock and two atomic stores.
func (s *Store) RecordClick(code string, now time.Time) {
	s.mu.RLock()
	c := s.clicks[code]
	s.mu.RUnlock()
	if c == nil {
		return
	}
	c.n.Add(1)
	c.last.Store(now.Unix())
	s.dirty.Store(true)
}

// Clicks returns the count and last-visit time for one code.
func (s *Store) Clicks(code string) (int64, time.Time) {
	s.mu.RLock()
	c := s.clicks[code]
	s.mu.RUnlock()
	if c == nil {
		return 0, time.Time{}
	}
	unix := c.last.Load()
	if unix == 0 {
		return c.n.Load(), time.Time{}
	}
	return c.n.Load(), time.Unix(unix, 0).UTC()
}

// FlushClicks writes clicks.json if any count has changed since the last call.
func (s *Store) FlushClicks() error {
	if !s.dirty.Load() {
		return nil
	}
	// Clear first. A click landing during the write sets the flag again, so
	// the next flush picks it up rather than losing it.
	s.dirty.Store(false)

	s.mu.RLock()
	saved := make(map[string]clickRecord, len(s.clicks))
	for code, c := range s.clicks {
		n := c.n.Load()
		if n == 0 {
			continue
		}
		saved[code] = clickRecord{Clicks: n, Last: c.last.Load()}
	}
	s.mu.RUnlock()

	b, err := json.Marshal(saved)
	if err != nil {
		s.dirty.Store(true)
		return err
	}
	if err := writeFileAtomic(s.path(clicksFileName), b, dataFilePerm); err != nil {
		s.dirty.Store(true)
		return fmt.Errorf("write %s: %w", clicksFileName, err)
	}
	return nil
}

// FlushLoop writes click counts every interval until ctx is cancelled. An
// unclean kill therefore loses at most one interval of counts, which beats an
// fsync on every redirect for a number nobody bills on.
func (s *Store) FlushLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.FlushClicks(); err != nil {
				log.Printf("store: flush clicks: %v", err)
			}
		}
	}
}

// Close flushes click counts and closes the log.
func (s *Store) Close() error {
	err := s.FlushClicks()
	if cerr := s.logFile.Close(); err == nil {
		err = cerr
	}
	return err
}

// writeFileAtomic writes data through a temporary file and a rename, so a
// reader never sees a partial file and a crash never leaves one. This is what
// makes the data directory safe to rsync while the service runs.
func writeFileAtomic(name string, data []byte, perm os.FileMode) error {
	tmp := name + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(name))
	return nil
}

// syncDir flushes a directory entry so that a rename survives a power loss. It
// is best effort: some file systems reject fsync on a directory, and that is
// not a reason to fail a write that already reached the disk.
func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	f.Close()
}

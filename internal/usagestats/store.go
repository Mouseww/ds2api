package usagestats

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ds2api/internal/config"
)

// Store aggregates usage events and persists a snapshot to disk.
//
// All mutation and read paths take the internal mutex; the store is designed to
// be called directly from HTTP middleware and admin handlers.
type Store struct {
	mu sync.Mutex

	path         string
	loadErr      error
	closed       bool
	saveInterval time.Duration

	trackingSince time.Time

	totals   bucket
	models   map[string]*bucket
	surfaces map[string]*bucket
	accounts map[string]*bucket
	callers  map[string]*bucket

	// accountSeries backs the per-account usage monitoring surface: cumulative
	// totals plus the time buckets needed to render any dashboard range for a
	// single account. It also backs the per-account quota window, which is
	// aggregated from the minute/hour buckets on demand.
	accountSeries map[string]*accountSeries

	seconds map[int64]*bucket
	minutes map[int64]*bucket
	hours   map[int64]*bucket
	days    map[int64]*bucket

	lastPrune time.Time
	dirty     bool

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// snapshotFile is the on-disk representation of the store.
type snapshotFile struct {
	Version       int                       `json:"version"`
	SavedAt       int64                     `json:"saved_at"`
	TrackingSince int64                     `json:"tracking_since"`
	Totals        bucket                    `json:"totals"`
	Models        map[string]*bucket        `json:"models"`
	Surfaces      map[string]*bucket        `json:"surfaces"`
	Accounts      map[string]*bucket        `json:"accounts"`
	Callers       map[string]*bucket        `json:"callers"`
	AccountSeries map[string]*accountSeries `json:"account_series,omitempty"`
	Seconds       map[int64]*bucket         `json:"seconds"`
	Minutes       map[int64]*bucket         `json:"minutes"`
	Hours         map[int64]*bucket         `json:"hours"`
	Days          map[int64]*bucket         `json:"days"`
}

// NewStore creates a usage store. An empty path keeps everything in memory and
// skips the background flusher, which is what tests and embedded callers want.
func NewStore(path string) *Store {
	now := time.Now()
	store := &Store{
		path:          strings.TrimSpace(path),
		saveInterval:  defaultSaveInterval,
		trackingSince: now,
		models:        map[string]*bucket{},
		surfaces:      map[string]*bucket{},
		accounts:      map[string]*bucket{},
		callers:       map[string]*bucket{},
		accountSeries: map[string]*accountSeries{},
		seconds:       map[int64]*bucket{},
		minutes:       map[int64]*bucket{},
		hours:         map[int64]*bucket{},
		days:          map[int64]*bucket{},
	}
	if store.path == "" {
		return store
	}
	store.loadErr = store.load()
	if store.loadErr != nil {
		config.Logger.Warn("[usage_stats] load failed", "path", store.path, "error", store.loadErr)
	}
	store.stopCh = make(chan struct{})
	store.wg.Add(1)
	go store.flushLoop()
	return store
}

// Path reports the persistence path, or "" for an in-memory store.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Err reports a load failure that happened while opening the store.
func (s *Store) Err() error {
	if s == nil {
		return errors.New("usage stats store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

// Record folds one completed request into the store.
func (s *Store) Record(ev Event) {
	if s == nil {
		return
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	now := ev.Time

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}

	s.totals.addEvent(ev)
	bumpBreakdown(s.models, ev.Model, ev)
	bumpBreakdown(s.surfaces, ev.Surface, ev)
	bumpBreakdown(s.accounts, ev.AccountID, ev)
	bumpBreakdown(s.callers, ev.CallerID, ev)
	s.recordAccountLocked(ev, now)

	addBucket(s.seconds, now.Unix(), ev)
	addBucket(s.minutes, now.Unix()/60, ev)
	addBucket(s.hours, now.Unix()/3600, ev)
	addBucket(s.days, now.Unix()/86400, ev)

	s.dirty = true
	s.pruneIfDueLocked(now)
}

// Reset clears every counter and restarts the tracking window.
func (s *Store) Reset() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("usage stats store is closed")
	}
	s.totals = bucket{}
	s.models = map[string]*bucket{}
	s.surfaces = map[string]*bucket{}
	s.accounts = map[string]*bucket{}
	s.callers = map[string]*bucket{}
	s.accountSeries = map[string]*accountSeries{}
	s.seconds = map[int64]*bucket{}
	s.minutes = map[int64]*bucket{}
	s.hours = map[int64]*bucket{}
	s.days = map[int64]*bucket{}
	s.trackingSince = time.Now()
	s.dirty = true
	s.mu.Unlock()
	return s.Flush()
}

// Flush writes the current snapshot when there is anything new to persist.
func (s *Store) Flush() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed || s.path == "" || !s.dirty {
		s.mu.Unlock()
		return nil
	}
	payload, err := s.marshalLocked()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.dirty = false
	path := s.path
	s.mu.Unlock()

	if err := writeFileAtomic(path, payload); err != nil {
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
		return err
	}
	return nil
}

// Close stops the background flusher and writes a final snapshot.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() {
		if s.stopCh != nil {
			close(s.stopCh)
		}
	})
	s.wg.Wait()
	flushErr := s.Flush()
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return flushErr
}

// flushLoop periodically persists dirty state.
func (s *Store) flushLoop() {
	defer s.wg.Done()
	interval := s.saveInterval
	if interval <= 0 {
		interval = defaultSaveInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			if err := s.Flush(); err != nil {
				config.Logger.Warn("[usage_stats] flush failed", "path", s.path, "error", err)
			}
		}
	}
}

// marshalLocked renders the persisted snapshot. Callers must hold s.mu.
func (s *Store) marshalLocked() ([]byte, error) {
	file := snapshotFile{
		Version:       FileVersion,
		SavedAt:       time.Now().UnixMilli(),
		TrackingSince: s.trackingSince.UnixMilli(),
		Totals:        s.totals,
		Models:        s.models,
		Surfaces:      s.surfaces,
		Accounts:      s.accounts,
		Callers:       s.callers,
		AccountSeries: s.accountSeries,
		Seconds:       s.seconds,
		Minutes:       s.minutes,
		Hours:         s.hours,
		Days:          s.days,
	}
	payload, err := json.Marshal(file)
	if err != nil {
		return nil, fmt.Errorf("encode usage stats: %w", err)
	}
	return append(payload, '\n'), nil
}

// load restores a previously persisted snapshot.
func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read usage stats: %w", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var file snapshotFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return fmt.Errorf("decode usage stats: %w", err)
	}

	s.totals = file.Totals
	s.models = adoptBreakdowns(file.Models)
	s.surfaces = adoptBreakdowns(file.Surfaces)
	s.accounts = adoptBreakdowns(file.Accounts)
	s.callers = adoptBreakdowns(file.Callers)
	s.accountSeries = adoptAccountSeries(file.AccountSeries)
	s.seconds = adoptBuckets(file.Seconds)
	s.minutes = adoptBuckets(file.Minutes)
	s.hours = adoptBuckets(file.Hours)
	s.days = adoptBuckets(file.Days)

	if file.TrackingSince > 0 {
		s.trackingSince = time.UnixMilli(file.TrackingSince)
	}
	now := time.Now()
	s.pruneLocked(now)
	s.lastPrune = now
	return nil
}

// pruneLocked drops buckets that fell out of their retention window.
func (s *Store) pruneLocked(now time.Time) {
	pruneBucketMap(s.seconds, now.Add(-secondRetention).Unix())
	pruneBucketMap(s.minutes, now.Add(-minuteRetention).Unix()/60)
	pruneBucketMap(s.hours, now.Add(-hourRetention).Unix()/3600)
	pruneBucketMap(s.days, now.Add(-dayRetention).Unix()/86400)
	for _, series := range s.accountSeries {
		series.prune(now)
	}
}

// pruneIfDueLocked runs the retention sweep at most once per minute, so a hot
// request path does not walk every bucket map on each call. Callers must hold
// s.mu.
func (s *Store) pruneIfDueLocked(now time.Time) {
	if s.lastPrune.IsZero() || now.Sub(s.lastPrune) >= time.Minute {
		s.pruneLocked(now)
		s.lastPrune = now
	}
}

// addBucket folds an event into the bucket keyed by key.
func addBucket(m map[int64]*bucket, key int64, ev Event) {
	if m == nil {
		return
	}
	target, ok := m[key]
	if !ok {
		target = &bucket{}
		m[key] = target
	}
	target.addEvent(ev)
}

// bumpBreakdown folds an event into the breakdown bucket for rawKey, honouring
// the cardinality cap.
func bumpBreakdown(m map[string]*bucket, rawKey string, ev Event) {
	if m == nil {
		return
	}
	key := normalizeKey(rawKey)
	target, ok := m[key]
	if !ok {
		if len(m) >= maxBreakdownKeys {
			key = otherKey
			target, ok = m[key]
		}
		if !ok {
			target = &bucket{}
			m[key] = target
		}
	}
	target.addEvent(ev)
}

// pruneBucketMap deletes every key below minKey.
func pruneBucketMap(m map[int64]*bucket, minKey int64) {
	for key := range m {
		if key < minKey {
			delete(m, key)
		}
	}
}

// adoptBuckets copies a decoded map into a usable one, dropping nil entries.
func adoptBuckets(in map[int64]*bucket) map[int64]*bucket {
	out := make(map[int64]*bucket, len(in))
	for key, value := range in {
		if value == nil {
			continue
		}
		copied := *value
		out[key] = &copied
	}
	return out
}

// adoptBreakdowns copies a decoded breakdown map, dropping nil entries.
func adoptBreakdowns(in map[string]*bucket) map[string]*bucket {
	out := make(map[string]*bucket, len(in))
	for key, value := range in {
		if value == nil {
			continue
		}
		copied := *value
		out[key] = &copied
	}
	return out
}

// writeFileAtomic writes payload to path through a temp file and rename.
func writeFileAtomic(path string, payload []byte) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create usage stats dir: %w", err)
		}
	}
	tmpFile, err := os.CreateTemp(dir, ".usage-stats-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp usage stats: %w", err)
	}
	tmpPath := tmpFile.Name()
	discardTemp := func(primary error) error {
		if rmErr := os.Remove(tmpPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return errors.Join(primary, fmt.Errorf("remove temp usage stats: %w", rmErr))
		}
		return primary
	}
	closeWith := func(primary error) error {
		if closeErr := tmpFile.Close(); closeErr != nil {
			primary = errors.Join(primary, fmt.Errorf("close temp usage stats: %w", closeErr))
		}
		return discardTemp(primary)
	}
	if _, err := tmpFile.Write(payload); err != nil {
		return closeWith(fmt.Errorf("write temp usage stats: %w", err))
	}
	if err := tmpFile.Sync(); err != nil {
		return closeWith(fmt.Errorf("sync temp usage stats: %w", err))
	}
	if err := tmpFile.Close(); err != nil {
		return discardTemp(fmt.Errorf("close temp usage stats: %w", err))
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return discardTemp(fmt.Errorf("promote usage stats: %w", err))
	}
	return nil
}

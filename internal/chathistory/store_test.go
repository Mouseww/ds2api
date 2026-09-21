package chathistory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func blockDetailDir(t *testing.T, detailDir string) func() {
	t.Helper()
	blockedDir := detailDir + ".blocked"
	if err := os.RemoveAll(blockedDir); err != nil {
		t.Fatalf("remove blocked detail dir failed: %v", err)
	}
	if err := os.Rename(detailDir, blockedDir); err != nil {
		t.Fatalf("move detail dir aside failed: %v", err)
	}
	if err := os.RemoveAll(detailDir); err != nil {
		t.Fatalf("remove blocked detail path failed: %v", err)
	}
	if err := os.WriteFile(detailDir, []byte("blocked"), 0o644); err != nil {
		t.Fatalf("write blocked detail path failed: %v", err)
	}
	var once sync.Once
	return func() {
		t.Helper()
		once.Do(func() {
			if err := os.RemoveAll(detailDir); err != nil {
				t.Fatalf("remove blocking detail path failed: %v", err)
			}
			if err := os.Rename(blockedDir, detailDir); err != nil {
				t.Fatalf("restore detail dir failed: %v", err)
			}
		})
	}
}

func TestStoreCreatesAndPersistsEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)

	started, err := store.Start(StartParams{
		CallerID:  "caller:abc",
		AccountID: "user@example.com",
		Model:     "deepseek-v4-flash",
		Stream:    true,
		UserInput: "hello",
	})
	if err != nil {
		t.Fatalf("start entry failed: %v", err)
	}

	updated, err := store.Update(started.ID, UpdateParams{
		Status:           "success",
		ReasoningContent: "thinking",
		Content:          "answer",
		StatusCode:       200,
		ElapsedMs:        321,
		FinishReason:     "stop",
		Usage:            map[string]any{"total_tokens": 9},
		Completed:        true,
	})
	if err != nil {
		t.Fatalf("update entry failed: %v", err)
	}
	if updated.Status != "success" || updated.Content != "answer" {
		t.Fatalf("unexpected updated entry: %#v", updated)
	}

	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if snapshot.Limit != DefaultLimit {
		t.Fatalf("unexpected default limit: %d", snapshot.Limit)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one item, got %d", len(snapshot.Items))
	}
	if snapshot.Items[0].CompletedAt == 0 {
		t.Fatalf("expected completed_at to be populated")
	}
	if snapshot.Items[0].Preview != "answer" {
		t.Fatalf("expected summary preview=answer, got %#v", snapshot.Items[0])
	}

	reloaded := New(path)
	reloadedSnapshot, err := reloaded.Snapshot()
	if err != nil {
		t.Fatalf("reload snapshot failed: %v", err)
	}
	if len(reloadedSnapshot.Items) != 1 {
		t.Fatalf("unexpected reloaded summaries: %#v", reloadedSnapshot.Items)
	}
	full, err := reloaded.Get(started.ID)
	if err != nil {
		t.Fatalf("get detail failed: %v", err)
	}
	if full.Content != "answer" {
		t.Fatalf("expected detail content=answer, got %#v", full)
	}
}

func TestBuildPreviewPreservesUTF8MB4Characters(t *testing.T) {
	long := strings.Repeat("😀", defaultPreviewAt+1)
	preview := buildPreview(Entry{Content: long})
	if !utf8.ValidString(preview) {
		t.Fatalf("expected valid utf-8 preview, got %q", preview)
	}
	if preview != strings.Repeat("😀", defaultPreviewAt)+"..." {
		t.Fatalf("unexpected preview: %q", preview)
	}
}

func TestStoreTrimsToConfiguredLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)
	if _, err := store.SetLimit(10); err != nil {
		t.Fatalf("set limit failed: %v", err)
	}

	for i := 0; i < 12; i++ {
		entry, err := store.Start(StartParams{Model: "deepseek-v4-flash", UserInput: "msg"})
		if err != nil {
			t.Fatalf("start %d failed: %v", i, err)
		}
		if _, err := store.Update(entry.ID, UpdateParams{Status: "success", Content: "ok", Completed: true}); err != nil {
			t.Fatalf("update %d failed: %v", i, err)
		}
	}

	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 10 {
		t.Fatalf("expected 10 items, got %d", len(snapshot.Items))
	}
}

func TestStoreDeleteClearAndLimitValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)
	entry, err := store.Start(StartParams{UserInput: "hello"})
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if err := store.Delete(entry.ID); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 0 {
		t.Fatalf("expected empty items after delete, got %d", len(snapshot.Items))
	}
	if _, err := store.SetLimit(999); err == nil {
		t.Fatalf("expected invalid limit error")
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("clear failed: %v", err)
	}
}

func TestStoreDisablePreservesHistoryAndBlocksNewEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)

	entry, err := store.Start(StartParams{UserInput: "hello"})
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if _, err := store.Update(entry.ID, UpdateParams{Status: "success", Content: "world", Completed: true}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	snapshot, err := store.SetLimit(DisabledLimit)
	if err != nil {
		t.Fatalf("disable failed: %v", err)
	}
	if snapshot.Limit != DisabledLimit {
		t.Fatalf("expected disabled limit, got %d", snapshot.Limit)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected disabled mode to preserve summaries, got %d", len(snapshot.Items))
	}
	if store.Enabled() {
		t.Fatalf("expected store to report disabled")
	}
	if _, err := store.Start(StartParams{UserInput: "later"}); err != ErrDisabled {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
}

func TestStoreConcurrentUpdatesKeepSplitFilesValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			entry, err := store.Start(StartParams{
				CallerID:  "caller:test",
				Model:     "deepseek-v4-flash",
				UserInput: "hello",
			})
			if err != nil {
				t.Errorf("start failed: %v", err)
				return
			}
			_, err = store.Update(entry.ID, UpdateParams{
				Status:    "success",
				Content:   "answer",
				ElapsedMs: int64(idx),
				Completed: true,
			})
			if err != nil {
				t.Errorf("update failed: %v", err)
			}
		}(i)
	}
	wg.Wait()

	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 8 {
		t.Fatalf("expected 8 items, got %d", len(snapshot.Items))
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index failed: %v", err)
	}
	var persisted File
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("persisted index is invalid json: %v", err)
	}
	if len(persisted.Items) != 8 {
		t.Fatalf("expected persisted items=8, got %d", len(persisted.Items))
	}

	detailFiles, err := os.ReadDir(path + ".d")
	if err != nil {
		t.Fatalf("read detail dir failed: %v", err)
	}
	if len(detailFiles) != 8 {
		t.Fatalf("expected 8 detail files, got %d", len(detailFiles))
	}
}

func TestStoreAutoMigratesLegacyMonolith(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	legacy := legacyFile{
		Version: 1,
		Limit:   20,
		Items: []Entry{{
			ID:               "chat_legacy",
			CreatedAt:        1,
			UpdatedAt:        2,
			Status:           "success",
			UserInput:        "hello",
			Content:          "world",
			ReasoningContent: "thinking",
		}},
	}
	body, _ := json.MarshalIndent(legacy, "", "  ")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write legacy file failed: %v", err)
	}

	store := New(path)
	if err := store.Err(); err != nil {
		t.Fatalf("expected legacy migration success, got %v", err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one migrated summary, got %#v", snapshot.Items)
	}
	full, err := store.Get("chat_legacy")
	if err != nil {
		t.Fatalf("get migrated detail failed: %v", err)
	}
	if full.Content != "world" {
		t.Fatalf("expected migrated detail content preserved, got %#v", full)
	}
}

func TestStoreAutoMigratesMetadataOnlyLegacyMonolith(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	legacy := legacyFile{
		Version: 1,
		Limit:   20,
		Items: []Entry{{
			ID:           "chat_metadata_only",
			Revision:     0,
			CreatedAt:    1,
			UpdatedAt:    2,
			Status:       "error",
			CallerID:     "caller:test",
			AccountID:    "acct:test",
			Model:        "deepseek-v4-flash",
			Stream:       true,
			UserInput:    "hello",
			Error:        "boom",
			StatusCode:   500,
			ElapsedMs:    12,
			FinishReason: "error",
		}},
	}
	body, _ := json.MarshalIndent(legacy, "", "  ")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write legacy file failed: %v", err)
	}

	store := New(path)
	if err := store.Err(); err != nil {
		t.Fatalf("expected legacy metadata-only migration success, got %v", err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one migrated summary, got %#v", snapshot.Items)
	}
	full, err := store.Get("chat_metadata_only")
	if err != nil {
		t.Fatalf("get migrated detail failed: %v", err)
	}
	if full.Error != "boom" || full.UserInput != "hello" {
		t.Fatalf("expected metadata-only legacy fields preserved, got %#v", full)
	}
	if _, err := os.Stat(filepath.Join(store.DetailDir(), "chat_metadata_only.json")); err != nil {
		t.Fatalf("expected migrated detail file to exist: %v", err)
	}
}

func TestStoreLegacyMigrationBestEffortWhenRewriteFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	longID := "chat_" + strings.Repeat("x", 320)
	legacy := legacyFile{
		Version: 1,
		Limit:   20,
		Items: []Entry{{
			ID:        longID,
			CreatedAt: 1,
			UpdatedAt: 2,
			Status:    "success",
			UserInput: "hello",
			Content:   "world",
		}},
	}
	body, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy file failed: %v", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write legacy file failed: %v", err)
	}

	store := New(path)
	if err := store.Err(); err != nil {
		t.Fatalf("expected store to stay usable after migration writeback failure, got %v", err)
	}
	if !store.Enabled() {
		t.Fatal("expected store to remain enabled after best-effort migration")
	}

	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].ID != longID {
		t.Fatalf("unexpected snapshot after best-effort migration: %#v", snapshot.Items)
	}
	full, err := store.Get(longID)
	if err != nil {
		t.Fatalf("get migrated detail failed: %v", err)
	}
	if full.Content != "world" {
		t.Fatalf("expected migrated content to stay in memory, got %#v", full)
	}
	if _, statErr := os.Stat(filepath.Join(store.DetailDir(), longID+".json")); statErr == nil {
		t.Fatal("expected detail write to fail for overlong legacy id")
	}
}

func TestStoreTransientPersistenceFailureDoesNotLatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)

	first, err := store.Start(StartParams{UserInput: "first"})
	if err != nil {
		t.Fatalf("start first failed: %v", err)
	}
	restore := blockDetailDir(t, store.DetailDir())
	t.Cleanup(restore)

	blocked, err := store.Start(StartParams{UserInput: "blocked"})
	if err == nil {
		t.Fatalf("expected start failure while detail dir is blocked")
	}
	if blocked.ID == "" {
		t.Fatalf("expected in-memory entry from failed start")
	}
	if err := store.Err(); err != nil {
		t.Fatalf("transient start failure should not latch store error: %v", err)
	}
	if _, err := store.Update(first.ID, UpdateParams{Status: "success", Content: "one", Completed: true}); err == nil {
		t.Fatalf("expected update failure while detail dir is blocked")
	}
	if err := store.Err(); err != nil {
		t.Fatalf("transient update failure should not latch store error: %v", err)
	}

	restore()

	if _, err := store.Update(blocked.ID, UpdateParams{Status: "success", Content: "two", Completed: true}); err != nil {
		t.Fatalf("update after restore failed: %v", err)
	}
	if _, err := store.Start(StartParams{UserInput: "later"}); err != nil {
		t.Fatalf("start after restore failed: %v", err)
	}
	full, err := store.Get(blocked.ID)
	if err != nil {
		t.Fatalf("get restored entry failed: %v", err)
	}
	if full.Content != "two" || full.Status != "success" {
		t.Fatalf("expected restored entry persisted, got %#v", full)
	}
}

func TestStoreWritesOnlyChangedDetailFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)

	first, err := store.Start(StartParams{UserInput: "one"})
	if err != nil {
		t.Fatalf("start first failed: %v", err)
	}
	if _, err := store.Update(first.ID, UpdateParams{Status: "success", Content: "first", Completed: true}); err != nil {
		t.Fatalf("update first failed: %v", err)
	}
	second, err := store.Start(StartParams{UserInput: "two"})
	if err != nil {
		t.Fatalf("start second failed: %v", err)
	}
	if _, err := store.Update(second.ID, UpdateParams{Status: "success", Content: "second", Completed: true}); err != nil {
		t.Fatalf("update second failed: %v", err)
	}

	firstPath := filepath.Join(store.DetailDir(), first.ID+".json")
	secondPath := filepath.Join(store.DetailDir(), second.ID+".json")
	beforeFirst, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatalf("read first detail before update failed: %v", err)
	}
	beforeSecond, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatalf("read second detail before update failed: %v", err)
	}

	if _, err := store.Update(first.ID, UpdateParams{Status: "success", Content: "first-updated", Completed: true}); err != nil {
		t.Fatalf("update first again failed: %v", err)
	}

	afterFirst, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatalf("read first detail after update failed: %v", err)
	}
	afterSecond, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatalf("read second detail after update failed: %v", err)
	}

	if bytes.Equal(beforeFirst, afterFirst) {
		t.Fatalf("expected first detail file to change after update")
	}
	if !bytes.Equal(beforeSecond, afterSecond) {
		t.Fatalf("expected untouched detail file to remain byte-identical")
	}
}

func TestStoreOrdersByCreationTimeNotStreamingUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)

	first, err := store.Start(StartParams{UserInput: "first"})
	if err != nil {
		t.Fatalf("start first failed: %v", err)
	}
	time.Sleep(time.Millisecond)
	second, err := store.Start(StartParams{UserInput: "second"})
	if err != nil {
		t.Fatalf("start second failed: %v", err)
	}
	time.Sleep(time.Millisecond)
	if _, err := store.Update(first.ID, UpdateParams{Status: "streaming", Content: "still running"}); err != nil {
		t.Fatalf("update first failed: %v", err)
	}

	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 2 {
		t.Fatalf("expected two items, got %#v", snapshot.Items)
	}
	if snapshot.Items[0].ID != second.ID || snapshot.Items[1].ID != first.ID {
		t.Fatalf("expected creation-time order to stay stable, got %#v", snapshot.Items)
	}
}

func TestUpdatePreservesContentWhenNewContentIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)

	started, err := store.Start(StartParams{
		CallerID:  "caller:abc",
		Model:     "deepseek-v4-flash",
		Stream:    true,
		UserInput: "hello",
	})
	if err != nil {
		t.Fatalf("start entry failed: %v", err)
	}

	if _, err := store.Update(started.ID, UpdateParams{
		Status:           "streaming",
		ReasoningContent: "let me think",
		Content:          "I'll help you with that.",
	}); err != nil {
		t.Fatalf("progress update failed: %v", err)
	}

	updated, err := store.Update(started.ID, UpdateParams{
		Status:    "success",
		Content:   "",
		Completed: true,
	})
	if err != nil {
		t.Fatalf("success update failed: %v", err)
	}

	if updated.Content != "I'll help you with that." {
		t.Fatalf("expected content to be preserved, got %q", updated.Content)
	}
	if updated.ReasoningContent != "let me think" {
		t.Fatalf("expected reasoning content to be preserved, got %q", updated.ReasoningContent)
	}

	full, err := store.Get(started.ID)
	if err != nil {
		t.Fatalf("get entry failed: %v", err)
	}
	if full.Content != "I'll help you with that." {
		t.Fatalf("expected persisted content to be preserved, got %q", full.Content)
	}
	if full.ReasoningContent != "let me think" {
		t.Fatalf("expected persisted reasoning content to be preserved, got %q", full.ReasoningContent)
	}
}

func TestUpdateAllowsSettingContentFromEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)

	started, err := store.Start(StartParams{
		CallerID:  "caller:abc",
		Model:     "deepseek-v4-flash",
		Stream:    true,
		UserInput: "hello",
	})
	if err != nil {
		t.Fatalf("start entry failed: %v", err)
	}

	updated, err := store.Update(started.ID, UpdateParams{
		Status:  "success",
		Content: "final answer",
	})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if updated.Content != "final answer" {
		t.Fatalf("expected content to be set, got %q", updated.Content)
	}
}

func TestUpdateAllowsOverwritingContentWithNewValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)

	started, err := store.Start(StartParams{
		CallerID:  "caller:abc",
		Model:     "deepseek-v4-flash",
		Stream:    true,
		UserInput: "hello",
	})
	if err != nil {
		t.Fatalf("start entry failed: %v", err)
	}

	if _, err := store.Update(started.ID, UpdateParams{
		Status:  "streaming",
		Content: "partial",
	}); err != nil {
		t.Fatalf("first update failed: %v", err)
	}

	updated, err := store.Update(started.ID, UpdateParams{
		Status:  "success",
		Content: "final answer",
	})
	if err != nil {
		t.Fatalf("second update failed: %v", err)
	}
	if updated.Content != "final answer" {
		t.Fatalf("expected content to be overwritten, got %q", updated.Content)
	}
}

func readIndexFile(t *testing.T, path string) (File, []byte) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index failed: %v", err)
	}
	var persisted File
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("persisted index is invalid json: %v", err)
	}
	return persisted, raw
}

// TestStreamingProgressUpdateDoesNotRewriteIndex pins the core of the
// persistence layering: a streaming progress update flushes its own detail but
// neither rewrites nor touches the index file.
func TestStreamingProgressUpdateDoesNotRewriteIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)

	started, err := store.Start(StartParams{UserInput: "hello"})
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	_, beforeIndex := readIndexFile(t, path)
	beforeStat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat index failed: %v", err)
	}

	if _, err := store.Update(started.ID, UpdateParams{
		Status:           "streaming",
		ReasoningContent: "thinking",
		Content:          "partial",
		StatusCode:       200,
		ElapsedMs:        120,
	}); err != nil {
		t.Fatalf("progress update failed: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	_, afterIndex := readIndexFile(t, path)
	afterStat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat index after progress failed: %v", err)
	}
	if !bytes.Equal(beforeIndex, afterIndex) {
		t.Fatalf("streaming progress rewrote the index file\nbefore=%s\nafter=%s", beforeIndex, afterIndex)
	}
	if !afterStat.ModTime().Equal(beforeStat.ModTime()) {
		t.Fatalf("streaming progress touched the index file")
	}

	detail, err := os.ReadFile(filepath.Join(store.DetailDir(), started.ID+".json"))
	if err != nil {
		t.Fatalf("read progress detail failed: %v", err)
	}
	if !strings.Contains(string(detail), "partial") {
		t.Fatalf("expected progress detail to be persisted, got %s", detail)
	}

	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Preview != "partial" {
		t.Fatalf("expected in-memory summary to track the progress detail, got %#v", snapshot.Items)
	}
	if snapshot.Items[0].Status != "streaming" {
		t.Fatalf("expected streaming summary, got %#v", snapshot.Items[0])
	}
}

// TestStreamingProgressDebounceFlushesDetailWithoutIndex covers the deferred
// coalescing path: no synchronous flush is used, the detail still lands after
// the debounce window, and the index stays untouched.
func TestStreamingProgressDebounceFlushesDetailWithoutIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close store failed: %v", err)
		}
	})

	started, err := store.Start(StartParams{UserInput: "hello"})
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	_, beforeIndex := readIndexFile(t, path)

	if _, err := store.Update(started.ID, UpdateParams{Status: "streaming", Content: "partial"}); err != nil {
		t.Fatalf("progress update failed: %v", err)
	}

	detailPath := filepath.Join(store.DetailDir(), started.ID+".json")
	deadline := time.Now().Add(5 * progressFlushInterval)
	var detail []byte
	for time.Now().Before(deadline) {
		raw, readErr := os.ReadFile(detailPath)
		if readErr == nil && strings.Contains(string(raw), "partial") {
			detail = raw
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(detail) == 0 || !strings.Contains(string(detail), "partial") {
		t.Fatalf("expected debounced progress flush to persist the detail, got %s", detail)
	}

	_, afterIndex := readIndexFile(t, path)
	if !bytes.Equal(beforeIndex, afterIndex) {
		t.Fatalf("debounced progress flush rewrote the index file\nbefore=%s\nafter=%s", beforeIndex, afterIndex)
	}
}

// TestTerminalUpdateWritesIndexAndSurvivesReload proves the terminal path still
// writes both files and that a fresh Store reads the terminal data back.
func TestTerminalUpdateWritesIndexAndSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)

	started, err := store.Start(StartParams{
		CallerID:  "caller:test",
		Model:     "deepseek-v4-flash",
		Stream:    true,
		UserInput: "hello",
	})
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if _, err := store.Update(started.ID, UpdateParams{Status: "streaming", Content: "partial"}); err != nil {
		t.Fatalf("progress update failed: %v", err)
	}
	if _, err := store.Update(started.ID, UpdateParams{
		Status:       "success",
		Content:      "final answer",
		StatusCode:   200,
		FinishReason: "stop",
		Usage:        map[string]any{"total_tokens": 3},
		Completed:    true,
	}); err != nil {
		t.Fatalf("terminal update failed: %v", err)
	}

	persisted, _ := readIndexFile(t, path)
	if len(persisted.Items) != 1 {
		t.Fatalf("expected terminal update to persist one summary, got %#v", persisted.Items)
	}
	if persisted.Items[0].Status != "success" || persisted.Items[0].Preview != "final answer" {
		t.Fatalf("expected terminal summary on disk, got %#v", persisted.Items[0])
	}
	if persisted.Items[0].CompletedAt == 0 {
		t.Fatalf("expected completed_at on the persisted summary, got %#v", persisted.Items[0])
	}

	reloaded := New(path)
	reloadedSnapshot, err := reloaded.Snapshot()
	if err != nil {
		t.Fatalf("reload snapshot failed: %v", err)
	}
	if len(reloadedSnapshot.Items) != 1 || reloadedSnapshot.Items[0].Status != "success" {
		t.Fatalf("expected reloaded terminal summary, got %#v", reloadedSnapshot.Items)
	}
	full, err := reloaded.Get(started.ID)
	if err != nil {
		t.Fatalf("reload detail failed: %v", err)
	}
	if full.Status != "success" || full.Content != "final answer" || full.CompletedAt == 0 {
		t.Fatalf("expected reloaded terminal detail, got %#v", full)
	}
}

// TestErrorAndStoppedUpdatesRewriteIndex keeps the other terminal statuses on
// the synchronous index-writing path.
func TestErrorAndStoppedUpdatesRewriteIndex(t *testing.T) {
	for _, status := range []string{"error", "stopped"} {
		t.Run(status, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "chat_history.json")
			store := New(path)
			started, err := store.Start(StartParams{UserInput: "hello"})
			if err != nil {
				t.Fatalf("start failed: %v", err)
			}
			if _, err := store.Update(started.ID, UpdateParams{
				Status:  status,
				Content: "body",
			}); err != nil {
				t.Fatalf("%s update failed: %v", status, err)
			}
			persisted, _ := readIndexFile(t, path)
			if len(persisted.Items) != 1 || persisted.Items[0].Status != status {
				t.Fatalf("expected %s summary persisted, got %#v", status, persisted.Items)
			}
		})
	}
}

// TestCloseFlushesPendingProgress proves terminal safety: Close must land the
// deferred progress detail without waiting for the debounce timer.
func TestCloseFlushesPendingProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)

	started, err := store.Start(StartParams{UserInput: "hello"})
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if _, err := store.Update(started.ID, UpdateParams{Status: "streaming", Content: "partial"}); err != nil {
		t.Fatalf("progress update failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store failed: %v", err)
	}

	detail, err := os.ReadFile(filepath.Join(store.DetailDir(), started.ID+".json"))
	if err != nil {
		t.Fatalf("read detail after close failed: %v", err)
	}
	if !strings.Contains(string(detail), "partial") {
		t.Fatalf("expected Close to flush the pending progress detail, got %s", detail)
	}

	reloaded := New(path)
	full, err := reloaded.Get(started.ID)
	if err != nil {
		t.Fatalf("reload detail failed: %v", err)
	}
	if full.Content != "partial" {
		t.Fatalf("expected flushed progress content after reload, got %#v", full)
	}
}

// TestOverflowEvictionUpdatesIndexAndDetailFiles checks that retention keeps the
// index and the detail directory on disk consistent.
func TestOverflowEvictionUpdatesIndexAndDetailFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.json")
	store := New(path)
	if _, err := store.SetLimit(10); err != nil {
		t.Fatalf("set limit failed: %v", err)
	}

	ids := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		entry, err := store.Start(StartParams{UserInput: fmt.Sprintf("msg-%d", i)})
		if err != nil {
			t.Fatalf("start %d failed: %v", i, err)
		}
		ids = append(ids, entry.ID)
	}

	persisted, _ := readIndexFile(t, path)
	if len(persisted.Items) != 10 {
		t.Fatalf("expected 10 persisted summaries, got %d", len(persisted.Items))
	}
	if persisted.Items[0].ID != ids[len(ids)-1] {
		t.Fatalf("expected newest entry first, got %#v", persisted.Items[0])
	}
	for _, id := range ids[:2] {
		if _, err := store.Get(id); err == nil {
			t.Fatalf("expected evicted entry %s to be gone from memory", id)
		}
		if _, err := os.Stat(filepath.Join(store.DetailDir(), id+".json")); err == nil {
			t.Fatalf("expected evicted detail file for %s to be removed", id)
		}
	}
	detailFiles, err := os.ReadDir(store.DetailDir())
	if err != nil {
		t.Fatalf("read detail dir failed: %v", err)
	}
	if len(detailFiles) != 10 {
		t.Fatalf("expected 10 detail files after eviction, got %d", len(detailFiles))
	}

	reloaded := New(path)
	reloadedSnapshot, err := reloaded.Snapshot()
	if err != nil {
		t.Fatalf("reload snapshot failed: %v", err)
	}
	if len(reloadedSnapshot.Items) != 10 {
		t.Fatalf("expected reloaded index to stay consistent, got %d", len(reloadedSnapshot.Items))
	}
}

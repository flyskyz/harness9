package memory_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/harness9/internal/memory"
)

func TestManager_NewAndListSessions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := memory.NewManager(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	sess1, err := mgr.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sess2, err := mgr.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if sess1.SessionID() == sess2.SessionID() {
		t.Error("sessions must have unique IDs")
	}

	list, err := mgr.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(list))
	}
}

func TestManager_OpenExistingSession(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := memory.NewManager(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	sess, err := mgr.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := sess.SessionID()

	reopened, err := mgr.OpenSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.SessionID() != id {
		t.Errorf("want %q, got %q", id, reopened.SessionID())
	}
}

func TestManager_OpenNonExistentSession(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := memory.NewManager(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	_, err = mgr.OpenSession(ctx, "nonexistent-id")
	if err == nil {
		t.Error("want error for non-existent session, got nil")
	}
}

func TestManager_DeleteSession(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := memory.NewManager(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	sess, err := mgr.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := mgr.DeleteSession(ctx, sess.SessionID()); err != nil {
		t.Fatal(err)
	}

	list, err := mgr.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("want 0 sessions after delete, got %d", len(list))
	}
}

func TestManager_CreatesDirectory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nested", "dir", "test.db")

	mgr, err := memory.NewManager(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file was not created")
	}

	_, err = mgr.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
}

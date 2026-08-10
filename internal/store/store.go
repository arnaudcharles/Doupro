// Package store is the SQLite access layer: embedded schema migrations
// applied on startup, and typed query functions for containers, image
// history, schedules, events, notifications, settings, users and API
// keys. No package outside store issues raw SQL. See docs/architecture.md.
//
// Only the containers table is implemented so far, backing the first
// vertical slice (discovery -> store -> GET /api/v1/containers).
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arnaudcharles/doupro/internal/secrets"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const sqliteBusyTimeout = 5 * time.Second

// Store wraps the SQLite connection. The zero value is not usable — build
// one with Open.
type Store struct {
	db      *sql.DB
	secrets *secrets.Cipher
}

// ConfigureSecrets installs the process master key and upgrades legacy
// plaintext reversible secrets before any worker or API route starts.
func (s *Store) ConfigureSecrets(ctx context.Context, cipher *secrets.Cipher) error {
	s.secrets = cipher
	return s.migratePlaintextSecrets(ctx)
}

// Open opens (creating if needed) the SQLite database at dbPath, enables
// WAL mode for safe concurrent access from the API server and background
// workers (see docs/architecture.md), and applies any pending migrations.
func Open(ctx context.Context, dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite at %s: %w", dbPath, err)
	}
	// SQLite permits one writer at a time. DoUpRo has several legitimate
	// background writers (discovery, crash-loop monitoring and notification
	// delivery), so letting database/sql open a pool turns ordinary startup
	// concurrency into SQLITE_BUSY races. One serialized connection plus a
	// bounded busy wait keeps those writes durable instead of dropping them.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL;"); err != nil {
		db.Close() //nolint:errcheck // already failing to open; nothing to recover
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON;"); err != nil {
		db.Close() //nolint:errcheck // already failing to open; nothing to recover
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d;", sqliteBusyTimeout.Milliseconds())); err != nil {
		db.Close() //nolint:errcheck // already failing to open; nothing to recover
		return nil, fmt.Errorf("configure sqlite busy timeout: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close() //nolint:errcheck // already failing to open; nothing to recover
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close releases the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names) // filenames are numerically prefixed, e.g. 0001_init.sql

	for _, name := range names {
		script, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := s.db.ExecContext(ctx, string(script)); err != nil {
			// Migrations have no version table — every file re-runs on every
			// startup, relying on idempotent statements (CREATE TABLE/INDEX
			// IF NOT EXISTS). SQLite has no ADD COLUMN IF NOT EXISTS, so an
			// ALTER TABLE migration re-running after it already applied hits
			// this instead — treat it the same as already-applied.
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}

// ContainerRecord is a tracked container as stored in SQLite. See
// docs/containers.md for the semantics of current/previous image and
// update_available.
type ContainerRecord struct {
	ID     string
	Name   string
	Stack  string
	State  string
	Status string

	// CurrentImage/PreviousImage are the raw, as-configured references
	// (tag or image ID) — see shortImageRef in internal/web for how
	// PreviousImage's sha256 form is shortened for display.
	CurrentImage  string
	PreviousImage string

	// CurrentVersion/AvailableVersion are, in order of preference: a
	// resolved human-readable tag (e.g. "1.25.3"), or a raw digest when
	// resolution didn't find one (see resolveVersions in
	// internal/scheduler/check.go) — the digest is still the honest
	// identity of what's running, unlike echoing the floating tag (e.g.
	// "latest") back. Empty only when no digest was ever recorded at all
	// (e.g. a locally built image with no RepoDigests).
	CurrentVersion   string
	AvailableVersion string

	UpdateAvailable bool
	Excluded        bool
	LastCheckedAt   *time.Time
}

// UpsertSeen records (or refreshes) the discovery-level fields of a
// container observed on the Docker host: identity, state, and current
// image. It never touches PreviousImage or UpdateAvailable — those are
// owned by the registry/updater packages once they land, not by
// discovery.
//
// current_image is deliberately NOT always overwritten with the live
// Docker value: Rollback recreates a container pinned to an immutable
// digest (Config.Image becomes "sha256:..." or "repo@sha256:...") to guarantee exact content, by
// design — see updater.Rollback. If discovery blindly synced that digest
// into current_image, it would stick (every subsequent check keeps
// seeing the same digest) and break two things at once: the Containers
// page display (an unreadable digest instead of the compose-defined
// tag) and Update() itself (which reuses current_image as its own pull
// target — recreate() skips pulling for anything already digest-shaped,
// so Update would silently stop picking up new releases at all). A
// freshly-discovered digest only replaces an already-known, readable tag
// when there's nothing better on record yet (a brand new container row).
func (s *Store) UpsertSeen(ctx context.Context, c ContainerRecord) error {
	const q = `
INSERT INTO containers (id, name, stack, state, status, current_image, excluded, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  name          = excluded.name,
  stack         = excluded.stack,
  state         = excluded.state,
  status        = excluded.status,
  current_image = CASE
    WHEN (excluded.current_image LIKE 'sha256:%'
       OR excluded.current_image LIKE '%@%:%')
     AND containers.current_image <> ''
     AND containers.current_image NOT LIKE 'sha256:%'
     AND containers.current_image NOT LIKE '%@%:%'
    THEN containers.current_image
    ELSE excluded.current_image
  END,
  excluded      = excluded.excluded,
  updated_at    = excluded.updated_at;`

	_, err := s.db.ExecContext(ctx, q,
		c.ID, c.Name, c.Stack, c.State, c.Status, c.CurrentImage, c.Excluded,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert container %s: %w", c.ID, err)
	}
	return nil
}

// RecoverImageReference returns the most recently logged mutable image
// reference for a container. It repairs rows written by older releases
// that allowed a rollback's raw/canonical digest to overwrite the original
// tag. Containers with no update/rollback history legitimately have
// nothing to recover and return ErrNotFound.
func (s *Store) RecoverImageReference(ctx context.Context, name string) (string, error) {
	const q = `
WITH candidates AS (
  SELECT id, timestamp, 0 AS position, from_version AS image
  FROM events WHERE container = ?
  UNION ALL
  SELECT id, timestamp, 1 AS position, to_version AS image
  FROM events WHERE container = ?
)
SELECT image
FROM candidates
WHERE image <> ''
  AND image NOT LIKE 'sha256:%'
  AND image NOT LIKE '%@%:%'
ORDER BY timestamp DESC, id DESC, position ASC
LIMIT 1;`

	var image string
	err := s.db.QueryRowContext(ctx, q, name, name).Scan(&image)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("recover image reference for %s: %w", name, err)
	}
	return image, nil
}

// RepairCurrentImage replaces a legacy digest-shaped current_image only.
// The WHERE guard prevents a delayed recovery from overwriting a valid tag
// written concurrently by Update or Rollback.
func (s *Store) RepairCurrentImage(ctx context.Context, id, image string) error {
	const q = `
UPDATE containers
SET current_image = ?, updated_at = ?
WHERE id = ?
  AND (current_image LIKE 'sha256:%' OR current_image LIKE '%@%:%');`
	if _, err := s.db.ExecContext(ctx, q, image, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return fmt.Errorf("repair current image for %s: %w", id, err)
	}
	return nil
}

// PruneContainers deletes every tracked container row whose ID isn't in
// liveIDs — i.e. Docker no longer knows about it at all (removed, not
// merely stopped; stopped containers are still tracked and stay in
// liveIDs). Discovery calls this once per check, right after upserting
// every currently visible container.
//
// Without this, a container recreated outside DoUpRo's own update path
// (docker-compose recreate, a manual `docker rm`+`run`, anything that
// changes the container's ID while keeping its name) leaves its old row
// behind forever — UpsertSeen only ever inserts/updates by ID, never
// deletes. That stale row then shadows the real one: GetContainerByName's
// `SELECT id FROM containers WHERE name = ?` has no ORDER BY, so with two
// rows sharing a name, SQLite can return either one, including the dead
// one — found via a schedule that silently kept reading a stale
// `update_available` from the wrong row and never fired.
func (s *Store) PruneContainers(ctx context.Context, liveIDs []string) error {
	if len(liveIDs) == 0 {
		// Never delete every tracked container just because discovery
		// returned none (e.g. Docker briefly unreachable) — Check() bails
		// out before ever reaching here in that case, but stay defensive.
		return nil
	}
	placeholders := make([]string, len(liveIDs))
	args := make([]any, len(liveIDs))
	for i, id := range liveIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	q := fmt.Sprintf("DELETE FROM containers WHERE id NOT IN (%s);", strings.Join(placeholders, ","))
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("prune stale containers: %w", err)
	}
	return nil
}

// ApplyExclusions recomputes and persists the excluded flag for every
// already-known container from ex, so a Settings -> Exclusions change
// takes effect immediately instead of waiting for the next periodic
// check (or a restart) to reach it via UpsertSeen. A container name or
// literal sentinel that can never match a real Docker name/stack is used
// in place of an empty IN (...) list, which SQLite rejects.
func (s *Store) ApplyExclusions(ctx context.Context, ex Exclusions) error {
	names := ex.Containers
	if len(names) == 0 {
		names = []string{"\x00doupro-no-such-container"}
	}
	stacks := ex.Stacks
	if len(stacks) == 0 {
		stacks = []string{"\x00doupro-no-such-stack"}
	}

	namePlaceholders := make([]string, len(names))
	args := make([]any, 0, len(names)+len(stacks))
	for i, n := range names {
		namePlaceholders[i] = "?"
		args = append(args, n)
	}
	stackPlaceholders := make([]string, len(stacks))
	for i, st := range stacks {
		stackPlaceholders[i] = "?"
		args = append(args, st)
	}

	q := fmt.Sprintf(`UPDATE containers SET excluded = CASE WHEN name IN (%s) OR stack IN (%s) THEN 1 ELSE 0 END;`,
		strings.Join(namePlaceholders, ","), strings.Join(stackPlaceholders, ","))
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("apply exclusions: %w", err)
	}
	return nil
}

// GetContainer looks up one tracked container by ID, or ErrNotFound.
func (s *Store) GetContainer(ctx context.Context, id string) (ContainerRecord, error) {
	const q = `
SELECT id, name, stack, state, status, current_image, previous_image,
       current_version, available_version,
       update_available, excluded, last_checked_at
FROM containers WHERE id = ?;`

	var c ContainerRecord
	var lastChecked sql.NullString
	err := s.db.QueryRowContext(ctx, q, id).Scan(
		&c.ID, &c.Name, &c.Stack, &c.State, &c.Status,
		&c.CurrentImage, &c.PreviousImage,
		&c.CurrentVersion, &c.AvailableVersion,
		&c.UpdateAvailable, &c.Excluded, &lastChecked,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ContainerRecord{}, ErrNotFound
	}
	if err != nil {
		return ContainerRecord{}, fmt.Errorf("get container %s: %w", id, err)
	}
	if lastChecked.Valid {
		if t, err := time.Parse(time.RFC3339Nano, lastChecked.String); err == nil {
			c.LastCheckedAt = &t
		}
	}
	return c, nil
}

// GetContainerByName looks up one tracked container by name, or
// ErrNotFound — used by the CLI/API where operators refer to containers
// by name, not Docker's (recreate-volatile) ID.
func (s *Store) GetContainerByName(ctx context.Context, name string) (ContainerRecord, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM containers WHERE name = ?;", name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ContainerRecord{}, ErrNotFound
	}
	if err != nil {
		return ContainerRecord{}, fmt.Errorf("get container by name %s: %w", name, err)
	}
	return s.GetContainer(ctx, id)
}

// ReplaceContainer atomically retires oldID (a recreate changes Docker's
// container ID even though the name stays the same — see docs/containers.md
// on current/previous version tracking) and upserts rec as the new row.
// update_available is always reset to false: a fresh recreate has, by
// definition, just been checked.
func (s *Store) ReplaceContainer(ctx context.Context, oldID string, rec ContainerRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace container tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op if already committed

	if err := replaceContainerTx(ctx, tx, oldID, rec); err != nil {
		return err
	}

	return tx.Commit()
}

func replaceContainerTx(ctx context.Context, tx *sql.Tx, oldID string, rec ContainerRecord) error {
	if oldID != "" && oldID != rec.ID {
		if _, err := tx.ExecContext(ctx, "DELETE FROM containers WHERE id = ?;", oldID); err != nil {
			return fmt.Errorf("delete superseded container %s: %w", oldID, err)
		}
	}

	const q = `
INSERT INTO containers (id, name, stack, state, status, current_image, previous_image, update_available, current_version, available_version, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, '', '', ?)
ON CONFLICT(id) DO UPDATE SET
  name              = excluded.name,
  stack             = excluded.stack,
  state             = excluded.state,
  status            = excluded.status,
  current_image     = excluded.current_image,
  previous_image    = excluded.previous_image,
  update_available  = 0,
  current_version   = '',
  available_version = '',
  updated_at        = excluded.updated_at;`

	_, err := tx.ExecContext(ctx, q, rec.ID, rec.Name, rec.Stack, rec.State, rec.Status,
		rec.CurrentImage, rec.PreviousImage, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("upsert replacement container %s: %w", rec.ID, err)
	}

	return nil
}

// ReplaceContainerAndArmRollback atomically commits a successful running
// update and its crash-loop watch. A daemon restart can therefore never see
// the new container without also seeing the protection record.
func (s *Store) ReplaceContainerAndArmRollback(ctx context.Context, oldID string, rec ContainerRecord, appliedAt time.Time, window time.Duration, threshold, restartCount int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin protected replace tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := replaceContainerTx(ctx, tx, oldID, rec); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO rollback_watches
  (container_name, container_id, update_applied_at, window_expires_at, crash_count, threshold,
   observed_restart_count, last_event_nano, status, updated_at)
VALUES (?, ?, ?, ?, 0, ?, ?, 0, 'active', ?)
ON CONFLICT(container_name) DO UPDATE SET
  container_id=excluded.container_id, update_applied_at=excluded.update_applied_at,
  window_expires_at=excluded.window_expires_at, crash_count=0,
  threshold=excluded.threshold,
  observed_restart_count=excluded.observed_restart_count, last_event_nano=0,
  last_crash_at=NULL, triggered_at=NULL, status='active', updated_at=excluded.updated_at;`,
		rec.Name, rec.ID, appliedAt.UTC().Format(time.RFC3339Nano),
		appliedAt.Add(window).UTC().Format(time.RFC3339Nano), threshold, restartCount,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("arm rollback watch for %s: %w", rec.Name, err)
	}
	return tx.Commit()
}

// ReplaceContainerAndCompleteRollback atomically commits the recreated
// rollback target and closes any post-update watch. A restart can never replay
// a rollback that already changed the container successfully.
func (s *Store) ReplaceContainerAndCompleteRollback(ctx context.Context, oldID string, rec ContainerRecord, watchStatus string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rollback commit tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := replaceContainerTx(ctx, tx, oldID, rec); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rollback_watches SET status=?, updated_at=? WHERE container_name=?;`, watchStatus, time.Now().UTC().Format(time.RFC3339Nano), rec.Name); err != nil {
		return fmt.Errorf("complete rollback watch for %s: %w", rec.Name, err)
	}
	return tx.Commit()
}

// SetUpdateAvailable records the outcome of a registry check for one
// container and stamps last_checked_at. See docs/containers.md.
func (s *Store) SetUpdateAvailable(ctx context.Context, id string, available bool) error {
	v := 0
	if available {
		v = 1
	}
	const q = `UPDATE containers SET update_available = ?, last_checked_at = ? WHERE id = ?;`
	_, err := s.db.ExecContext(ctx, q, v, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("set update_available for %s: %w", id, err)
	}
	return nil
}

// SetVersions records the best display version for one container's
// current and (if any) available image digest — see
// ContainerRecord.CurrentVersion/AvailableVersion. This never blocks or
// affects SetUpdateAvailable, which owns the actual update-available
// signal.
func (s *Store) SetVersions(ctx context.Context, id, currentVersion, availableVersion string) error {
	const q = `UPDATE containers SET current_version = ?, available_version = ? WHERE id = ?;`
	_, err := s.db.ExecContext(ctx, q, currentVersion, availableVersion, id)
	if err != nil {
		return fmt.Errorf("set versions for %s: %w", id, err)
	}
	return nil
}

// ListContainers returns every tracked container, ordered by stack then
// name — the same grouping the Containers page uses (docs/containers.md).
func (s *Store) ListContainers(ctx context.Context) ([]ContainerRecord, error) {
	const q = `
SELECT id, name, stack, state, status, current_image, previous_image,
       current_version, available_version,
       update_available, excluded, last_checked_at
FROM containers
ORDER BY stack, name;`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	defer rows.Close() //nolint:errcheck // no actionable recovery from this error

	var out []ContainerRecord
	for rows.Next() {
		var c ContainerRecord
		var lastChecked sql.NullString
		if err := rows.Scan(
			&c.ID, &c.Name, &c.Stack, &c.State, &c.Status,
			&c.CurrentImage, &c.PreviousImage,
			&c.CurrentVersion, &c.AvailableVersion,
			&c.UpdateAvailable, &c.Excluded, &lastChecked,
		); err != nil {
			return nil, fmt.Errorf("scan container: %w", err)
		}
		if lastChecked.Valid {
			if t, err := time.Parse(time.RFC3339Nano, lastChecked.String); err == nil {
				c.LastCheckedAt = &t
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

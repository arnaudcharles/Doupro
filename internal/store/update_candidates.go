package store

import (
	"context"
	"fmt"
	"time"
)

type UpdateCandidate struct {
	ContainerID string
	Scope       string
	Image       string
	Version     string
	DetectedAt  time.Time
}

// ReplaceUpdateCandidates atomically refreshes the candidates for a
// container. detected_at survives checks while the exact target stays the
// same, which makes delayed policies durable and restart-safe.
func (s *Store) ReplaceUpdateCandidates(ctx context.Context, containerID string, candidates []UpdateCandidate, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op if already committed
	wanted := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		wanted[candidate.Scope] = true
		_, err = tx.ExecContext(ctx, `INSERT INTO update_candidates(container_id,scope,image,version,detected_at,updated_at)
VALUES(?,?,?,?,?,?) ON CONFLICT(container_id,scope) DO UPDATE SET
 image=excluded.image, version=excluded.version,
 detected_at=CASE WHEN update_candidates.image=excluded.image THEN update_candidates.detected_at ELSE excluded.detected_at END,
 updated_at=excluded.updated_at`, containerID, candidate.Scope, candidate.Image, candidate.Version,
			now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("upsert %s update candidate: %w", candidate.Scope, err)
		}
	}
	for _, scope := range []string{"patch", "minor", "major"} {
		if !wanted[scope] {
			if _, err := tx.ExecContext(ctx, `DELETE FROM update_candidates WHERE container_id=? AND scope=?`, containerID, scope); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) GetUpdateCandidate(ctx context.Context, containerID, scope string) (UpdateCandidate, error) {
	var c UpdateCandidate
	var detected string
	err := s.db.QueryRowContext(ctx, `SELECT container_id,scope,image,version,detected_at FROM update_candidates WHERE container_id=? AND scope=?`, containerID, scope).
		Scan(&c.ContainerID, &c.Scope, &c.Image, &c.Version, &detected)
	if err != nil {
		return c, fmt.Errorf("get %s update candidate: %w", scope, err)
	}
	c.DetectedAt, _ = time.Parse(time.RFC3339Nano, detected)
	return c, nil
}

func (s *Store) ClearUpdateCandidates(ctx context.Context, containerID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM update_candidates WHERE container_id=?`, containerID)
	return err
}

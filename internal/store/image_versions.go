package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type ImageVersionRecord struct {
	Registry   string
	Repository string
	Digest     string
	Version    string
	Status     string
	CheckedAt  time.Time
}

func (s *Store) GetImageVersion(ctx context.Context, registry, repository, digest string) (ImageVersionRecord, bool, error) {
	var rec ImageVersionRecord
	var checkedAt string
	err := s.db.QueryRowContext(ctx, `SELECT registry,repository,digest,version,status,checked_at
		FROM image_versions WHERE registry=? AND repository=? AND digest=?`, registry, repository, digest).
		Scan(&rec.Registry, &rec.Repository, &rec.Digest, &rec.Version, &rec.Status, &checkedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ImageVersionRecord{}, false, nil
	}
	if err != nil {
		return ImageVersionRecord{}, false, fmt.Errorf("get image version: %w", err)
	}
	rec.CheckedAt, err = time.Parse(time.RFC3339Nano, checkedAt)
	if err != nil {
		return ImageVersionRecord{}, false, fmt.Errorf("parse image version timestamp: %w", err)
	}
	return rec, true, nil
}

func (s *Store) PutImageVersion(ctx context.Context, rec ImageVersionRecord) error {
	if rec.CheckedAt.IsZero() {
		rec.CheckedAt = time.Now().UTC()
	}
	if rec.Status == "" {
		if rec.Version == "" {
			rec.Status = "unresolved"
		} else {
			rec.Status = "resolved"
		}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO image_versions(registry,repository,digest,version,status,checked_at)
		VALUES(?,?,?,?,?,?) ON CONFLICT(registry,repository,digest) DO UPDATE SET
		version=excluded.version,status=excluded.status,checked_at=excluded.checked_at`, rec.Registry, rec.Repository,
		rec.Digest, rec.Version, rec.Status, rec.CheckedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("put image version: %w", err)
	}
	return nil
}

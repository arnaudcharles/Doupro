package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	appsecrets "github.com/arnaudcharles/doupro/internal/secrets"
)

func (s *Store) encryptSecret(value string) (string, error) {
	if s.secrets == nil {
		return "", errors.New("secret encryption is not configured")
	}
	return s.secrets.Encrypt(value)
}

func (s *Store) decryptSecret(value string) (string, error) {
	if !appsecrets.IsEncrypted(value) {
		return value, nil
	}
	if s.secrets == nil {
		return "", errors.New("secret encryption is not configured")
	}
	return s.secrets.Decrypt(value)
}

func (s *Store) GetSecretSetting(ctx context.Context, key string) (string, bool, error) {
	value, ok, err := s.GetSetting(ctx, key)
	if err != nil || !ok {
		return value, ok, err
	}
	plain, err := s.decryptSecret(value)
	if err != nil {
		return "", false, fmt.Errorf("decrypt setting %s: %w", key, err)
	}
	return plain, true, nil
}

func (s *Store) SetSecretSetting(ctx context.Context, key, value string) error {
	encrypted, err := s.encryptSecret(value)
	if err != nil {
		return fmt.Errorf("encrypt setting %s: %w", key, err)
	}
	return s.SetSetting(ctx, key, encrypted)
}

func (s *Store) migratePlaintextSecrets(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin secret migration: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op if already committed
	var value string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='notifications.channels'`).Scan(&value)
	if err == nil && !appsecrets.IsEncrypted(value) {
		enc, encErr := s.encryptSecret(value)
		if encErr != nil {
			return encErr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE settings SET value=? WHERE key='notifications.channels'`, enc); err != nil {
			return err
		}
	} else if err == nil {
		if _, err := s.decryptSecret(value); err != nil {
			return fmt.Errorf("validate encrypted notification settings: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var registryValue string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='registries.configs'`).Scan(&registryValue)
	if err == nil && !appsecrets.IsEncrypted(registryValue) {
		enc, encErr := s.encryptSecret(registryValue)
		if encErr != nil {
			return encErr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE settings SET value=? WHERE key='registries.configs'`, enc); err != nil {
			return err
		}
	} else if err == nil {
		if _, err := s.decryptSecret(registryValue); err != nil {
			return fmt.Errorf("validate encrypted registry settings: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,channel_url FROM notification_queue`)
	if err != nil {
		return fmt.Errorf("list queued secrets: %w", err)
	}
	type update struct {
		id    int64
		value string
	}
	var updates []update
	for rows.Next() {
		var u update
		if err := rows.Scan(&u.id, &u.value); err != nil {
			rows.Close() //nolint:errcheck // returning an error already; nothing to recover
			return err
		}
		if !appsecrets.IsEncrypted(u.value) {
			u.value, err = s.encryptSecret(u.value)
			if err != nil {
				rows.Close() //nolint:errcheck // returning an error already; nothing to recover
				return err
			}
			updates = append(updates, u)
		} else if _, err = s.decryptSecret(u.value); err != nil {
			rows.Close() //nolint:errcheck // returning an error already; nothing to recover
			return fmt.Errorf("validate encrypted notification job %d: %w", u.id, err)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, u := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE notification_queue SET channel_url=? WHERE id=?`, u.value, u.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CountUnencryptedSecrets(ctx context.Context) (int, error) {
	var settingsCount, queueCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key IN ('notifications.channels','registries.configs') AND value NOT LIKE 'enc:v1:%'`).Scan(&settingsCount); err != nil {
		return 0, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_queue WHERE channel_url NOT LIKE 'enc:v1:%'`).Scan(&queueCount); err != nil {
		return 0, err
	}
	return settingsCount + queueCount, nil
}

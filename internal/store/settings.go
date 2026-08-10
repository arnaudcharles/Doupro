package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// GetSetting returns the raw stored value for key, and whether it exists.
// Runtime-editable settings (docs/settings.md) are stored here as flat
// key/value pairs, namespaced by dotted prefix (e.g.
// "notifications.channels"); typed access and validation belongs to the
// caller (internal/api, internal/notifier, ...), not this package.
func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?;", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get setting %s: %w", key, err)
	}
	return value, true, nil
}

// SetSetting upserts a raw value for key.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	const q = `
INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;`
	_, err := s.db.ExecContext(ctx, q, key, value, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("set setting %s: %w", key, err)
	}
	return nil
}

// ListSettingsByPrefix returns every key/value pair whose key starts with
// prefix — used to fetch a whole settings group at once (docs/settings.md
// groups: general, registries, notifications, ...).
func (s *Store) ListSettingsByPrefix(ctx context.Context, prefix string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, value FROM settings WHERE key LIKE ?;", prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("list settings %s*: %w", prefix, err)
	}
	defer rows.Close() //nolint:errcheck // no actionable recovery from this error

	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		if strings.HasPrefix(k, prefix) {
			out[k] = v
		}
	}
	return out, rows.Err()
}

// --- General ----------------------------------------------------------

const checkIntervalKey = "general.check_interval_minutes"

const (
	crashLoopThresholdKey = "updates.crash_loop_threshold"
	crashLoopWindowKey    = "updates.crash_loop_window_minutes"
)

const logsPageSizeKey = "general.logs_page_size"

// Logs page size bounds (docs/logs.md). The floor keeps the page usable —
// below it, filtering becomes the only practical way to find anything. The
// ceiling is a deliberate engineering call, not a hard technical limit:
// SQLite handles a few thousand rows trivially, but the Logs page renders
// every row as DOM directly (no client-side pagination/virtualization), so
// this is where the browser starts to feel it, not where the database
// would.
const (
	DefaultLogsPageSize = 300
	MinLogsPageSize     = 50
	MaxLogsPageSize     = 1000
)

// GetLogsPageSize returns the configured Logs page display size, or
// DefaultLogsPageSize if never set (or set to something outside
// [MinLogsPageSize, MaxLogsPageSize], which is treated as unset rather
// than erroring the page).
func (s *Store) GetLogsPageSize(ctx context.Context) (int, error) {
	raw, ok, err := s.GetSetting(ctx, logsPageSizeKey)
	if err != nil {
		return DefaultLogsPageSize, err
	}
	if !ok {
		return DefaultLogsPageSize, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < MinLogsPageSize || n > MaxLogsPageSize {
		return DefaultLogsPageSize, nil
	}
	return n, nil
}

// SetLogsPageSize updates the configured Logs page display size. Callers
// are expected to validate against [MinLogsPageSize, MaxLogsPageSize]
// first (see internal/api.handleSetGeneralSettings) so a bad value is
// rejected with a clear error instead of silently falling back later.
func (s *Store) SetLogsPageSize(ctx context.Context, size int) error {
	return s.SetSetting(ctx, logsPageSizeKey, strconv.Itoa(size))
}

// GetCheckIntervalMinutes returns the configured registry-check interval,
// or ok=false if it's never been set (caller applies the documented
// default — see docs/settings.md). Read fresh on every scheduler cycle
// (internal/scheduler) rather than cached, which is what makes changing
// it from Settings take effect without a restart.
func (s *Store) GetCheckIntervalMinutes(ctx context.Context) (int, bool, error) {
	raw, ok, err := s.GetSetting(ctx, checkIntervalKey)
	if err != nil || !ok {
		return 0, false, err
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false, nil // treat a corrupt value as unset rather than erroring the whole check cycle
	}
	return n, true, nil
}

// SetCheckIntervalMinutes updates the registry-check interval.
func (s *Store) SetCheckIntervalMinutes(ctx context.Context, minutes int) error {
	return s.SetSetting(ctx, checkIntervalKey, strconv.Itoa(minutes))
}

func (s *Store) GetCrashLoopSettings(ctx context.Context) (threshold, windowMinutes int, err error) {
	threshold = DefaultCrashLoopThreshold
	windowMinutes = int(DefaultCrashLoopWindow / time.Minute)
	if raw, ok, getErr := s.GetSetting(ctx, crashLoopThresholdKey); getErr != nil {
		return 0, 0, getErr
	} else if ok {
		if value, parseErr := strconv.Atoi(raw); parseErr == nil && value > 0 {
			threshold = value
		}
	}
	if raw, ok, getErr := s.GetSetting(ctx, crashLoopWindowKey); getErr != nil {
		return 0, 0, getErr
	} else if ok {
		if value, parseErr := strconv.Atoi(raw); parseErr == nil && value > 0 {
			windowMinutes = value
		}
	}
	return threshold, windowMinutes, nil
}

func (s *Store) SetCrashLoopSettings(ctx context.Context, threshold, windowMinutes int) error {
	if err := s.SetSetting(ctx, crashLoopThresholdKey, strconv.Itoa(threshold)); err != nil {
		return err
	}
	return s.SetSetting(ctx, crashLoopWindowKey, strconv.Itoa(windowMinutes))
}

// --- Exclusions ---------------------------------------------------------

const (
	excludedContainersKey = "exclusions.containers"
	excludedStacksKey     = "exclusions.stacks"
)

// Exclusions is the Settings -> Exclusions list (docs/settings.md): ad-hoc
// exclusions by name, on top of the doupro.enable=false label.
type Exclusions struct {
	Containers []string `json:"containers"`
	Stacks     []string `json:"stacks"`
}

// GetExclusions returns the configured exclusion lists (empty if never set).
func (s *Store) GetExclusions(ctx context.Context) (Exclusions, error) {
	var ex Exclusions
	if raw, ok, err := s.GetSetting(ctx, excludedContainersKey); err != nil {
		return Exclusions{}, err
	} else if ok {
		_ = json.Unmarshal([]byte(raw), &ex.Containers)
	}
	if raw, ok, err := s.GetSetting(ctx, excludedStacksKey); err != nil {
		return Exclusions{}, err
	} else if ok {
		_ = json.Unmarshal([]byte(raw), &ex.Stacks)
	}
	return ex, nil
}

// SetExclusions replaces the configured exclusion lists wholesale.
func (s *Store) SetExclusions(ctx context.Context, ex Exclusions) error {
	containers, err := json.Marshal(ex.Containers)
	if err != nil {
		return fmt.Errorf("marshal excluded containers: %w", err)
	}
	stacks, err := json.Marshal(ex.Stacks)
	if err != nil {
		return fmt.Errorf("marshal excluded stacks: %w", err)
	}
	if err := s.SetSetting(ctx, excludedContainersKey, string(containers)); err != nil {
		return err
	}
	return s.SetSetting(ctx, excludedStacksKey, string(stacks))
}

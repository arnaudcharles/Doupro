package store

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	appsecrets "github.com/arnaudcharles/doupro/internal/secrets"
)

func configureTestSecrets(t *testing.T, ctx context.Context, st *Store) {
	t.Helper()
	cipher, err := appsecrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ConfigureSecrets(ctx, cipher); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationQueuePersistsAndReclaimsExpiredLease(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "doupro.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	configureTestSecrets(t, ctx, st)
	id, err := st.EnqueueNotification(ctx, NotificationJob{Event: "update.succeeded", Channel: "ops", ChannelURL: "generic://host", Message: "done"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	job, err := st.ClaimNotification(ctx, now, time.Second)
	if err != nil || job == nil || job.ID != id || job.Attempts != 1 {
		t.Fatalf("first claim=%+v err=%v", job, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	configureTestSecrets(t, ctx, st)
	t.Cleanup(func() { _ = st.Close() })
	if early, err := st.ClaimNotification(ctx, now.Add(500*time.Millisecond), time.Second); err != nil || early != nil {
		t.Fatalf("unexpired lease claim=%+v err=%v", early, err)
	}
	reclaimed, err := st.ClaimNotification(ctx, now.Add(2*time.Second), time.Second)
	if err != nil || reclaimed == nil || reclaimed.ID != id || reclaimed.Attempts != 2 {
		t.Fatalf("reclaimed=%+v err=%v", reclaimed, err)
	}
}

func TestNotificationDeadLetterCanBeManuallyRetried(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	configureTestSecrets(t, ctx, st)
	t.Cleanup(func() { _ = st.Close() })
	id, err := st.EnqueueNotification(ctx, NotificationJob{Event: "test", Channel: "ops", ChannelURL: "generic://host", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.ClaimNotification(ctx, time.Now(), time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	if err := st.FailNotification(ctx, id, time.Now(), "offline", true); err != nil {
		t.Fatal(err)
	}
	if err := st.RetryNotification(ctx, id); err != nil {
		t.Fatal(err)
	}
	jobs, err := st.ListNotificationJobs(ctx, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	if jobs[0].Status != "pending" || jobs[0].Attempts != 0 || jobs[0].LastError != "" {
		t.Fatalf("retried job=%+v", jobs[0])
	}
}

func TestConfigureSecretsMigratesLegacyPlaintextAndRejectsWrongKey(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "doupro.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `[{"name":"ops","url":"telegram://bot:secret@telegram"}]`
	if err := st.SetSetting(ctx, "notifications.channels", legacy); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := st.db.ExecContext(ctx, `INSERT INTO notification_queue
(event,channel,channel_url,status,next_attempt_at,created_at,updated_at) VALUES('test','ops','generic://secret','pending',?,?,?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	cipher, _ := appsecrets.New(make([]byte, 32))
	if err := st.ConfigureSecrets(ctx, cipher); err != nil {
		t.Fatal(err)
	}
	var rawSetting, rawURL string
	if err := st.db.QueryRow(`SELECT value FROM settings WHERE key='notifications.channels'`).Scan(&rawSetting); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT channel_url FROM notification_queue LIMIT 1`).Scan(&rawURL); err != nil {
		t.Fatal(err)
	}
	if rawSetting == legacy || rawURL == "generic://secret" {
		t.Fatalf("plaintext remains setting=%q url=%q", rawSetting, rawURL)
	}
	if count, err := st.CountUnencryptedSecrets(ctx); err != nil || count != 0 {
		t.Fatalf("unencrypted=%d err=%v", count, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	wrong, _ := appsecrets.New(bytes.Repeat([]byte{1}, 32))
	if err := st.ConfigureSecrets(ctx, wrong); err == nil {
		t.Fatal("wrong key accepted")
	}
}

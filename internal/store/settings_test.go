package store

import (
	"context"
	"testing"
)

func TestLogsPageSize(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	if got, err := st.GetLogsPageSize(ctx); err != nil || got != DefaultLogsPageSize {
		t.Fatalf("unset GetLogsPageSize = (%d, %v), want (%d, nil)", got, err, DefaultLogsPageSize)
	}

	if err := st.SetLogsPageSize(ctx, 750); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetLogsPageSize(ctx); err != nil || got != 750 {
		t.Fatalf("GetLogsPageSize after set(750) = (%d, %v), want (750, nil)", got, err)
	}
}

func TestLogsPageSizeIgnoresCorruptOrOutOfRangeStoredValue(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	for _, bad := range []string{"not-a-number", "0", "49", "1001", "-5"} {
		if err := st.SetSetting(ctx, logsPageSizeKey, bad); err != nil {
			t.Fatal(err)
		}
		if got, err := st.GetLogsPageSize(ctx); err != nil || got != DefaultLogsPageSize {
			t.Errorf("GetLogsPageSize with stored value %q = (%d, %v), want (%d, nil)", bad, got, err, DefaultLogsPageSize)
		}
	}
}

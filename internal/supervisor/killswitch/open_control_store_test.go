package killswitch

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

func TestTheControlStoreIsOpenedOnlyWhenPreparedAndNeverMigrated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFactory(t)
	if _, err := OpenControlStore(ctx, core.NewSecretValue(nil), testFactory, f.clock); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("no connection = %v, want INVALID_ARGUMENT", err)
	}
	fresh := filepath.Join(t.TempDir(), "fresh.db")
	if _, err := OpenControlStore(ctx, core.NewSecretValue([]byte(fresh)), testFactory, f.clock); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("an unprepared store = %v, want UNAVAILABLE", err)
	}
	prepared := filepath.Join(t.TempDir(), "prepared.db")
	setup := newFactoryAt(t, prepared)
	store, err := OpenControlStore(ctx, core.NewSecretValue([]byte(prepared)), testFactory, setup.clock)
	if err != nil {
		t.Fatalf("a prepared store = %v", err)
	}
	_ = store.Close()
}

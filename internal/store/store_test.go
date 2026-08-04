package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ankittk/chargepoints-api/internal/store"
	"github.com/ankittk/chargepoints-api/pkg/chargepoint"
)

func TestCreateGetNearby(t *testing.T) {
	t.Parallel()
	st := openTemp(t)
	ctx := context.Background()

	cp, err := st.Create(ctx, chargepoint.ChargePoint{
		Name:     "Test Spot",
		Location: chargepoint.Location{Lat: 52.37, Lon: 4.89},
		Status:   chargepoint.StatusAvailable,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cp.ID == "" {
		t.Fatal("expected id")
	}

	got, err := st.GetByID(ctx, cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Test Spot" {
		t.Fatalf("name = %q", got.Name)
	}

	_, err = st.GetByID(ctx, "00000000-0000-0000-0000-000000000000")
	if err != store.ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	far, err := st.Create(ctx, chargepoint.ChargePoint{
		Name:     "Far",
		Location: chargepoint.Location{Lat: 51.92, Lon: 4.47},
		Status:   chargepoint.StatusOccupied,
	})
	if err != nil {
		t.Fatal(err)
	}

	near, err := st.Nearby(ctx, 52.37, 4.89, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(near) != 1 || near[0].ID != cp.ID {
		t.Fatalf("nearby short = %+v", near)
	}

	wide, err := st.Nearby(ctx, 52.37, 4.89, 80)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, p := range wide {
		ids[p.ID] = true
	}
	if !ids[cp.ID] || !ids[far.ID] {
		t.Fatalf("nearby wide missing points: %+v", wide)
	}
}

func TestSeedIfEmpty(t *testing.T) {
	t.Parallel()
	st := openTemp(t)
	ctx := context.Background()
	if err := st.SeedIfEmpty(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := st.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("count = %d, want 4", n)
	}
	if err := st.SeedIfEmpty(ctx); err != nil {
		t.Fatal(err)
	}
	n2, _ := st.Count(ctx)
	if n2 != 4 {
		t.Fatalf("seed twice grew to %d", n2)
	}
}

func openTemp(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

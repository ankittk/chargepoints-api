package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ankittk/chargepoints-api/internal/geo"
	"github.com/ankittk/chargepoints-api/pkg/chargepoint"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("charge point not found")
	tracer      = otel.Tracer("github.com/ankittk/chargepoints-api/internal/store")
)

// Store persists charge points in SQLite (on-disk file, not an in-memory map).
// Safe for concurrent use by the HTTP server without an extra sync.Mutex — see Open.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite database at path, pings it, and runs migrations.
func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Concurrency model (no Go mutex on Store):
	//
	// 1) SetMaxOpenConns(1) — database/sql would otherwise open a pool of conns.
	//    Multiple SQLite writers on separate conns hit SQLITE_BUSY under load.
	//    One conn serializes all queries in this process.
	//
	// 2) SQLite's own file lock — even with multiple processes, the DB file is
	//    locked at the OS/SQLite layer for writers. Readers can share under WAL
	//    (enabled below); writers still take an exclusive lock for the write.
	//
	// 3) Not an in-memory store — data lives on disk (path). We deliberately do
	//    not keep an in-memory map + mutex in front of SQLite; the DB is source of
	//    truth. An in-memory cache would need its own sync.Mutex (or RWMutex) and
	//    invalidation rules — skip until profiling says we need it.
	//
	// Upgrade path: WAL + higher MaxOpenConns for read-heavy load, or Postgres.
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, err
	}
	// WAL: readers do not block writers as hard as rollback-journal mode.
	// Still one writer at a time (SQLite file lock); pairs with MaxOpenConns(1).
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode = WAL`); err != nil {
		_ = db.Close()
		return nil, err
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS charge_points (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	lat REAL NOT NULL,
	lon REAL NOT NULL,
	status TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_charge_points_lat_lon ON charge_points (lat, lon);
`)
	return err
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Ping checks database connectivity (readiness).
func (s *Store) Ping(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "store.Ping")
	defer span.End()
	if err := s.db.PingContext(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}

// Create inserts a new charge point, assigning a UUID. Client-supplied ID is ignored.
func (s *Store) Create(ctx context.Context, cp chargepoint.ChargePoint) (chargepoint.ChargePoint, error) {
	ctx, span := tracer.Start(ctx, "store.Create")
	defer span.End()

	cp.ID = uuid.NewString()
	span.SetAttributes(attribute.String("chargepoint.id", cp.ID))
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO charge_points (id, name, lat, lon, status) VALUES (?, ?, ?, ?, ?)`,
		cp.ID, cp.Name, cp.Location.Lat, cp.Location.Lon, string(cp.Status),
	)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return chargepoint.ChargePoint{}, fmt.Errorf("insert: %w", err)
	}
	return cp, nil
}

// GetByID returns a charge point or ErrNotFound.
func (s *Store) GetByID(ctx context.Context, id string) (chargepoint.ChargePoint, error) {
	ctx, span := tracer.Start(ctx, "store.GetByID", trace.WithAttributes(attribute.String("chargepoint.id", id)))
	defer span.End()

	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, lat, lon, status FROM charge_points WHERE id = ?`, id)
	cp, err := scanCP(row)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		return chargepoint.ChargePoint{}, err
	}
	return cp, nil
}

// Nearby returns charge points within radiusKm of (lat, lon).
func (s *Store) Nearby(ctx context.Context, lat, lon, radiusKm float64) ([]chargepoint.ChargePoint, error) {
	ctx, span := tracer.Start(ctx, "store.Nearby", trace.WithAttributes(
		attribute.Float64("geo.lat", lat),
		attribute.Float64("geo.lon", lon),
		attribute.Float64("geo.radius_km", radiusKm),
	))
	defer span.End()

	minLat, maxLat, minLon, maxLon := geo.BoundingBox(lat, lon, radiusKm)
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, lat, lon, status FROM charge_points
WHERE lat BETWEEN ? AND ? AND lon BETWEEN ? AND ?`,
		minLat, maxLat, minLon, maxLon)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("query nearby: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []chargepoint.ChargePoint
	for rows.Next() {
		cp, err := scanCP(rows)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, err
		}
		if geo.HaversineKm(lat, lon, cp.Location.Lat, cp.Location.Lon) <= radiusKm {
			out = append(out, cp)
		}
	}
	if err := rows.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	if out == nil {
		out = []chargepoint.ChargePoint{}
	}
	span.SetAttributes(attribute.Int("chargepoint.count", len(out)))
	return out, nil
}

// Count returns number of rows (for seed check).
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM charge_points`).Scan(&n)
	return n, err
}

// SeedIfEmpty inserts demo points when the table is empty (single transaction).
func (s *Store) SeedIfEmpty(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM charge_points`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return tx.Commit()
	}

	seeds := []chargepoint.ChargePoint{
		{Name: "Amsterdam Centraal", Location: chargepoint.Location{Lat: 52.3791, Lon: 4.9003}, Status: chargepoint.StatusAvailable},
		{Name: "Museumplein", Location: chargepoint.Location{Lat: 52.3579, Lon: 4.8812}, Status: chargepoint.StatusOccupied},
		{Name: "Schiphol P1", Location: chargepoint.Location{Lat: 52.3105, Lon: 4.7683}, Status: chargepoint.StatusAvailable},
		{Name: "Rotterdam CS", Location: chargepoint.Location{Lat: 51.9244, Lon: 4.4699}, Status: chargepoint.StatusOffline},
	}
	for _, cp := range seeds {
		id := uuid.NewString()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO charge_points (id, name, lat, lon, status) VALUES (?, ?, ?, ?, ?)`,
			id, cp.Name, cp.Location.Lat, cp.Location.Lon, string(cp.Status),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanCP(row scanner) (chargepoint.ChargePoint, error) {
	var cp chargepoint.ChargePoint
	var status string
	err := row.Scan(&cp.ID, &cp.Name, &cp.Location.Lat, &cp.Location.Lon, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return chargepoint.ChargePoint{}, ErrNotFound
	}
	if err != nil {
		return chargepoint.ChargePoint{}, err
	}
	cp.Status = chargepoint.Status(status)
	return cp, nil
}

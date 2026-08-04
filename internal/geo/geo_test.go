package geo_test

import (
	"math"
	"testing"

	"github.com/ankittk/chargepoints-api/internal/geo"
)

func TestHaversineKm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		lat1, lon1 float64
		lat2, lon2 float64
		wantKm     float64
		tolKm      float64
	}{
		{name: "same point", lat1: 52.37, lon1: 4.89, lat2: 52.37, lon2: 4.89, wantKm: 0, tolKm: 0.001},
		// Amsterdam Centraal ≈ Museumplein ~2.7 km
		{name: "amsterdam short", lat1: 52.3791, lon1: 4.9003, lat2: 52.3579, lon2: 4.8812, wantKm: 2.7, tolKm: 0.3},
		// Amsterdam to Rotterdam ≈ 57 km
		{name: "ams to rotterdam", lat1: 52.3791, lon1: 4.9003, lat2: 51.9244, lon2: 4.4699, wantKm: 57, tolKm: 3},
		// 1° latitude ≈ 111 km anywhere
		{name: "one degree latitude", lat1: 0, lon1: 0, lat2: 1, lon2: 0, wantKm: 111, tolKm: 1},
		// Equator 1° longitude ≈ 111 km
		{name: "one degree longitude at equator", lat1: 0, lon1: 0, lat2: 0, lon2: 1, wantKm: 111, tolKm: 1},
		// Opposite points on Earth ≈ half circumference (~20015 km)
		{name: "antipodes-ish", lat1: 0, lon1: 0, lat2: 0, lon2: 180, wantKm: 20015, tolKm: 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := geo.HaversineKm(tt.lat1, tt.lon1, tt.lat2, tt.lon2)
			if math.Abs(got-tt.wantKm) > tt.tolKm {
				t.Fatalf("HaversineKm = %v, want ~%v (±%v)", got, tt.wantKm, tt.tolKm)
			}
		})
	}
}

func TestHaversineKmSymmetric(t *testing.T) {
	t.Parallel()
	a := geo.HaversineKm(52.3791, 4.9003, 51.9244, 4.4699)
	b := geo.HaversineKm(51.9244, 4.4699, 52.3791, 4.9003)
	if math.Abs(a-b) > 1e-9 {
		t.Fatalf("not symmetric: %v vs %v", a, b)
	}
}

func TestBoundingBoxContainsCenter(t *testing.T) {
	t.Parallel()
	minLat, maxLat, minLon, maxLon := geo.BoundingBox(52.37, 4.89, 10)
	if !(minLat < 52.37 && 52.37 < maxLat && minLon < 4.89 && 4.89 < maxLon) {
		t.Fatalf("bbox does not contain center: %v %v %v %v", minLat, maxLat, minLon, maxLon)
	}
}

func TestBoundingBoxZeroRadius(t *testing.T) {
	t.Parallel()
	minLat, maxLat, minLon, maxLon := geo.BoundingBox(10, 20, 0)
	if minLat != 10 || maxLat != 10 || minLon != 20 || maxLon != 20 {
		t.Fatalf("zero radius should be a point, got %v %v %v %v", minLat, maxLat, minLon, maxLon)
	}
}

func TestBoundingBoxLatSpanMatchesRadius(t *testing.T) {
	t.Parallel()
	// 111 km ≈ 1° latitude, so radius 111 → about ±1° lat
	minLat, maxLat, _, _ := geo.BoundingBox(0, 0, 111)
	if math.Abs(maxLat-minLat-2) > 0.05 {
		t.Fatalf("lat span = %v, want ~2°", maxLat-minLat)
	}
}

func TestBoundingBoxLonWiderNearPoles(t *testing.T) {
	t.Parallel()
	// Same radius: lon span must be larger at high latitude than at equator.
	_, _, eqMin, eqMax := geo.BoundingBox(0, 0, 50)
	_, _, polMin, polMax := geo.BoundingBox(60, 0, 50)
	eqSpan := eqMax - eqMin
	polSpan := polMax - polMin
	if polSpan <= eqSpan {
		t.Fatalf("expected wider lon span near pole: equator=%v pole=%v", eqSpan, polSpan)
	}
}

func TestBoundingBoxClampsLat(t *testing.T) {
	t.Parallel()
	minLat, maxLat, _, _ := geo.BoundingBox(89, 0, 500)
	if minLat < -90 || maxLat > 90 {
		t.Fatalf("lat out of range: %v %v", minLat, maxLat)
	}
	if maxLat != 90 {
		t.Fatalf("maxLat should clamp to 90, got %v", maxLat)
	}
}

func TestBoundingBoxClampsLon(t *testing.T) {
	t.Parallel()
	_, _, minLon, maxLon := geo.BoundingBox(0, 179, 500)
	if minLon < -180 || maxLon > 180 {
		t.Fatalf("lon out of range: %v %v", minLon, maxLon)
	}
	if maxLon != 180 {
		t.Fatalf("maxLon should clamp to 180, got %v", maxLon)
	}
}

func TestBoundingBoxCoversHaversineNeighbors(t *testing.T) {
	t.Parallel()
	// A point ~5 km north of center must sit inside a 10 km bbox.
	centerLat, centerLon := 52.37, 4.89
	northLat := centerLat + (5.0 / 111.0)
	minLat, maxLat, minLon, maxLon := geo.BoundingBox(centerLat, centerLon, 10)
	if northLat < minLat || northLat > maxLat || centerLon < minLon || centerLon > maxLon {
		t.Fatalf("neighbor outside bbox: lat=%v box=[%v,%v]x[%v,%v]",
			northLat, minLat, maxLat, minLon, maxLon)
	}
	dist := geo.HaversineKm(centerLat, centerLon, northLat, centerLon)
	if dist > 10 {
		t.Fatalf("setup bug: neighbor is %v km away", dist)
	}
}

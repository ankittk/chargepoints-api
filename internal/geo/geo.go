package geo

import "math"

const (
	earthRadiusKm = 6371.0 // mean Earth radius used by Haversine
	kmPerDegLat   = 111.0  // rough km per 1° of latitude
)

// HaversineKm is the great-circle distance in km between two WGS84 points.
//
// Algorithm (Haversine):
//  1. Convert degrees to radians.
//  2. Compute half-chord lengths from dLat and dLon (the "haversines").
//  3. Combine them into a, then central angle c = 2 * atan2(sqrt(a), sqrt(1-a)).
//  4. Distance = earth radius * c.
//
// Good enough for "chargers within N km". Not a geodesic library.
func HaversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	phi1 := degreesToRadians(lat1)
	phi2 := degreesToRadians(lat2)
	dPhi := degreesToRadians(lat2 - lat1)
	dLambda := degreesToRadians(lon2 - lon1)

	sinHalfDPhi := math.Sin(dPhi / 2)
	sinHalfDLambda := math.Sin(dLambda / 2)

	a := sinHalfDPhi*sinHalfDPhi +
		math.Cos(phi1)*math.Cos(phi2)*sinHalfDLambda*sinHalfDLambda

	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusKm * c
}

// BoundingBox builds a rough lat/lon rectangle around (lat, lon) with radiusKm.
//
// Algorithm:
//  1. Latitude span ≈ radiusKm / 111 km per degree (almost constant).
//  2. Longitude span widens near the poles: divide by cos(lat), because
//     a degree of longitude covers fewer km as you leave the equator.
//  3. Clamp to valid WGS84 ranges.
//
// This box is only a prefilter. Callers must still run HaversineKm and keep
// points with distance <= radiusKm (corners of the box are farther than the
// circle radius).
//
// Ceiling: if the box would cross ±180° longitude we clamp instead of wrapping,
// so searches near the date line can miss points. Fix later with two lon ranges
// or a real spatial index (S2 / PostGIS).
func BoundingBox(lat, lon, radiusKm float64) (minLat, maxLat, minLon, maxLon float64) {
	dLat := radiusKm / kmPerDegLat
	minLat = clamp(lat-dLat, -90, 90)
	maxLat = clamp(lat+dLat, -90, 90)

	cosLat := math.Cos(degreesToRadians(lat))
	if cosLat < 1e-6 {
		// Avoid divide-by-zero near the poles.
		cosLat = 1e-6
	}
	dLon := radiusKm / (kmPerDegLat * cosLat)
	minLon = clamp(lon-dLon, -180, 180)
	maxLon = clamp(lon+dLon, -180, 180)
	return
}

func degreesToRadians(deg float64) float64 {
	return deg * math.Pi / 180
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

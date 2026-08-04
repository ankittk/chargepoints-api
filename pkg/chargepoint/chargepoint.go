package chargepoint

import (
	"fmt"
	"math"
	"unicode/utf8"
)

// Status is the operational state of a charge point.
type Status string

const (
	StatusAvailable Status = "AVAILABLE"
	StatusOccupied  Status = "OCCUPIED"
	StatusOffline   Status = "OFFLINE"

	// NameMaxLen is the maximum UTF-8 rune count for a charge point name.
	NameMaxLen = 200
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case StatusAvailable, StatusOccupied, StatusOffline:
		return true
	default:
		return false
	}
}

// Location holds WGS84 coordinates.
type Location struct {
	Lat float64 `json:"lat" example:"52.37" minimum:"-90" maximum:"90"`
	Lon float64 `json:"lon" example:"4.89" minimum:"-180" maximum:"180"`
}

// Validate checks latitude/longitude ranges and rejects NaN/Inf.
func (l Location) Validate() error {
	if math.IsNaN(l.Lat) || math.IsInf(l.Lat, 0) {
		return fmt.Errorf("lat must be a finite number")
	}
	if math.IsNaN(l.Lon) || math.IsInf(l.Lon, 0) {
		return fmt.Errorf("lon must be a finite number")
	}
	if l.Lat < -90 || l.Lat > 90 {
		return fmt.Errorf("lat must be between -90 and 90")
	}
	if l.Lon < -180 || l.Lon > 180 {
		return fmt.Errorf("lon must be between -180 and 180")
	}
	return nil
}

// ValidateName checks a human-readable charge point name.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if utf8.RuneCountInString(name) > NameMaxLen {
		return fmt.Errorf("name must be at most %d characters", NameMaxLen)
	}
	return nil
}

// CreateRequest is the POST /chargepoints body (ID assigned server-side).
type CreateRequest struct {
	Name     string   `json:"name" example:"Dock 1"`
	Location Location `json:"location"`
	Status   Status   `json:"status" example:"AVAILABLE" enums:"AVAILABLE,OCCUPIED,OFFLINE"`
}

// ChargePoint is an EV charging station.
type ChargePoint struct {
	ID       string   `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Name     string   `json:"name" example:"Dock 1"`
	Location Location `json:"location"`
	Status   Status   `json:"status" example:"AVAILABLE" enums:"AVAILABLE,OCCUPIED,OFFLINE"`
}

// ErrorBody is the JSON error envelope.
type ErrorBody struct {
	Error string `json:"error" example:"charge point not found"`
}

package chargepoint_test

import (
	"math"
	"testing"

	"github.com/ankittk/chargepoints-api/pkg/chargepoint"
)

func TestStatusValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		s    chargepoint.Status
		want bool
	}{
		{chargepoint.StatusAvailable, true},
		{chargepoint.StatusOccupied, true},
		{chargepoint.StatusOffline, true},
		{chargepoint.Status("available"), false},
		{chargepoint.Status(""), false},
		{chargepoint.Status("BROKEN"), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.s), func(t *testing.T) {
			t.Parallel()
			if got := tt.s.Valid(); got != tt.want {
				t.Fatalf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLocationValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		loc     chargepoint.Location
		wantErr bool
	}{
		{name: "ok", loc: chargepoint.Location{Lat: 52.3, Lon: 4.9}},
		{name: "lat high", loc: chargepoint.Location{Lat: 91, Lon: 0}, wantErr: true},
		{name: "lat low", loc: chargepoint.Location{Lat: -91, Lon: 0}, wantErr: true},
		{name: "lon high", loc: chargepoint.Location{Lat: 0, Lon: 181}, wantErr: true},
		{name: "lon low", loc: chargepoint.Location{Lat: 0, Lon: -181}, wantErr: true},
		{name: "lat nan", loc: chargepoint.Location{Lat: math.NaN(), Lon: 0}, wantErr: true},
		{name: "lon inf", loc: chargepoint.Location{Lat: 0, Lon: math.Inf(1)}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.loc.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

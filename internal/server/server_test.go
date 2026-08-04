package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankittk/chargepoints-api/api"
	"github.com/ankittk/chargepoints-api/internal/server"
	"github.com/ankittk/chargepoints-api/internal/store"
	"github.com/ankittk/chargepoints-api/pkg/chargepoint"
)

func TestAPIIntegration(t *testing.T) {
	t.Parallel()
	h := newTestHandler(t)

	t.Run("create and get", func(t *testing.T) {
		body := `{"name":"Dock 1","location":{"lat":52.37,"lon":4.89},"status":"AVAILABLE"}`
		res := do(t, h, http.MethodPost, "/chargepoints", body)
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("status=%d body=%s", res.StatusCode, res.Body)
		}
		var cp chargepoint.ChargePoint
		if err := json.Unmarshal([]byte(res.Body), &cp); err != nil {
			t.Fatal(err)
		}
		if cp.ID == "" || cp.Name != "Dock 1" {
			t.Fatalf("unexpected: %+v", cp)
		}

		get := do(t, h, http.MethodGet, "/chargepoints/"+cp.ID, "")
		if get.StatusCode != http.StatusOK {
			t.Fatalf("get status=%d", get.StatusCode)
		}
	})

	t.Run("not found", func(t *testing.T) {
		res := do(t, h, http.MethodGet, "/chargepoints/00000000-0000-0000-0000-000000000000", "")
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("status=%d", res.StatusCode)
		}
		assertErrorJSON(t, res.Body)
	})

	t.Run("bad create", func(t *testing.T) {
		res := do(t, h, http.MethodPost, "/chargepoints", `{"name":"","location":{"lat":0,"lon":0},"status":"AVAILABLE"}`)
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("status=%d", res.StatusCode)
		}
		assertErrorJSON(t, res.Body)
	})

	t.Run("nearby", func(t *testing.T) {
		_ = do(t, h, http.MethodPost, "/chargepoints",
			`{"name":"Near","location":{"lat":52.3701,"lon":4.8901},"status":"OCCUPIED"}`)
		res := do(t, h, http.MethodGet, "/chargepoints/nearby?lat=52.37&lon=4.89&radius=1", "")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", res.StatusCode, res.Body)
		}
		var list []chargepoint.ChargePoint
		if err := json.Unmarshal([]byte(res.Body), &list); err != nil {
			t.Fatal(err)
		}
		if len(list) < 1 {
			t.Fatal("expected at least one nearby")
		}
	})

	t.Run("nearby bad radius", func(t *testing.T) {
		res := do(t, h, http.MethodGet, "/chargepoints/nearby?lat=52&lon=4&radius=0", "")
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("status=%d", res.StatusCode)
		}
	})

	t.Run("healthz", func(t *testing.T) {
		res := do(t, h, http.MethodGet, "/healthz", "")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", res.StatusCode)
		}
	})

	t.Run("openapi", func(t *testing.T) {
		res := do(t, h, http.MethodGet, "/openapi.yaml", "")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", res.StatusCode)
		}
		if len(res.Body) < 10 {
			t.Fatal("empty openapi")
		}
	})

	t.Run("readyz", func(t *testing.T) {
		res := do(t, h, http.MethodGet, "/readyz", "")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", res.StatusCode)
		}
	})

	t.Run("nan lat rejected", func(t *testing.T) {
		res := do(t, h, http.MethodPost, "/chargepoints",
			`{"name":"x","location":{"lat":"NaN","lon":0},"status":"AVAILABLE"}`)
		// JSON number NaN is invalid JSON → 400
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", res.StatusCode, res.Body)
		}
	})

	t.Run("nearby inf radius", func(t *testing.T) {
		res := do(t, h, http.MethodGet, "/chargepoints/nearby?lat=52&lon=4&radius=Inf", "")
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("status=%d", res.StatusCode)
		}
	})

	t.Run("body too large", func(t *testing.T) {
		big := `{"name":"` + strings.Repeat("a", 70<<10) + `","location":{"lat":0,"lon":0},"status":"AVAILABLE"}`
		res := do(t, h, http.MethodPost, "/chargepoints", big)
		if res.StatusCode != http.StatusRequestEntityTooLarge && res.StatusCode != http.StatusBadRequest {
			t.Fatalf("status=%d", res.StatusCode)
		}
	})

	t.Run("xff ignored without trust proxy", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set("X-Forwarded-For", "1.2.3.4")
		req.RemoteAddr = "10.0.0.1:1234"
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d", rr.Code)
		}
	})

	t.Run("cors preflight", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/chargepoints", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("status=%d", rr.Code)
		}
		if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatal("missing CORS header")
		}
	})
}

type response struct {
	StatusCode int
	Body       string
}

func do(t *testing.T, h http.Handler, method, path, body string) response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return response{StatusCode: rr.Code, Body: rr.Body.String()}
}

func assertErrorJSON(t *testing.T, body string) {
	t.Helper()
	var e chargepoint.ErrorBody
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatal(err)
	}
	if e.Error == "" {
		t.Fatal("empty error message")
	}
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := server.New(st, server.Config{
		OpenAPI:      api.OpenAPI,
		RateLimitRPS: 1000,
		RateBurst:    1000,
	})
	t.Cleanup(srv.Close)
	return srv.Handler()
}

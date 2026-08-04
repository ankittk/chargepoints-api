package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ankittk/chargepoints-api/internal/store"
	"github.com/ankittk/chargepoints-api/pkg/chargepoint"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/time/rate"
)

const (
	maxBodyBytes   = 64 << 10 // 64 KiB
	maxNearbyRadiusKm = 2000
	maxLimiterEntries = 10_000
)

// Config holds HTTP server settings.
type Config struct {
	OpenAPI      []byte
	RateLimitRPS float64
	RateBurst    int
	TrustProxy   bool
	CORSOrigin   string
	Logger       *slog.Logger
}

// Server is the HTTP API.
type Server struct {
	store      *store.Store
	mux        *http.ServeMux
	log        *slog.Logger
	openapi    []byte
	limiters   *ipLimiter
	trustProxy bool
	corsOrigin string
}

// New builds a Server with routes registered.
func New(st *store.Store, cfg Config) *Server {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	rps := cfg.RateLimitRPS
	if rps <= 0 {
		rps = 20
	}
	burst := cfg.RateBurst
	if burst <= 0 {
		burst = 40
	}
	cors := cfg.CORSOrigin
	if cors == "" {
		cors = "*"
	}
	s := &Server{
		store:      st,
		mux:        http.NewServeMux(),
		log:        log,
		openapi:    cfg.OpenAPI,
		limiters:   newIPLimiter(rps, burst, maxLimiterEntries),
		trustProxy: cfg.TrustProxy,
		corsOrigin: cors,
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("POST /chargepoints", s.handleCreate)
	s.mux.HandleFunc("GET /chargepoints/nearby", s.handleNearby)
	s.mux.HandleFunc("GET /chargepoints/{id}", s.handleGet)
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /readyz", s.handleReadyz)
	s.mux.HandleFunc("GET /openapi.yaml", s.handleOpenAPI)
	s.mux.HandleFunc("GET /docs", s.handleDocs)
	s.mux.HandleFunc("GET /docs/", s.handleDocs)
}

// Handler returns the middleware-wrapped mux.
// otelhttp is outermost so W3C traceparent is extracted/injected and child spans nest under the HTTP span.
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.mux
	h = s.withRateLimit(h)
	h = s.withCORS(h)
	h = s.withLogging(h)
	h = s.withRequestID(h)
	h = otelhttp.NewHandler(h, "chargepoints-api",
		otelhttp.WithFilter(func(r *http.Request) bool {
			switch r.URL.Path {
			case "/healthz", "/readyz":
				return false // skip probe noise
			default:
				return true
			}
		}),
	)
	return h
}

// Close stops background limiter cleanup.
func (s *Server) Close() {
	s.limiters.Close()
}

// handleHealthz godoc
//
//	@Summary		Liveness probe
//	@Description	Returns 200 when the process is up (does not check the database)
//	@Tags			ops
//	@Produce		plain
//	@Success		200	{string}	string	"ok"
//	@Router			/healthz [get]
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleReadyz godoc
//
//	@Summary		Readiness probe
//	@Description	Returns 200 when the database is reachable
//	@Tags			ops
//	@Produce		plain
//	@Success		200	{string}	string	"ok"
//	@Failure		503	{object}	chargepoint.ErrorBody
//	@Router			/readyz [get]
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		s.log.Error("readyz", "err", err, "request_id", requestID(r.Context()))
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(s.openapi)
}

func (s *Server) handleDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Charge Points API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
  <script>
    window.ui = SwaggerUIBundle({
      url: "/openapi.yaml",
      dom_id: "#swagger-ui",
      presets: [SwaggerUIBundle.presets.apis],
      layout: "BaseLayout"
    });
  </script>
</body>
</html>`)
}

// handleCreate godoc
//
//	@Summary		Create a charge point
//	@Description	Creates a charge point; server assigns a UUID (client id ignored)
//	@Tags			chargepoints
//	@Accept			json
//	@Produce		json
//	@Param			body	body		chargepoint.CreateRequest	true	"Charge point"
//	@Success		201		{object}	chargepoint.ChargePoint
//	@Failure		400		{object}	chargepoint.ErrorBody
//	@Failure		429		{object}	chargepoint.ErrorBody
//	@Failure		500		{object}	chargepoint.ErrorBody
//	@Router			/chargepoints [post]
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body chargepoint.CreateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if err := chargepoint.ValidateName(body.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := body.Location.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !body.Status.Valid() {
		writeError(w, http.StatusBadRequest, "status must be AVAILABLE, OCCUPIED, or OFFLINE")
		return
	}
	created, err := s.store.Create(r.Context(), chargepoint.ChargePoint{
		Name:     body.Name,
		Location: body.Location,
		Status:   body.Status,
	})
	if err != nil {
		s.log.Error("create", "err", err, "request_id", requestID(r.Context()))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// handleGet godoc
//
//	@Summary		Get a charge point by ID
//	@Tags			chargepoints
//	@Produce		json
//	@Param			id	path		string	true	"Charge point UUID"
//	@Success		200	{object}	chargepoint.ChargePoint
//	@Failure		404	{object}	chargepoint.ErrorBody
//	@Failure		429	{object}	chargepoint.ErrorBody
//	@Failure		500	{object}	chargepoint.ErrorBody
//	@Router			/chargepoints/{id} [get]
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	cp, err := s.store.GetByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "charge point not found")
		return
	}
	if err != nil {
		s.log.Error("get", "err", err, "request_id", requestID(r.Context()))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, cp)
}

// handleNearby godoc
//
//	@Summary		Find charge points within a radius
//	@Description	Returns charge points within radius kilometers of lat/lon
//	@Tags			chargepoints
//	@Produce		json
//	@Param			lat		query		number	true	"Center latitude"
//	@Param			lon		query		number	true	"Center longitude"
//	@Param			radius	query		number	true	"Search radius in kilometers"
//	@Success		200		{array}		chargepoint.ChargePoint
//	@Failure		400		{object}	chargepoint.ErrorBody
//	@Failure		429		{object}	chargepoint.ErrorBody
//	@Failure		500		{object}	chargepoint.ErrorBody
//	@Router			/chargepoints/nearby [get]
func (s *Server) handleNearby(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, err := strconv.ParseFloat(q.Get("lat"), 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "lat must be a number")
		return
	}
	lon, err := strconv.ParseFloat(q.Get("lon"), 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "lon must be a number")
		return
	}
	radius, err := strconv.ParseFloat(q.Get("radius"), 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "radius must be a number")
		return
	}
	if err := (chargepoint.Location{Lat: lat, Lon: lon}).Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if math.IsNaN(radius) || math.IsInf(radius, 0) || radius <= 0 {
		writeError(w, http.StatusBadRequest, "radius must be a finite number greater than 0")
		return
	}
	if radius > maxNearbyRadiusKm {
		writeError(w, http.StatusBadRequest, "radius must be at most "+strconv.Itoa(maxNearbyRadiusKm)+" km")
		return
	}
	list, err := s.store.Nearby(r.Context(), lat, lon, radius)
	if err != nil {
		s.log.Error("nearby", "err", err, "request_id", requestID(r.Context()))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, chargepoint.ErrorBody{Error: msg})
}

type ctxKey int

const requestIDKey ctxKey = 1

func requestID(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			// Prefer OTel trace id so logs and traces share one correlation key.
			if sc := trace.SpanFromContext(r.Context()).SpanContext(); sc.IsValid() {
				id = sc.TraceID().String()
			} else {
				id = newRequestID()
			}
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", requestID(r.Context()),
		}
		if sc := trace.SpanFromContext(r.Context()).SpanContext(); sc.IsValid() {
			attrs = append(attrs,
				"trace_id", sc.TraceID().String(),
				"span_id", sc.SpanID().String(),
			)
		}
		s.log.Info("request", attrs...)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap exposes the underlying ResponseWriter for http.ResponseController.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", s.corsOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r, s.trustProxy)
		if !s.limiters.Allow(ip) {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ipLimiter is a per-IP token bucket map with periodic eviction and a hard size cap.
type ipLimiter struct {
	mu         sync.Mutex
	rps        rate.Limit
	burst      int
	maxEntries int
	entries    map[string]*limiterEntry
	done       chan struct{}
}

type limiterEntry struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

func newIPLimiter(rps float64, burst, maxEntries int) *ipLimiter {
	l := &ipLimiter{
		rps:        rate.Limit(rps),
		burst:      burst,
		maxEntries: maxEntries,
		entries:    make(map[string]*limiterEntry),
		done:       make(chan struct{}),
	}
	go l.evictLoop()
	return l
}

func (l *ipLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ip]
	if !ok {
		if len(l.entries) >= l.maxEntries {
			l.evictLocked()
			if len(l.entries) >= l.maxEntries {
				return false
			}
		}
		e = &limiterEntry{lim: rate.NewLimiter(l.rps, l.burst)}
		l.entries[ip] = e
	}
	e.lastSeen = time.Now()
	return e.lim.Allow()
}

func (l *ipLimiter) evictLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-t.C:
			l.mu.Lock()
			l.evictLocked()
			l.mu.Unlock()
		}
	}
}

func (l *ipLimiter) evictLocked() {
	cutoff := time.Now().Add(-10 * time.Minute)
	for ip, e := range l.entries {
		if e.lastSeen.Before(cutoff) {
			delete(l.entries, ip)
		}
	}
}

func (l *ipLimiter) Close() {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
}

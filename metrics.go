package telemetry

import (
	"context"
	"database/sql"
	"math"
	"net/http"
	"runtime/metrics"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/semconv/v1.41.0/dbconv"
	"go.opentelemetry.io/otel/semconv/v1.41.0/goconv"
	"go.opentelemetry.io/otel/semconv/v1.41.0/httpconv"
	"go.opentelemetry.io/otel/semconv/v1.41.0/rpcconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ScopeName is the instrumentation scope used by the metric bundles
const ScopeName string = "github.com/nxdir-s/telemetry"

const (
	goroutinesKey        string = "/sched/goroutines:goroutines"
	gomaxprocsKey        string = "/sched/gomaxprocs:threads"
	gogcKey              string = "/gc/gogc:percent"
	gomemlimitKey        string = "/gc/gomemlimit:bytes"
	heapAllocsBytesKey   string = "/gc/heap/allocs:bytes"
	heapAllocsObjectsKey string = "/gc/heap/allocs:objects"
	heapGoalKey          string = "/gc/heap/goal:bytes"
	memTotalKey          string = "/memory/classes/total:bytes"
	memReleasedKey       string = "/memory/classes/heap/released:bytes"
	memStacksKey         string = "/memory/classes/heap/stacks:bytes"
)

type ErrInstrument struct {
	err error
}

func (e *ErrInstrument) Error() string {
	return "failed to create otel instrument: " + e.err.Error()
}

func (e *ErrInstrument) Unwrap() error {
	return e.err
}

type ErrRegisterCallback struct {
	err error
}

func (e *ErrRegisterCallback) Error() string {
	return "failed to register metric callback: " + e.err.Error()
}

func (e *ErrRegisterCallback) Unwrap() error {
	return e.err
}

// NewMeter returns a named meter from the global meter provider
func NewMeter(name string, opts ...metric.MeterOption) metric.Meter {
	return otel.Meter(name, opts...)
}

// WithDescription sets the instrument description
func WithDescription(desc string) metric.InstrumentOption {
	return metric.WithDescription(desc)
}

// WithUnit sets the instrument unit
func WithUnit(unit string) metric.InstrumentOption {
	return metric.WithUnit(unit)
}

// WithExplicitBucketBoundaries sets histogram bucket boundaries
func WithExplicitBucketBoundaries(bounds ...float64) metric.HistogramOption {
	return metric.WithExplicitBucketBoundaries(bounds...)
}

// WithInt64Callback adds a callback to an int64 observable instrument
func WithInt64Callback(callback metric.Int64Callback) metric.Int64ObservableOption {
	return metric.WithInt64Callback(callback)
}

// WithFloat64Callback adds a callback to a float64 observable instrument
func WithFloat64Callback(callback metric.Float64Callback) metric.Float64ObservableOption {
	return metric.WithFloat64Callback(callback)
}

type requestConfig struct {
	attrs []attribute.KeyValue
}

type RequestOption func(*requestConfig)

// WithMethod sets the http.request.method attribute
func WithMethod(method string) RequestOption {
	return func(cfg *requestConfig) {
		cfg.attrs = append(cfg.attrs, requestMethodAttr(method))
	}
}

// WithRoute sets the http.route attribute
func WithRoute(route string) RequestOption {
	return func(cfg *requestConfig) {
		cfg.attrs = append(cfg.attrs, semconv.HTTPRoute(route))
	}
}

// WithStatusCode sets the http.response.status_code attribute
func WithStatusCode(code int) RequestOption {
	return func(cfg *requestConfig) {
		cfg.attrs = append(cfg.attrs, semconv.HTTPResponseStatusCode(code))
	}
}

// WithScheme sets the url.scheme attribute
func WithScheme(scheme string) RequestOption {
	return func(cfg *requestConfig) {
		cfg.attrs = append(cfg.attrs, semconv.URLScheme(scheme))
	}
}

// WithAttributes adds custom attributes to a recorded request
func WithAttributes(attrs ...attribute.KeyValue) RequestOption {
	return func(cfg *requestConfig) {
		cfg.attrs = append(cfg.attrs, attrs...)
	}
}

// requestMethodAttr maps an http method to its semconv attribute
func requestMethodAttr(method string) attribute.KeyValue {
	switch method {
	case http.MethodConnect, http.MethodDelete, http.MethodGet,
		http.MethodHead, http.MethodOptions, http.MethodPatch,
		http.MethodPost, http.MethodPut, http.MethodTrace:
		return semconv.HTTPRequestMethodKey.String(method)
	default:
		return semconv.HTTPRequestMethodOther
	}
}

// RequestMetrics records http server metrics
type RequestMetrics struct {
	duration     httpconv.ServerRequestDuration
	active       httpconv.ServerActiveRequests
	requestSize  httpconv.ServerRequestBodySize
	responseSize httpconv.ServerResponseBodySize
}

// NewRequestMetrics creates http server metrics using the global meter provider
func NewRequestMetrics() (*RequestMetrics, error) {
	meter := NewMeter(ScopeName)

	duration, err := httpconv.NewServerRequestDuration(meter)
	if err != nil {
		return nil, &ErrInstrument{err}
	}

	active, err := httpconv.NewServerActiveRequests(meter)
	if err != nil {
		return nil, &ErrInstrument{err}
	}

	requestSize, err := httpconv.NewServerRequestBodySize(meter)
	if err != nil {
		return nil, &ErrInstrument{err}
	}

	responseSize, err := httpconv.NewServerResponseBodySize(meter)
	if err != nil {
		return nil, &ErrInstrument{err}
	}

	return &RequestMetrics{
		duration:     duration,
		active:       active,
		requestSize:  requestSize,
		responseSize: responseSize,
	}, nil
}

// Handler wraps an http handler and records request metrics
func (m *RequestMetrics) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		entryAttrs := metric.WithAttributeSet(attribute.NewSet(
			requestMethodAttr(r.Method),
			semconv.URLScheme(scheme),
		))

		m.active.Inst().Add(r.Context(), 1, entryAttrs)

		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		m.active.Inst().Add(r.Context(), -1, entryAttrs)

		attrs := []attribute.KeyValue{
			requestMethodAttr(r.Method),
			semconv.URLScheme(scheme),
			semconv.HTTPResponseStatusCode(rw.status),
		}

		if r.Pattern != "" {
			attrs = append(attrs, semconv.HTTPRoute(r.Pattern))
		}

		exitAttrs := metric.WithAttributeSet(attribute.NewSet(attrs...))

		m.duration.Inst().Record(r.Context(), time.Since(start).Seconds(), exitAttrs)

		if r.ContentLength >= 0 {
			m.requestSize.Inst().Record(r.Context(), r.ContentLength, exitAttrs)
		}

		m.responseSize.Inst().Record(r.Context(), rw.written, exitAttrs)
	})
}

// Record records a request duration with the supplied attributes
func (m *RequestMetrics) Record(ctx context.Context, duration time.Duration, opts ...RequestOption) {
	var cfg requestConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	m.duration.Inst().Record(ctx, duration.Seconds(),
		metric.WithAttributeSet(attribute.NewSet(cfg.attrs...)))
}

// responseWriter captures the status code and bytes written by a handler.
// Flush supports direct http.Flusher assertions and Unwrap supports
// http.ResponseController; other optional interfaces are deliberately not
// implemented to avoid advertising capabilities the underlying writer may lack
type responseWriter struct {
	http.ResponseWriter
	status      int
	written     int64
	wroteHeader bool
}

func (w *responseWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status = status
		w.wroteHeader = true
	}

	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.written += int64(n)

	return n, err
}

func (w *responseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *responseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// GoRoutineMetrics observes go runtime metrics; call once after InitProviders
func GoRoutineMetrics() error {
	meter := NewMeter(ScopeName)

	goroutines, err := goconv.NewGoroutineCount(meter)
	if err != nil {
		return &ErrInstrument{err}
	}

	memoryUsed, err := goconv.NewMemoryUsed(meter)
	if err != nil {
		return &ErrInstrument{err}
	}

	memoryLimit, err := goconv.NewMemoryLimit(meter)
	if err != nil {
		return &ErrInstrument{err}
	}

	memoryAllocated, err := goconv.NewMemoryAllocated(meter)
	if err != nil {
		return &ErrInstrument{err}
	}

	memoryAllocations, err := goconv.NewMemoryAllocations(meter)
	if err != nil {
		return &ErrInstrument{err}
	}

	gcGoal, err := goconv.NewMemoryGCGoal(meter)
	if err != nil {
		return &ErrInstrument{err}
	}

	gogc, err := goconv.NewConfigGogc(meter)
	if err != nil {
		return &ErrInstrument{err}
	}

	processorLimit, err := goconv.NewProcessorLimit(meter)
	if err != nil {
		return &ErrInstrument{err}
	}

	stackAttrs := metric.WithAttributeSet(attribute.NewSet(
		memoryUsed.AttrMemoryType(goconv.MemoryTypeStack),
	))
	otherAttrs := metric.WithAttributeSet(attribute.NewSet(
		memoryUsed.AttrMemoryType(goconv.MemoryTypeOther),
	))

	_, err = meter.RegisterCallback(
		func(ctx context.Context, o metric.Observer) error {
			samples := []metrics.Sample{
				{Name: goroutinesKey},
				{Name: memStacksKey},
				{Name: memTotalKey},
				{Name: memReleasedKey},
				{Name: gomemlimitKey},
				{Name: heapAllocsBytesKey},
				{Name: heapAllocsObjectsKey},
				{Name: heapGoalKey},
				{Name: gogcKey},
				{Name: gomaxprocsKey},
			}
			metrics.Read(samples)

			o.ObserveInt64(goroutines.Inst(), int64(samples[0].Value.Uint64()))

			stacks := samples[1].Value.Uint64()
			total := samples[2].Value.Uint64()
			released := samples[3].Value.Uint64()

			o.ObserveInt64(memoryUsed.Inst(), int64(stacks), stackAttrs)
			o.ObserveInt64(memoryUsed.Inst(), int64(total-released-stacks), otherAttrs)

			// gomemlimit reports math.MaxInt64 when no limit is set
			if limit := samples[4].Value.Uint64(); limit != math.MaxInt64 {
				o.ObserveInt64(memoryLimit.Inst(), int64(limit))
			}

			o.ObserveInt64(memoryAllocated.Inst(), int64(samples[5].Value.Uint64()))
			o.ObserveInt64(memoryAllocations.Inst(), int64(samples[6].Value.Uint64()))
			o.ObserveInt64(gcGoal.Inst(), int64(samples[7].Value.Uint64()))
			o.ObserveInt64(gogc.Inst(), int64(samples[8].Value.Uint64()))
			o.ObserveInt64(processorLimit.Inst(), int64(samples[9].Value.Uint64()))

			return nil
		},
		goroutines.Inst(),
		memoryUsed.Inst(),
		memoryLimit.Inst(),
		memoryAllocated.Inst(),
		memoryAllocations.Inst(),
		gcGoal.Inst(),
		gogc.Inst(),
		processorLimit.Inst(),
	)
	if err != nil {
		return &ErrRegisterCallback{err}
	}

	return nil
}

// DatabaseMetrics observes connection pool metrics for a database; call once per pool after InitProviders
func DatabaseMetrics(db *sql.DB, poolName string) error {
	meter := NewMeter(ScopeName)

	connCount, err := meter.Int64ObservableUpDownCounter(
		dbconv.ClientConnectionCount{}.Name(),
		metric.WithDescription(dbconv.ClientConnectionCount{}.Description()),
		metric.WithUnit(dbconv.ClientConnectionCount{}.Unit()),
	)
	if err != nil {
		return &ErrInstrument{err}
	}

	connMax, err := meter.Int64ObservableUpDownCounter(
		dbconv.ClientConnectionMax{}.Name(),
		metric.WithDescription(dbconv.ClientConnectionMax{}.Description()),
		metric.WithUnit(dbconv.ClientConnectionMax{}.Unit()),
	)
	if err != nil {
		return &ErrInstrument{err}
	}

	// semconv defines wait_time as a histogram, but sql.DBStats only exposes
	// the cumulative total, so it is observed as a cumulative counter
	waitTime, err := meter.Float64ObservableCounter(
		dbconv.ClientConnectionWaitTime{}.Name(),
		metric.WithDescription(dbconv.ClientConnectionWaitTime{}.Description()),
		metric.WithUnit(dbconv.ClientConnectionWaitTime{}.Unit()),
	)
	if err != nil {
		return &ErrInstrument{err}
	}

	waits, err := meter.Int64ObservableCounter(
		"db.client.connection.waits",
		metric.WithDescription("The cumulative number of times a connection had to wait"),
		metric.WithUnit("{wait}"),
	)
	if err != nil {
		return &ErrInstrument{err}
	}

	closed, err := meter.Int64ObservableCounter(
		"db.client.connection.closed",
		metric.WithDescription("The cumulative number of connections closed by pool limits"),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		return &ErrInstrument{err}
	}

	pool := semconv.DBClientConnectionPoolName(poolName)
	closeReason := attribute.Key("db.client.connection.close.reason")

	poolAttrs := metric.WithAttributeSet(attribute.NewSet(pool))
	idleAttrs := metric.WithAttributeSet(attribute.NewSet(pool, semconv.DBClientConnectionStateIdle))
	usedAttrs := metric.WithAttributeSet(attribute.NewSet(pool, semconv.DBClientConnectionStateUsed))
	idleMaxAttrs := metric.WithAttributeSet(attribute.NewSet(pool, closeReason.String("idle_max")))
	idleTimeAttrs := metric.WithAttributeSet(attribute.NewSet(pool, closeReason.String("idle_time")))
	lifetimeAttrs := metric.WithAttributeSet(attribute.NewSet(pool, closeReason.String("lifetime")))

	_, err = meter.RegisterCallback(
		func(ctx context.Context, o metric.Observer) error {
			stats := db.Stats()

			o.ObserveInt64(connCount, int64(stats.Idle), idleAttrs)
			o.ObserveInt64(connCount, int64(stats.InUse), usedAttrs)
			o.ObserveInt64(connMax, int64(stats.MaxOpenConnections), poolAttrs)
			o.ObserveFloat64(waitTime, stats.WaitDuration.Seconds(), poolAttrs)
			o.ObserveInt64(waits, stats.WaitCount, poolAttrs)
			o.ObserveInt64(closed, stats.MaxIdleClosed, idleMaxAttrs)
			o.ObserveInt64(closed, stats.MaxIdleTimeClosed, idleTimeAttrs)
			o.ObserveInt64(closed, stats.MaxLifetimeClosed, lifetimeAttrs)

			return nil
		},
		connCount, connMax, waitTime, waits, closed,
	)
	if err != nil {
		return &ErrRegisterCallback{err}
	}

	return nil
}

// GrpcMetrics records grpc server metrics
type GrpcMetrics struct {
	duration rpcconv.ServerCallDuration
	active   metric.Int64UpDownCounter
}

// NewGrpcMetrics creates grpc server metrics using the global meter provider
func NewGrpcMetrics() (*GrpcMetrics, error) {
	meter := NewMeter(ScopeName)

	duration, err := rpcconv.NewServerCallDuration(meter)
	if err != nil {
		return nil, &ErrInstrument{err}
	}

	active, err := meter.Int64UpDownCounter(
		"rpc.server.active_requests",
		metric.WithDescription("Number of in-flight rpc server requests"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, &ErrInstrument{err}
	}

	return &GrpcMetrics{
		duration: duration,
		active:   active,
	}, nil
}

// UnaryServerInterceptor records metrics for unary rpc calls
func (m *GrpcMetrics) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()

		entryAttrs := metric.WithAttributeSet(attribute.NewSet(rpcAttrs(info.FullMethod)...))

		m.active.Add(ctx, 1, entryAttrs)

		resp, err := handler(ctx, req)

		m.active.Add(ctx, -1, entryAttrs)

		attrs := append(rpcAttrs(info.FullMethod),
			semconv.RPCResponseStatusCode(statusCodeString(status.Code(err))))

		m.duration.Inst().Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributeSet(attribute.NewSet(attrs...)))

		return resp, err
	}
}

// StreamServerInterceptor records metrics for streaming rpc calls
func (m *GrpcMetrics) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		ctx := ss.Context()

		entryAttrs := metric.WithAttributeSet(attribute.NewSet(rpcAttrs(info.FullMethod)...))

		m.active.Add(ctx, 1, entryAttrs)

		err := handler(srv, ss)

		m.active.Add(ctx, -1, entryAttrs)

		attrs := append(rpcAttrs(info.FullMethod),
			semconv.RPCResponseStatusCode(statusCodeString(status.Code(err))))

		m.duration.Inst().Record(ctx, time.Since(start).Seconds(),
			metric.WithAttributeSet(attribute.NewSet(attrs...)))

		return err
	}
}

// rpcAttrs builds the shared rpc attributes
func rpcAttrs(fullMethod string) []attribute.KeyValue {
	return []attribute.KeyValue{
		semconv.RPCSystemNameKey.String(string(rpcconv.SystemNameGRPC)),
		semconv.RPCMethod(strings.TrimPrefix(fullMethod, "/")),
	}
}

// statusCodeString converts a grpc code to its canonical semconv value
func statusCodeString(code codes.Code) string {
	switch code {
	case codes.OK:
		return "OK"
	case codes.Canceled:
		return "CANCELLED"
	case codes.Unknown:
		return "UNKNOWN"
	case codes.InvalidArgument:
		return "INVALID_ARGUMENT"
	case codes.DeadlineExceeded:
		return "DEADLINE_EXCEEDED"
	case codes.NotFound:
		return "NOT_FOUND"
	case codes.AlreadyExists:
		return "ALREADY_EXISTS"
	case codes.PermissionDenied:
		return "PERMISSION_DENIED"
	case codes.ResourceExhausted:
		return "RESOURCE_EXHAUSTED"
	case codes.FailedPrecondition:
		return "FAILED_PRECONDITION"
	case codes.Aborted:
		return "ABORTED"
	case codes.OutOfRange:
		return "OUT_OF_RANGE"
	case codes.Unimplemented:
		return "UNIMPLEMENTED"
	case codes.Internal:
		return "INTERNAL"
	case codes.Unavailable:
		return "UNAVAILABLE"
	case codes.DataLoss:
		return "DATA_LOSS"
	case codes.Unauthenticated:
		return "UNAUTHENTICATED"
	default:
		return strconv.Itoa(int(code))
	}
}

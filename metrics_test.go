package telemetry

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// setTestProvider swaps the global meter provider for a manual reader backed one
func setTestProvider(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	prev := otel.GetMeterProvider()

	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
	})

	return reader
}

const TestMeterName string = "testmeter"

// newTestMeter creates a meter backed by a manual reader
func newTestMeter(t *testing.T) (metric.Meter, *sdkmetric.ManualReader) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	return provider.Meter(TestMeterName), reader
}

// findMetric searches collected metrics by name
func findMetric(rm metricdata.ResourceMetrics, name string) (metricdata.Metrics, bool) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}

	return metricdata.Metrics{}, false
}

// findDataPoint searches datapoints by attribute
func findDataPoint[N int64 | float64](dps []metricdata.DataPoint[N], attr attribute.KeyValue) (metricdata.DataPoint[N], bool) {
	for _, dp := range dps {
		if value, ok := dp.Attributes.Value(attr.Key); ok && value.Emit() == attr.Value.Emit() {
			return dp, true
		}
	}

	return metricdata.DataPoint[N]{}, false
}

func TestNewMeter(t *testing.T) {
	cases := []struct {
		name string
		opts []metric.MeterOption
	}{
		{
			name: TestMeterName,
			opts: []metric.MeterOption{},
		},
		{
			name: TestMeterName,
			opts: []metric.MeterOption{
				metric.WithInstrumentationVersion("v1.0.0"),
			},
		},
	}

	for i, tt := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			meter := NewMeter(tt.name, tt.opts...)

			assert.NotNil(t, meter)
		})
	}
}

func TestNewMeterWithProviders(t *testing.T) {
	ctx := t.Context()

	cleanup, err := InitProviders(ctx, &Config{
		ServiceName:  TestServiceName,
		OtelEndpoint: TestEndpoint,
		TlsConfig:    &tls.Config{},
	})
	assert.Nil(t, err)
	defer cleanup()

	meter := NewMeter(TestMeterName)
	assert.NotNil(t, meter)

	counter, err := meter.Int64Counter("test.requests")
	assert.Nil(t, err)

	counter.Add(ctx, 1)
}

func TestInstrumentOptions(t *testing.T) {
	ctx := t.Context()
	meter, reader := newTestMeter(t)

	counter, err := meter.Int64Counter("test.requests",
		WithDescription("processed requests"),
		WithUnit("{request}"),
	)
	assert.Nil(t, err)

	counter.Add(ctx, 5)

	histogram, err := meter.Float64Histogram("test.latency",
		WithUnit("s"),
		WithExplicitBucketBoundaries(1, 5, 10),
	)
	assert.Nil(t, err)

	histogram.Record(ctx, 2.5)

	_, err = meter.Int64ObservableGauge("test.pool.size",
		WithInt64Callback(func(ctx context.Context, o metric.Int64Observer) error {
			o.Observe(42)
			return nil
		}),
	)
	assert.Nil(t, err)

	_, err = meter.Float64ObservableGauge("test.utilization",
		WithFloat64Callback(func(ctx context.Context, o metric.Float64Observer) error {
			o.Observe(0.5)
			return nil
		}),
	)
	assert.Nil(t, err)

	var rm metricdata.ResourceMetrics
	assert.Nil(t, reader.Collect(ctx, &rm))

	counterMetric, found := findMetric(rm, "test.requests")
	assert.True(t, found)
	assert.Equal(t, "processed requests", counterMetric.Description)
	assert.Equal(t, "{request}", counterMetric.Unit)

	sum, ok := counterMetric.Data.(metricdata.Sum[int64])
	assert.True(t, ok)
	assert.Equal(t, int64(5), sum.DataPoints[0].Value)

	histogramMetric, found := findMetric(rm, "test.latency")
	assert.True(t, found)
	assert.Equal(t, "s", histogramMetric.Unit)

	hist, ok := histogramMetric.Data.(metricdata.Histogram[float64])
	assert.True(t, ok)
	assert.Equal(t, []float64{1, 5, 10}, hist.DataPoints[0].Bounds)

	gaugeMetric, found := findMetric(rm, "test.pool.size")
	assert.True(t, found)

	gauge, ok := gaugeMetric.Data.(metricdata.Gauge[int64])
	assert.True(t, ok)
	assert.Equal(t, int64(42), gauge.DataPoints[0].Value)

	utilizationMetric, found := findMetric(rm, "test.utilization")
	assert.True(t, found)

	utilization, ok := utilizationMetric.Data.(metricdata.Gauge[float64])
	assert.True(t, ok)
	assert.Equal(t, 0.5, utilization.DataPoints[0].Value)
}

func TestRequestMethodAttr(t *testing.T) {
	cases := []struct {
		method        string
		expectedValue string
	}{
		{
			method:        http.MethodGet,
			expectedValue: "GET",
		},
		{
			method:        http.MethodPatch,
			expectedValue: "PATCH",
		},
		{
			method:        "weird",
			expectedValue: "_OTHER",
		},
	}

	for i, tt := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			attr := requestMethodAttr(tt.method)

			assert.Equal(t, semconv.HTTPRequestMethodKey, attr.Key)
			assert.Equal(t, tt.expectedValue, attr.Value.AsString())
		})
	}
}

func TestRequestMetricsHandler(t *testing.T) {
	cases := []struct {
		method        string
		target        string
		pattern       string
		status        int
		body          string
		expectedRoute string
	}{
		{
			method:        http.MethodGet,
			target:        "/orders/42",
			pattern:       "GET /orders/{id}",
			status:        http.StatusOK,
			body:          "order",
			expectedRoute: "GET /orders/{id}",
		},
		{
			method:        http.MethodPost,
			target:        "/orders",
			pattern:       "POST /orders",
			status:        http.StatusInternalServerError,
			body:          "error",
			expectedRoute: "POST /orders",
		},
		{
			method:        http.MethodGet,
			target:        "/plain",
			pattern:       "",
			status:        http.StatusOK,
			body:          "ok",
			expectedRoute: "",
		},
	}

	for i, tt := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			ctx := t.Context()
			reader := setTestProvider(t)

			reqMetrics, err := NewRequestMetrics()
			assert.Nil(t, err)

			handleFunc := func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}

			var next http.Handler = http.HandlerFunc(handleFunc)
			if tt.pattern != "" {
				mux := http.NewServeMux()
				mux.HandleFunc(tt.pattern, handleFunc)
				next = mux
			}

			request := httptest.NewRequest(tt.method, tt.target, strings.NewReader("payload"))
			reqMetrics.Handler(next).ServeHTTP(httptest.NewRecorder(), request)

			var rm metricdata.ResourceMetrics
			assert.Nil(t, reader.Collect(ctx, &rm))

			durationMetric, found := findMetric(rm, "http.server.request.duration")
			assert.True(t, found)
			assert.Equal(t, "s", durationMetric.Unit)

			duration, ok := durationMetric.Data.(metricdata.Histogram[float64])
			assert.True(t, ok)
			assert.Equal(t, uint64(1), duration.DataPoints[0].Count)
			assert.Equal(t, []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}, duration.DataPoints[0].Bounds)

			attrs := duration.DataPoints[0].Attributes

			method, ok := attrs.Value(semconv.HTTPRequestMethodKey)
			assert.True(t, ok)
			assert.Equal(t, tt.method, method.AsString())

			scheme, ok := attrs.Value(semconv.URLSchemeKey)
			assert.True(t, ok)
			assert.Equal(t, "http", scheme.AsString())

			status, ok := attrs.Value(semconv.HTTPResponseStatusCodeKey)
			assert.True(t, ok)
			assert.Equal(t, int64(tt.status), status.AsInt64())

			route, ok := attrs.Value(semconv.HTTPRouteKey)
			assert.Equal(t, tt.expectedRoute != "", ok)
			if tt.expectedRoute != "" {
				assert.Equal(t, tt.expectedRoute, route.AsString())
			}

			activeMetric, found := findMetric(rm, "http.server.active_requests")
			assert.True(t, found)

			active, ok := activeMetric.Data.(metricdata.Sum[int64])
			assert.True(t, ok)
			assert.Equal(t, int64(0), active.DataPoints[0].Value)

			requestSizeMetric, found := findMetric(rm, "http.server.request.body.size")
			assert.True(t, found)

			requestSize, ok := requestSizeMetric.Data.(metricdata.Histogram[int64])
			assert.True(t, ok)
			assert.Equal(t, int64(len("payload")), requestSize.DataPoints[0].Sum)

			responseSizeMetric, found := findMetric(rm, "http.server.response.body.size")
			assert.True(t, found)

			responseSize, ok := responseSizeMetric.Data.(metricdata.Histogram[int64])
			assert.True(t, ok)
			assert.Equal(t, int64(len(tt.body)), responseSize.DataPoints[0].Sum)
		})
	}
}

func TestRequestMetricsRecord(t *testing.T) {
	ctx := t.Context()
	reader := setTestProvider(t)

	reqMetrics, err := NewRequestMetrics()
	assert.Nil(t, err)

	reqMetrics.Record(ctx, 250*time.Millisecond,
		WithMethod(http.MethodGet),
		WithRoute("/orders"),
		WithStatusCode(http.StatusOK),
		WithScheme("https"),
		WithAttributes(attribute.String("tenant", "acme")),
	)

	var rm metricdata.ResourceMetrics
	assert.Nil(t, reader.Collect(ctx, &rm))

	durationMetric, found := findMetric(rm, "http.server.request.duration")
	assert.True(t, found)

	duration, ok := durationMetric.Data.(metricdata.Histogram[float64])
	assert.True(t, ok)
	assert.Equal(t, uint64(1), duration.DataPoints[0].Count)
	assert.Equal(t, 0.25, duration.DataPoints[0].Sum)

	attrs := duration.DataPoints[0].Attributes

	method, ok := attrs.Value(semconv.HTTPRequestMethodKey)
	assert.True(t, ok)
	assert.Equal(t, "GET", method.AsString())

	route, ok := attrs.Value(semconv.HTTPRouteKey)
	assert.True(t, ok)
	assert.Equal(t, "/orders", route.AsString())

	status, ok := attrs.Value(semconv.HTTPResponseStatusCodeKey)
	assert.True(t, ok)
	assert.Equal(t, int64(http.StatusOK), status.AsInt64())

	scheme, ok := attrs.Value(semconv.URLSchemeKey)
	assert.True(t, ok)
	assert.Equal(t, "https", scheme.AsString())

	tenant, ok := attrs.Value(attribute.Key("tenant"))
	assert.True(t, ok)
	assert.Equal(t, "acme", tenant.AsString())
}

func TestGoRoutineMetrics(t *testing.T) {
	ctx := t.Context()
	reader := setTestProvider(t)

	assert.Nil(t, GoRoutineMetrics())

	var rm metricdata.ResourceMetrics
	assert.Nil(t, reader.Collect(ctx, &rm))

	goroutineMetric, found := findMetric(rm, "go.goroutine.count")
	assert.True(t, found)

	goroutines, ok := goroutineMetric.Data.(metricdata.Sum[int64])
	assert.True(t, ok)
	assert.Greater(t, goroutines.DataPoints[0].Value, int64(0))

	memoryUsedMetric, found := findMetric(rm, "go.memory.used")
	assert.True(t, found)

	memoryUsed, ok := memoryUsedMetric.Data.(metricdata.Sum[int64])
	assert.True(t, ok)
	assert.Len(t, memoryUsed.DataPoints, 2)

	memoryType := attribute.Key("go.memory.type")

	stack, found := findDataPoint(memoryUsed.DataPoints, memoryType.String("stack"))
	assert.True(t, found)
	assert.Greater(t, stack.Value, int64(0))

	other, found := findDataPoint(memoryUsed.DataPoints, memoryType.String("other"))
	assert.True(t, found)
	assert.Greater(t, other.Value, int64(0))

	allocatedMetric, found := findMetric(rm, "go.memory.allocated")
	assert.True(t, found)

	allocated, ok := allocatedMetric.Data.(metricdata.Sum[int64])
	assert.True(t, ok)
	assert.Greater(t, allocated.DataPoints[0].Value, int64(0))

	processorMetric, found := findMetric(rm, "go.processor.limit")
	assert.True(t, found)

	processors, ok := processorMetric.Data.(metricdata.Sum[int64])
	assert.True(t, ok)
	assert.Greater(t, processors.DataPoints[0].Value, int64(0))
}

type stubDriver struct{}

func (d *stubDriver) Open(name string) (driver.Conn, error) {
	return &stubConn{}, nil
}

type stubConnector struct{}

func (c *stubConnector) Connect(ctx context.Context) (driver.Conn, error) {
	return &stubConn{}, nil
}

func (c *stubConnector) Driver() driver.Driver {
	return &stubDriver{}
}

type stubConn struct{}

func (c *stubConn) Prepare(query string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (c *stubConn) Close() error {
	return nil
}

func (c *stubConn) Begin() (driver.Tx, error) {
	return nil, driver.ErrSkip
}

func TestDatabaseMetrics(t *testing.T) {
	ctx := t.Context()
	reader := setTestProvider(t)

	db := sql.OpenDB(&stubConnector{})
	defer db.Close()

	db.SetMaxOpenConns(10)

	conn, err := db.Conn(ctx)
	assert.Nil(t, err)

	assert.Nil(t, DatabaseMetrics(db, "orders"))

	var rm metricdata.ResourceMetrics
	assert.Nil(t, reader.Collect(ctx, &rm))

	pool := attribute.Key("db.client.connection.pool.name").String("orders")
	state := attribute.Key("db.client.connection.state")

	countMetric, found := findMetric(rm, "db.client.connection.count")
	assert.True(t, found)

	count, ok := countMetric.Data.(metricdata.Sum[int64])
	assert.True(t, ok)

	used, found := findDataPoint(count.DataPoints, state.String("used"))
	assert.True(t, found)
	assert.Equal(t, int64(1), used.Value)

	_, found = findDataPoint(count.DataPoints, pool)
	assert.True(t, found)

	maxMetric, found := findMetric(rm, "db.client.connection.max")
	assert.True(t, found)

	max, ok := maxMetric.Data.(metricdata.Sum[int64])
	assert.True(t, ok)
	assert.Equal(t, int64(10), max.DataPoints[0].Value)

	_, found = findMetric(rm, "db.client.connection.wait_time")
	assert.True(t, found)

	_, found = findMetric(rm, "db.client.connection.waits")
	assert.True(t, found)

	_, found = findMetric(rm, "db.client.connection.closed")
	assert.True(t, found)

	assert.Nil(t, conn.Close())

	var released metricdata.ResourceMetrics
	assert.Nil(t, reader.Collect(ctx, &released))

	countMetric, found = findMetric(released, "db.client.connection.count")
	assert.True(t, found)

	count, ok = countMetric.Data.(metricdata.Sum[int64])
	assert.True(t, ok)

	idle, found := findDataPoint(count.DataPoints, state.String("idle"))
	assert.True(t, found)
	assert.Equal(t, int64(1), idle.Value)

	used, found = findDataPoint(count.DataPoints, state.String("used"))
	assert.True(t, found)
	assert.Equal(t, int64(0), used.Value)
}

func TestGrpcUnaryInterceptor(t *testing.T) {
	cases := []struct {
		handlerErr     error
		expectedStatus string
	}{
		{
			handlerErr:     nil,
			expectedStatus: "OK",
		},
		{
			handlerErr:     status.Error(codes.NotFound, "missing"),
			expectedStatus: "NOT_FOUND",
		},
	}

	for i, tt := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			ctx := t.Context()
			reader := setTestProvider(t)

			grpcMetrics, err := NewGrpcMetrics()
			assert.Nil(t, err)

			handler := func(ctx context.Context, req any) (any, error) {
				return "response", tt.handlerErr
			}

			interceptor := grpcMetrics.UnaryServerInterceptor()
			resp, err := interceptor(ctx, "request", &grpc.UnaryServerInfo{FullMethod: "/test.Service/Do"}, handler)

			assert.Equal(t, tt.handlerErr, err)
			assert.Equal(t, "response", resp)

			var rm metricdata.ResourceMetrics
			assert.Nil(t, reader.Collect(ctx, &rm))

			durationMetric, found := findMetric(rm, "rpc.server.call.duration")
			assert.True(t, found)
			assert.Equal(t, "s", durationMetric.Unit)

			duration, ok := durationMetric.Data.(metricdata.Histogram[float64])
			assert.True(t, ok)
			assert.Equal(t, uint64(1), duration.DataPoints[0].Count)

			attrs := duration.DataPoints[0].Attributes

			system, ok := attrs.Value(semconv.RPCSystemNameKey)
			assert.True(t, ok)
			assert.Equal(t, "grpc", system.AsString())

			method, ok := attrs.Value(semconv.RPCMethodKey)
			assert.True(t, ok)
			assert.Equal(t, "test.Service/Do", method.AsString())

			statusCode, ok := attrs.Value(semconv.RPCResponseStatusCodeKey)
			assert.True(t, ok)
			assert.Equal(t, tt.expectedStatus, statusCode.AsString())

			activeMetric, found := findMetric(rm, "rpc.server.active_requests")
			assert.True(t, found)

			active, ok := activeMetric.Data.(metricdata.Sum[int64])
			assert.True(t, ok)
			assert.Equal(t, int64(0), active.DataPoints[0].Value)
		})
	}
}

type fakeServerStream struct {
	ctx context.Context
}

func (s *fakeServerStream) SetHeader(metadata.MD) error {
	return nil
}

func (s *fakeServerStream) SendHeader(metadata.MD) error {
	return nil
}

func (s *fakeServerStream) SetTrailer(metadata.MD) {}

func (s *fakeServerStream) Context() context.Context {
	return s.ctx
}

func (s *fakeServerStream) SendMsg(m any) error {
	return nil
}

func (s *fakeServerStream) RecvMsg(m any) error {
	return nil
}

func TestGrpcStreamInterceptor(t *testing.T) {
	ctx := t.Context()
	reader := setTestProvider(t)

	grpcMetrics, err := NewGrpcMetrics()
	assert.Nil(t, err)

	handler := func(srv any, stream grpc.ServerStream) error {
		return nil
	}

	interceptor := grpcMetrics.StreamServerInterceptor()
	err = interceptor(nil, &fakeServerStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream"}, handler)
	assert.Nil(t, err)

	var rm metricdata.ResourceMetrics
	assert.Nil(t, reader.Collect(ctx, &rm))

	durationMetric, found := findMetric(rm, "rpc.server.call.duration")
	assert.True(t, found)

	duration, ok := durationMetric.Data.(metricdata.Histogram[float64])
	assert.True(t, ok)
	assert.Equal(t, uint64(1), duration.DataPoints[0].Count)

	method, ok := duration.DataPoints[0].Attributes.Value(semconv.RPCMethodKey)
	assert.True(t, ok)
	assert.Equal(t, "test.Service/Stream", method.AsString())

	activeMetric, found := findMetric(rm, "rpc.server.active_requests")
	assert.True(t, found)

	active, ok := activeMetric.Data.(metricdata.Sum[int64])
	assert.True(t, ok)
	assert.Equal(t, int64(0), active.DataPoints[0].Value)
}

func TestGetExportInterval(t *testing.T) {
	cases := []struct {
		cfg              *Config
		expectedInterval time.Duration
	}{
		{
			cfg:              &Config{},
			expectedInterval: 1 * time.Second,
		},
		{
			cfg:              &Config{Lambda: true},
			expectedInterval: 500 * time.Millisecond,
		},
		{
			cfg:              &Config{ExportInterval: 2 * time.Second},
			expectedInterval: 2 * time.Second,
		},
		{
			cfg:              &Config{ExportInterval: 2 * time.Second, Lambda: true},
			expectedInterval: 2 * time.Second,
		},
	}

	for i, tt := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			assert.Equal(t, tt.expectedInterval, getExportInterval(tt.cfg))
		})
	}
}

func TestViewsApplied(t *testing.T) {
	ctx := t.Context()

	cfg := &Config{
		Views: []sdkmetric.View{
			sdkmetric.NewView(
				sdkmetric.Instrument{Name: "old.name"},
				sdkmetric.Stream{Name: "new.name"},
			),
		},
	}

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(getMetricOptions(reader, resource.Default(), cfg)...)
	meter := provider.Meter(TestMeterName)

	counter, err := meter.Int64Counter("old.name")
	assert.Nil(t, err)

	counter.Add(ctx, 1)

	var rm metricdata.ResourceMetrics
	assert.Nil(t, reader.Collect(ctx, &rm))

	_, found := findMetric(rm, "old.name")
	assert.False(t, found)

	renamed, found := findMetric(rm, "new.name")
	assert.True(t, found)

	sum, ok := renamed.Data.(metricdata.Sum[int64])
	assert.True(t, ok)
	assert.Equal(t, int64(1), sum.DataPoints[0].Value)
}

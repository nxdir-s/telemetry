package telemetry

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/aws/aws-sdk-go-v2/aws"

	otelpyroscope "github.com/grafana/otel-profiling-go"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type ErrSdkResource struct {
	err error
}

func (e *ErrSdkResource) Error() string {
	return "failed to create otel sdk resource: " + e.err.Error()
}

type ErrGrpcConn struct {
	err error
}

func (e *ErrGrpcConn) Error() string {
	return "failed to create gRPC connection to collector: " + e.err.Error()
}

type ErrResourceEnv struct {
	err error
}

func (e *ErrResourceEnv) Error() string {
	return "failed to create resource from environment variables: " + e.err.Error()
}

type ErrDefaultResource struct {
	err error
}

func (e *ErrDefaultResource) Error() string {
	return "failed to create default resource: " + e.err.Error()
}

type ErrLogProvider struct{}

func (e *ErrLogProvider) Error() string {
	return "failed to type cast logger provider"
}

type ErrDetectorResource struct {
	err error
}

func (e *ErrDetectorResource) Error() string {
	return "failed to detect resource: " + e.err.Error()
}

type ErrResourceMerge struct {
	err error
}

func (e *ErrResourceMerge) Error() string {
	return "failed to merge otel resource: " + e.err.Error()
}

type ErrMetricExporter struct {
	err error
}

func (e *ErrMetricExporter) Error() string {
	return "failed to create metric exporter: " + e.err.Error()
}

type ErrTraceExporter struct {
	err error
}

func (e *ErrTraceExporter) Error() string {
	return "failed to create trace exporter: " + e.err.Error()
}

type ErrLogExporter struct {
	err error
}

func (e *ErrLogExporter) Error() string {
	return "failed to create log exporter: " + e.err.Error()
}

type ErrAwsInstrumentation struct {
	err error
}

func (e *ErrAwsInstrumentation) Error() string {
	return "failed to setup aws instrumentation: " + e.err.Error()
}

type ErrProtocol struct {
	protocol string
}

func (e *ErrProtocol) Error() string {
	return "unsupported otlp protocol: " + e.protocol
}

type Protocol string

const (
	ProtocolGRPC Protocol = "grpc"
	ProtocolHTTP Protocol = "http/protobuf"
)

const OtlpProtocolEnv string = "OTEL_EXPORTER_OTLP_PROTOCOL"

type Option func() error

func WithAwsInstrumentation(ctx context.Context, cfg *aws.Config) Option {
	return func() error {
		if cfg == nil {
			return nil
		}

		otelaws.AppendMiddlewares(&cfg.APIOptions)

		return nil
	}
}

type CleanupFunc func()

type Config struct {
	OtelEndpoint       string
	TlsConfig          *tls.Config
	Detector           resource.Detector
	Protocol           Protocol
	ExportTimeout      time.Duration
	Lambda             bool
	Insecure           bool
	EnableSpanProfiles bool
	DisableRetry       bool
}

// InitProviders initializes trace and metric providers
func InitProviders(ctx context.Context, cfg *Config, opts ...Option) (CleanupFunc, error) {
	var resource *resource.Resource
	resource, err := setupResource(ctx, cfg)
	if err != nil {
		return nil, &ErrSdkResource{err}
	}

	protocol, err := resolveProtocol(cfg)
	if err != nil {
		return nil, err
	}

	if err := setupTraceProvider(ctx, cfg, protocol, resource); err != nil {
		return nil, err
	}

	if err := setupMeterProvider(ctx, cfg, protocol, resource); err != nil {
		return nil, err
	}

	cleanup := func() {

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
			defer cancel()

			tp, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
			if !ok {
				fmt.Fprint(os.Stdout, "failed sdktrace.TracerProvider type assertion\n")
				return
			}

			if err := tp.Shutdown(ctx); err != nil {
				fmt.Fprintf(os.Stdout, "failed to shutdown trace provider: %s\n", err.Error())
				return
			}
		}()

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
			defer cancel()

			mp, ok := otel.GetMeterProvider().(*sdkmetric.MeterProvider)
			if !ok {
				fmt.Fprint(os.Stdout, "failed sdkmetric.MeterProvider type assertion\n")
				return
			}

			if err := mp.Shutdown(ctx); err != nil {
				fmt.Fprintf(os.Stdout, "failed to shutdown meter provider: %s\n", err.Error())
				return
			}
		}()
	}

	for _, opt := range opts {
		if err := opt(); err != nil {
			return nil, err
		}
	}

	return cleanup, nil
}

func resolveProtocol(cfg *Config) (Protocol, error) {
	protocol := cfg.Protocol
	if len(protocol) == 0 {
		protocol = Protocol(os.Getenv(OtlpProtocolEnv))
	}

	switch protocol {
	case "", ProtocolGRPC:
		return ProtocolGRPC, nil
	case ProtocolHTTP:
		return ProtocolHTTP, nil
	default:
		return "", &ErrProtocol{string(protocol)}
	}
}

func setupClient(cfg *Config) (*grpc.ClientConn, error) {
	if cfg.Insecure {
		return grpc.NewClient(cfg.OtelEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	return grpc.NewClient(cfg.OtelEndpoint, grpc.WithTransportCredentials(credentials.NewTLS(cfg.TlsConfig)))
}

// setupResource creates a resouce with the supplied config and environment variables
func setupResource(ctx context.Context, cfg *Config) (*resource.Resource, error) {
	resourceFromEnv, err := resource.New(ctx, resource.WithFromEnv())
	if err != nil {
		return nil, &ErrResourceEnv{err}
	}

	var otelResource *resource.Resource
	otelResource, err = mergeResource(resource.Default(), resourceFromEnv)
	if err != nil {
		return nil, &ErrDefaultResource{err}
	}

	if cfg.Detector != nil {
		cfgResource, err := cfg.Detector.Detect(ctx)
		if err != nil {
			return nil, &ErrDetectorResource{err}
		}

		otelResource, err = resource.Merge(cfgResource, otelResource)
		if err != nil {
			return nil, &ErrResourceMerge{err}
		}
	}

	return otelResource, nil
}

// mergeResource merges b into a, with b winning on conflicting keys
func mergeResource(a *resource.Resource, b *resource.Resource) (*resource.Resource, error) {
	merged, err := resource.Merge(a, b)
	if err != nil {
		if errors.Is(err, resource.ErrSchemaURLConflict) {
			return merged, nil
		}

		return nil, err
	}

	return merged, nil
}

// setupTraceProvider configures a trace provider
func setupTraceProvider(ctx context.Context, cfg *Config, protocol Protocol, resource *resource.Resource) error {
	traceExporter, err := setupTraceExporter(ctx, cfg, protocol)
	if err != nil {
		return &ErrTraceExporter{err}
	}

	traceProvider := getTraceProvider(traceExporter, resource, cfg)

	setTracerProvider(traceProvider, cfg.EnableSpanProfiles)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return nil
}

func setupTraceExporter(ctx context.Context, cfg *Config, protocol Protocol) (sdktrace.SpanExporter, error) {
	switch protocol {
	case ProtocolHTTP:
		return setupHttpTraceExporter(ctx, cfg)
	default:
		return setupGrpcTraceExporter(ctx, cfg)
	}
}

func setupHttpTraceExporter(ctx context.Context, cfg *Config) (sdktrace.SpanExporter, error) {
	opts := make([]otlptracehttp.Option, 0, 2)

	if cfg.ExportTimeout > 0 {
		opts = append(opts, otlptracehttp.WithTimeout(cfg.ExportTimeout))
	}

	if cfg.DisableRetry {
		opts = append(opts, otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}))
	}

	traceExporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, &ErrTraceExporter{err}
	}

	return traceExporter, nil
}

func setupGrpcTraceExporter(ctx context.Context, cfg *Config) (sdktrace.SpanExporter, error) {
	opts := make([]otlptracegrpc.Option, 0, 3)

	conn, err := setupClient(cfg)
	if err != nil {
		return nil, &ErrGrpcConn{err}
	}

	opts = append(opts, otlptracegrpc.WithGRPCConn(conn))

	if cfg.ExportTimeout > 0 {
		opts = append(opts, otlptracegrpc.WithTimeout(cfg.ExportTimeout))
	}

	if cfg.DisableRetry {
		opts = append(opts, otlptracegrpc.WithRetry(otlptracegrpc.RetryConfig{Enabled: false}))
	}

	traceExporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, &ErrTraceExporter{err}
	}

	return traceExporter, nil
}

func getTraceProvider(exporter sdktrace.SpanExporter, resource *resource.Resource, cfg *Config) *sdktrace.TracerProvider {
	switch {
	case cfg.Lambda:
		return sdktrace.NewTracerProvider(
			sdktrace.WithResource(resource),
			sdktrace.WithSampler(sdktrace.AlwaysSample()),
			sdktrace.WithSyncer(exporter),
		)
	default:
		return sdktrace.NewTracerProvider(
			sdktrace.WithResource(resource),
			sdktrace.WithSampler(sdktrace.AlwaysSample()),
			sdktrace.WithBatcher(exporter),
		)
	}
}

func setTracerProvider(tp trace.TracerProvider, enableSpanProfiles bool) {
	switch enableSpanProfiles {
	case true:
		otel.SetTracerProvider(otelpyroscope.NewTracerProvider(tp))
	case false:
		otel.SetTracerProvider(tp)
	}
}

// setupMeterProvider configures a meter provider
func setupMeterProvider(ctx context.Context, cfg *Config, protocol Protocol, resource *resource.Resource) error {
	metricExporter, err := setupMetricExporter(ctx, cfg, protocol)
	if err != nil {
		return &ErrMetricExporter{err}
	}

	meterProvider := getMeterProvider(metricExporter, resource, cfg)

	otel.SetMeterProvider(meterProvider)

	return nil
}

func setupMetricExporter(ctx context.Context, cfg *Config, protocol Protocol) (sdkmetric.Exporter, error) {
	switch protocol {
	case ProtocolHTTP:
		return setupHttpMetricExporter(ctx, cfg)
	default:
		return setupGrpcMetricExporter(ctx, cfg)
	}
}

func setupHttpMetricExporter(ctx context.Context, cfg *Config) (sdkmetric.Exporter, error) {
	opts := make([]otlpmetrichttp.Option, 0, 2)

	if cfg.ExportTimeout > 0 {
		opts = append(opts, otlpmetrichttp.WithTimeout(cfg.ExportTimeout))
	}

	if cfg.DisableRetry {
		opts = append(opts, otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{Enabled: false}))
	}

	metricExporter, err := otlpmetrichttp.New(ctx, opts...)
	if err != nil {
		return nil, &ErrMetricExporter{err}
	}

	return metricExporter, nil
}

func setupGrpcMetricExporter(ctx context.Context, cfg *Config) (sdkmetric.Exporter, error) {
	opts := make([]otlpmetricgrpc.Option, 0, 3)

	conn, err := setupClient(cfg)
	if err != nil {
		return nil, &ErrGrpcConn{err}
	}

	opts = append(opts, otlpmetricgrpc.WithGRPCConn(conn))

	if cfg.ExportTimeout > 0 {
		opts = append(opts, otlpmetricgrpc.WithTimeout(cfg.ExportTimeout))
	}

	if cfg.DisableRetry {
		opts = append(opts, otlpmetricgrpc.WithRetry(otlpmetricgrpc.RetryConfig{Enabled: false}))
	}

	metricExporter, err := otlpmetricgrpc.New(ctx, opts...)
	if err != nil {
		return nil, &ErrMetricExporter{err}
	}

	return metricExporter, nil
}

func getMeterProvider(exporter sdkmetric.Exporter, resource *resource.Resource, cfg *Config) *sdkmetric.MeterProvider {
	switch {
	case cfg.Lambda:
		return sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(resource),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
				exporter,
				sdkmetric.WithInterval(500*time.Millisecond),
			)),
		)
	default:
		return sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(resource),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
				exporter,
				sdkmetric.WithInterval(1*time.Second),
			)),
		)
	}
}

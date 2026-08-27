package telemetry

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"

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

type Option func() error

func WithAwsInstrumentation(ctx context.Context, cfg *aws.Config) Option {
	return func() error {
		var config aws.Config

		switch cfg == nil {
		case true:
			var err error
			config, err = awsconfig.LoadDefaultConfig(ctx)
			if err != nil {
				return &ErrAwsInstrumentation{err}
			}
		case false:
			config = *cfg
		}

		otelaws.AppendMiddlewares(&config.APIOptions)

		return nil
	}
}

type CleanupFunc func()

type Config struct {
	ServiceName        string
	OtelEndpoint       string
	TlsConfig          *tls.Config
	Detector           resource.Detector
	Views              []sdkmetric.View
	ExportInterval     time.Duration
	Lambda             bool
	Insecure           bool
	EnableSpanProfiles bool
}

// InitProviders initializes trace and metric providers
func InitProviders(ctx context.Context, cfg *Config, opts ...Option) (CleanupFunc, error) {
	var resource *resource.Resource
	resource, err := setupResource(ctx, cfg)
	if err != nil {
		return nil, &ErrSdkResource{err}
	}

	if err := setupTraceProvider(ctx, cfg, resource); err != nil {
		return nil, err
	}

	if err := setupMeterProvider(ctx, cfg, resource); err != nil {
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
	otelResource, err = resource.Merge(
		resource.Default(),
		resourceFromEnv,
	)
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

// setupTraceProvider configures a trace provider
func setupTraceProvider(ctx context.Context, cfg *Config, resource *resource.Resource) error {
	conn, err := setupClient(cfg)
	if err != nil {
		return &ErrGrpcConn{err}
	}

	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return &ErrTraceExporter{err}
	}

	traceProvider := getTraceProvider(traceExporter, resource, cfg.Lambda)

	setTracerProvider(traceProvider, cfg.EnableSpanProfiles)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return nil
}

func getTraceProvider(exporter sdktrace.SpanExporter, resource *resource.Resource, lambda bool) *sdktrace.TracerProvider {
	switch lambda {
	case true:
		return sdktrace.NewTracerProvider(
			sdktrace.WithResource(resource),
			sdktrace.WithSampler(sdktrace.AlwaysSample()),
			sdktrace.WithSyncer(exporter),
		)
	case false:
		return sdktrace.NewTracerProvider(
			sdktrace.WithResource(resource),
			sdktrace.WithSampler(sdktrace.AlwaysSample()),
			sdktrace.WithBatcher(exporter),
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
func setupMeterProvider(ctx context.Context, cfg *Config, resource *resource.Resource) error {
	conn, err := setupClient(cfg)
	if err != nil {
		return &ErrGrpcConn{err}
	}

	metricExporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithGRPCConn(conn))
	if err != nil {
		return &ErrMetricExporter{err}
	}

	meterProvider := getMeterProvider(metricExporter, resource, cfg)

	otel.SetMeterProvider(meterProvider)

	return nil
}

// getMeterProvider builds a meter provider with a periodic reader
func getMeterProvider(exporter sdkmetric.Exporter, resource *resource.Resource, cfg *Config) *sdkmetric.MeterProvider {
	reader := sdkmetric.NewPeriodicReader(
		exporter,
		sdkmetric.WithInterval(getExportInterval(cfg)),
	)

	return sdkmetric.NewMeterProvider(getMetricOptions(reader, resource, cfg)...)
}

// getMetricOptions assembles meter provider options
func getMetricOptions(reader sdkmetric.Reader, resource *resource.Resource, cfg *Config) []sdkmetric.Option {
	return []sdkmetric.Option{
		sdkmetric.WithResource(resource),
		sdkmetric.WithReader(reader),
		sdkmetric.WithView(cfg.Views...),
	}
}

// getExportInterval resolves the metric export interval
func getExportInterval(cfg *Config) time.Duration {
	if cfg.ExportInterval > 0 {
		return cfg.ExportInterval
	}

	switch cfg.Lambda {
	case true:
		return 500 * time.Millisecond
	default:
		return 1 * time.Second
	}
}

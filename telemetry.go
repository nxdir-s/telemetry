package telemetry

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"time"

	lambdadetector "go.opentelemetry.io/contrib/detectors/aws/lambda"
	"go.opentelemetry.io/contrib/propagators/aws/xray"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type TracerCtxKey struct{}
type MeterCtxKey struct{}
type LoggerCtxKey struct{}

type ShutdownFuncs []func(context.Context) error
type CleanupFunc func(context.Context)

type Config struct {
	ServiceName  string
	OtelEndpoint string
	TlsConfig    *tls.Config
	Lambda       bool
}

// InitProviders initializes trace and metric providers, and adds a tracer and meter to the context
func InitProviders(ctx context.Context, cfg *Config) (context.Context, CleanupFunc, error) {
	shutdown := make(ShutdownFuncs, 0, 2)

	resource, err := setupResource(ctx, cfg)
	if err != nil {
		return ctx, nil, SdkResourceError{err}
	}

	grpcClient, err := grpc.NewClient(cfg.OtelEndpoint, grpc.WithTransportCredentials(credentials.NewTLS(cfg.TlsConfig)))
	if err != nil {
		return ctx, nil, GrpcConnError{err}
	}

	traceProvider, err := setupTraceProvider(ctx, grpcClient, resource)
	if err != nil {
		return ctx, nil, err
	}
	shutdown = append(shutdown, traceProvider.Shutdown)

	meterProvider, err := setupMeterProvider(ctx, grpcClient, resource)
	if err != nil {
		return ctx, nil, err
	}
	shutdown = append(shutdown, meterProvider.Shutdown)

	tracer := traceProvider.Tracer(cfg.ServiceName)
	meter := meterProvider.Meter(cfg.ServiceName)

	ctx = context.WithValue(ctx, TracerCtxKey{}, tracer)
	ctx = context.WithValue(ctx, MeterCtxKey{}, meter)

	cleanup := func(ctx context.Context) {
		var err error
		for _, fn := range shutdown {
			err = errors.Join(err, fn(ctx))
		}

		if err != nil {
			fmt.Fprintf(os.Stdout, "error shutting down telemetry providers: %s\n", err.Error())
		}
	}

	return ctx, cleanup, nil
}

// setupResource creates a resouce with the supplied config and environment variables
func setupResource(ctx context.Context, cfg *Config) (*resource.Resource, error) {
	resourceFromEnv, err := resource.New(ctx, resource.WithFromEnv())
	if err != nil {
		return nil, ResourceEnvError{err}
	}

	var defaultResource *resource.Resource
	defaultResource, err = resource.Merge(
		resource.Default(),
		resourceFromEnv,
	)
	if err != nil {
		return nil, DefaultResourceError{err}
	}

	if cfg.Lambda {
		detector := lambdadetector.NewResourceDetector()
		lambdaResource, err := detector.Detect(ctx)
		if err != nil {
			return nil, LambdaResourceError{err}
		}

		defaultResource, err = resource.Merge(lambdaResource, defaultResource)
		if err != nil {
			return nil, ResourceMergeError{err}
		}
	}

	resource, err := resource.Merge(
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String(cfg.ServiceName),
		),
		defaultResource,
	)
	if err != nil {
		return nil, ResourceMergeError{err}
	}

	return resource, nil
}

// setupTraceProvider configures a trace provider
func setupTraceProvider(ctx context.Context, conn *grpc.ClientConn, resource *resource.Resource) (*sdktrace.TracerProvider, error) {
	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, TraceExporterError{err}
	}

	traceProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(traceExporter)),
	)

	otel.SetTracerProvider(traceProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		xray.Propagator{},
	))

	return traceProvider, nil
}

// setupMeterProvider configures a meter provider
func setupMeterProvider(ctx context.Context, conn *grpc.ClientConn, resource *resource.Resource) (*sdkmetric.MeterProvider, error) {
	metricExporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, MetricExporterError{err}
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(resource),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
			metricExporter,
			sdkmetric.WithInterval(1*time.Second),
		)),
	)

	otel.SetMeterProvider(meterProvider)

	return meterProvider, nil
}

// setupLoggerProvider configures a logger provider and adds it to the context. Feature still in BETA
func setupLoggerProvider(ctx context.Context, conn *grpc.ClientConn, resource *resource.Resource) (context.Context, error) {
	logExporter, err := otlploggrpc.New(ctx, otlploggrpc.WithGRPCConn(conn))
	if err != nil {
		return ctx, LogExporterError{err}
	}

	loggerProvider := sdklog.NewLoggerProvider(
		sdklog.WithResource(resource),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
	)

	ctx = context.WithValue(ctx, LoggerCtxKey{}, loggerProvider)

	return ctx, nil
}

// AddTracerContext adds the tracer to the context
func AddTracerContext(ctx context.Context, tracer trace.Tracer) context.Context {
	return context.WithValue(ctx, TracerCtxKey{}, tracer)
}

// AddMeterContext adds the meter to the context
func AddMeterContext(ctx context.Context, meter metric.Meter) context.Context {
	return context.WithValue(ctx, MeterCtxKey{}, meter)
}

// TracerFromContext checks the context for a tracer. The returned value can be nil
func TracerFromContext(ctx context.Context) (trace.Tracer, error) {
	tracer, ok := ctx.Value(TracerCtxKey{}).(trace.Tracer)
	if !ok {
		return nil, TracerError{}
	}

	return tracer, nil
}

// MeterFromContext checks the context for a meter. The returned value can be nil
func MeterFromContext(ctx context.Context) (metric.Meter, error) {
	meter, ok := ctx.Value(MeterCtxKey{}).(metric.Meter)
	if !ok {
		return nil, MeterError{}
	}

	return meter, nil
}

// LogProviderFromContext checks the context for a logger provider. The returned value can be nil
func LogProviderFromContext(ctx context.Context) (*sdklog.LoggerProvider, error) {
	logProvider, ok := ctx.Value(LoggerCtxKey{}).(*sdklog.LoggerProvider)
	if !ok {
		return nil, LogProviderError{}
	}

	return logProvider, nil
}

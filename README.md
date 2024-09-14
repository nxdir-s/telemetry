# Telemetry

This repository contains utilities for working with Open Telemetry. You can find a getting started guide with OpenTelemetry in Go on [opentelemetry.io](https://opentelemetry.io/docs/languages/go/getting-started/)

## Usage

Initialize the telemetry providers within `main()`

```go
cfg := &telemetry.Config{
    ServiceName:    os.Getenv("OTEL_SERVICE_NAME"),
    Endpoint:       os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
    Lambda:         true,
}

ctx, cleanup, err := telemetry.InitProviders(ctx, cfg)
if err != nil {
    // handle error
}
```

<br />

Example Lambda Setup

```go
func main() {
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
    defer cancel()

    logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
    slog.SetDefault(logger)

    cfg := &telemetry.Config{
        ServiceName:    os.Getenv("OTEL_SERVICE_NAME"),
        Endpoint:       os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
        Lambda:         true,
    }

    ctx, cleanup, err := telemetry.InitProviders(ctx, cfg)
    if err != nil {
        // handle error
    }

    adapter := primary.NewLambdaAdapter()

    // any remaining setup for lambda...

    lambda.StartWithOptions(
        otellambda.InstrumentHandler(adapter.HandleRequest,
            otellambda.WithTracerProvider(otel.GetTracerProvider()),
            otellambda.WithFlusher(otel.GetTracerProvider().(*trace.TracerProvider)),
            otellambda.WithPropagator(otel.GetTextMapPropagator()),
        ),
        lambda.WithContext(ctx),
        lambda.WithEnableSIGTERM(func() {
            cancel()
        }),
    )
}
```

## Instrumentation

Applications can be manually instrumented or you can use any of the [officially supported instrumentation libraries](https://github.com/open-telemetry/opentelemetry-go-contrib/tree/main/instrumentation)

Once providers are initialized, a tracer or meter can be retrieved from the context

```go
tracer, err := telemetry.TracerFromContext(ctx)
```

```go
meter, err := telemetry.MeterFromContext(ctx)
```

> docs: https://opentelemetry.io/docs/languages/go/instrumentation/#metrics

> Note: values returned by the functions above can be nil pointers, make sure to have proper validations before using them

<br />

To add custom spans within your application, the following can be done

```go
ctx, span := tracer.Start(ctx, "Adapter.HandleRequest")
defer span.End()
```

> docs: https://opentelemetry.io/docs/languages/go/instrumentation/#creating-spans

<br />

### AWS SDK

Add the following after initialization to instrument the aws sdk

```go
// init aws config
cfg, err := awsConfig.LoadDefaultConfig(ctx)
if err != nil {
    // handle error
}

// instrument all aws clients
otelaws.AppendMiddlewares(&cfg.APIOptions)
```

<br />

### Host and Runtime Metrics

Add the following after initialization to instrument host and runtime metric collection

```go
err = host.Start(host.WithMeterProvider(otel.GetMeterProvider()))
if err != nil {
    // handle error
}

err = runtime.Start(runtime.WithMeterProvider(otel.GetMeterProvider()), runtime.WithMinimumReadMemStatsInterval(time.Second))
if err != nil {
    // handle error
}
```

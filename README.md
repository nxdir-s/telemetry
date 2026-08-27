# Telemetry

This repository contains utilities for working with Open Telemetry. You can find a getting started guide with OpenTelemetry in Go on [opentelemetry.io](https://opentelemetry.io/docs/languages/go/getting-started/)

## Usage

Initialize the telemetry providers within `main()`

```go
cfg := &telemetry.Config{
    ServiceName:        os.Getenv("OTEL_SERVICE_NAME"),
    OtelEndpoint:       os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
}

cleanup, err := telemetry.InitProviders(ctx, cfg)
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
        ServiceName:        os.Getenv("OTEL_SERVICE_NAME"),
        OtelEndpoint:       os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
        Lambda:             true,
    }

    cleanup, err := telemetry.InitProviders(ctx, cfg)
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
            cleanup()
            cancel()
        }),
    )
}
```

## Instrumentation

Applications can be manually instrumented or you can use any of the [officially supported instrumentation libraries](https://github.com/open-telemetry/opentelemetry-go-contrib/tree/main/instrumentation)

> docs: https://opentelemetry.io/docs/languages/go/instrumentation/#metrics

<br />

To add custom spans within your application, the following can be done

```go
ctx, span := tracer.Start(ctx, "Adapter.HandleRequest")
defer span.End()
```

> docs: https://opentelemetry.io/docs/languages/go/instrumentation/#creating-spans

<br />

## Custom Metrics

The library ships ready-made metric bundles for common use cases. Each bundle creates its instruments with OpenTelemetry semantic convention names, units, and histogram buckets, and records through the global meter provider — call the bundle functions after `InitProviders`.

Use HTTP or gRPC metrics when the request path runs through your handlers. Use the runtime and database bundles for values you can only poll — they observe on each export automatically, with no recording calls in your code.

### HTTP request metrics

Wrap any `http.Handler` to record request metrics automatically. Routes registered on a pattern-based `http.ServeMux` are captured in the `http.route` attribute

```go
reqMetrics, err := telemetry.NewRequestMetrics()
if err != nil {
    // handle error
}

mux := http.NewServeMux()
mux.HandleFunc("GET /orders/{id}", getOrder)

http.ListenAndServe(":8080", reqMetrics.Handler(mux))
```

For non-HTTP or custom flows, record durations manually

```go
start := time.Now()

// ... handle work ...

reqMetrics.Record(ctx, time.Since(start),
    telemetry.WithMethod(http.MethodGet),
    telemetry.WithRoute("/orders/{id}"),
    telemetry.WithStatusCode(http.StatusOK),
)
```

| Metric | Type | Description |
|---|---|---|
| `http.server.request.duration` | histogram (s) | request latency with semconv buckets |
| `http.server.active_requests` | up/down counter | in-flight requests |
| `http.server.request.body.size` | histogram (By) | request body size |
| `http.server.response.body.size` | histogram (By) | response body size |

### Go runtime metrics

Observes the Go runtime on every export

```go
if err := telemetry.GoRoutineMetrics(); err != nil {
    // handle error
}
```

| Metric | Type | Description |
|---|---|---|
| `go.goroutine.count` | up/down counter | live goroutines |
| `go.memory.used` | up/down counter (By) | memory in use, split by `go.memory.type` |
| `go.memory.limit` | up/down counter (By) | GOMEMLIMIT, omitted when unset |
| `go.memory.allocated` | counter (By) | cumulative heap allocations |
| `go.memory.allocations` | counter | cumulative allocated objects |
| `go.memory.gc.goal` | up/down counter (By) | heap size target of the next GC cycle |
| `go.config.gogc` | up/down counter (%) | GOGC setting |
| `go.processor.limit` | up/down counter | GOMAXPROCS |

### Database pool metrics

Observes the connection pool of a `*sql.DB` on every export. The pool name is attached as the `db.client.connection.pool.name` attribute

```go
if err := telemetry.DatabaseMetrics(db, "orders"); err != nil {
    // handle error
}
```

| Metric | Type | Description |
|---|---|---|
| `db.client.connection.count` | up/down counter | connections by `db.client.connection.state` (idle/used) |
| `db.client.connection.max` | up/down counter | max open connections |
| `db.client.connection.wait_time` | counter (s) | cumulative time waiting for a connection |
| `db.client.connection.waits` | counter | cumulative number of waits |
| `db.client.connection.closed` | counter | connections closed by pool limits, split by `db.client.connection.close.reason` |

### gRPC server metrics

Record RPC metrics through server interceptors

```go
grpcMetrics, err := telemetry.NewGrpcMetrics()
if err != nil {
    // handle error
}

server := grpc.NewServer(
    grpc.ChainUnaryInterceptor(grpcMetrics.UnaryServerInterceptor()),
    grpc.ChainStreamInterceptor(grpcMetrics.StreamServerInterceptor()),
)
```

| Metric | Type | Description |
|---|---|---|
| `rpc.server.call.duration` | histogram (s) | call latency with `rpc.method` and `rpc.response.status_code` |
| `rpc.server.active_requests` | up/down counter | in-flight calls |

### Anything else

For metrics not covered by a bundle, create a meter and use the OpenTelemetry metric API directly

```go
meter := telemetry.NewMeter("payments")

queueDepth, err := meter.Int64UpDownCounter("payments.queue.depth",
    telemetry.WithDescription("pending payment jobs"),
    telemetry.WithUnit("{job}"),
)
if err != nil {
    // handle error
}

queueDepth.Add(ctx, 1)
```

> docs: https://opentelemetry.io/docs/languages/go/instrumentation/#metrics

### SDK configuration

Views can rename, re-aggregate, or drop instruments, and the metric export interval is configurable. Zero values keep the defaults, a 1s export interval, or 500ms when `Lambda` is true

```go
cfg := &telemetry.Config{
    ServiceName:  os.Getenv("OTEL_SERVICE_NAME"),
    OtelEndpoint: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
    Views: []sdkmetric.View{
        sdkmetric.NewView(
            sdkmetric.Instrument{Name: "http.server.request.duration"},
            sdkmetric.Stream{Name: "http.request.latency"},
        ),
    },
    ExportInterval: 10 * time.Second,
}
```

<br />

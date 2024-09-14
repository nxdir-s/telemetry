package telemetry

type SdkResourceError struct {
	err error
}

func (e SdkResourceError) Error() string {
	return "failed to create otel sdk resource: " + e.err.Error()
}

type GrpcConnError struct {
	err error
}

func (e GrpcConnError) Error() string {
	return "failed to create gRPC connection to collector: " + e.err.Error()
}

type TracerError struct{}

func (e TracerError) Error() string {
	return "failed to type cast tracer"
}

type ResourceEnvError struct {
	err error
}

type MeterError struct{}

func (e MeterError) Error() string {
	return "failed to type cast meter"
}

func (e ResourceEnvError) Error() string {
	return "failed to create resource from environment variables: " + e.err.Error()
}

type DefaultResourceError struct {
	err error
}

func (e DefaultResourceError) Error() string {
	return "failed to create default resource: " + e.err.Error()
}

type LogProviderError struct{}

func (e LogProviderError) Error() string {
	return "failed to type cast logger provider"
}

type LambdaResourceError struct {
	err error
}

func (e LambdaResourceError) Error() string {
	return "failed to create lambda resource: " + e.err.Error()
}

type ResourceMergeError struct {
	err error
}

func (e ResourceMergeError) Error() string {
	return "failed to merge lambda resource: " + e.err.Error()
}

type MetricExporterError struct {
	err error
}

func (e MetricExporterError) Error() string {
	return "failed to create metric exporter: " + e.err.Error()
}

type TraceExporterError struct {
	err error
}

func (e TraceExporterError) Error() string {
	return "failed to create trace exporter: " + e.err.Error()
}

type LogExporterError struct {
	err error
}

func (e LogExporterError) Error() string {
	return "failed to create log exporter: " + e.err.Error()
}

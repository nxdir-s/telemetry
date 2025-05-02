package telemetry

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

type ErrLambdaResource struct {
	err error
}

func (e *ErrLambdaResource) Error() string {
	return "failed to create lambda resource: " + e.err.Error()
}

type ErrResourceMerge struct {
	err error
}

func (e *ErrResourceMerge) Error() string {
	return "failed to merge lambda resource: " + e.err.Error()
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

package telemetry

import (
	"context"
	"crypto/tls"
	"strconv"
	"testing"

	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/stretchr/testify/assert"
)

const (
	TestServiceName string = "testservice"
	TestEndpoint    string = "127.0.0.1:8092"
)

var TestDetector = resource.StringDetector(semconv.SchemaURL, semconv.ServiceNameKey, func() (string, error) {
	return TestServiceName, nil
})

func TestInitProviders(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cases := []struct {
		cfg         *Config
		opts        []Option
		expectedErr error
	}{
		{
			cfg: &Config{
				OtelEndpoint: TestEndpoint,
				TlsConfig:    &tls.Config{},
				Detector:     TestDetector,
			},
			opts:        []Option{},
			expectedErr: nil,
		},
		{
			cfg: &Config{
				OtelEndpoint: TestEndpoint,
				TlsConfig:    &tls.Config{},
				Detector:     TestDetector,
			},
			opts: []Option{
				WithAwsInstrumentation(ctx, nil),
			},
			expectedErr: nil,
		},
		{
			cfg: &Config{
				OtelEndpoint: TestEndpoint,
				TlsConfig:    &tls.Config{},
				Detector:     TestDetector,
				Protocol:     ProtocolHTTP,
			},
			opts:        []Option{},
			expectedErr: nil,
		},
		{
			cfg: &Config{
				OtelEndpoint: TestEndpoint,
				Detector:     TestDetector,
				Protocol:     ProtocolHTTP,
				Insecure:     true,
			},
			opts:        []Option{},
			expectedErr: nil,
		},
	}

	for i, tt := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			cleanup, err := InitProviders(ctx, tt.cfg, tt.opts...)

			assert.Equal(t, tt.expectedErr, err)
			if cleanup != nil {
				cleanup()
			}
		})
	}
}

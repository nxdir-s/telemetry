package telemetry

import (
	"context"
	"crypto/tls"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

const (
	TestServiceName string = "testservice"
	TestEndpoint    string = "127.0.0.1:8092"
)

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
				ServiceName:  TestServiceName,
				OtelEndpoint: TestEndpoint,
				TlsConfig:    &tls.Config{},
			},
			opts:        []Option{},
			expectedErr: nil,
		},
		{
			cfg: &Config{
				ServiceName:  TestServiceName,
				OtelEndpoint: TestEndpoint,
				TlsConfig:    &tls.Config{},
			},
			opts: []Option{
				WithAwsInstrumentation(ctx, nil),
			},
			expectedErr: nil,
		},
		{
			cfg: &Config{
				ServiceName:  TestServiceName,
				OtelEndpoint: TestEndpoint,
				TlsConfig:    &tls.Config{},
				Views: []sdkmetric.View{
					sdkmetric.NewView(
						sdkmetric.Instrument{Name: "old.name"},
						sdkmetric.Stream{Name: "new.name"},
					),
				},
			},
			opts:        []Option{},
			expectedErr: nil,
		},
		{
			cfg: &Config{
				ServiceName:    TestServiceName,
				OtelEndpoint:   TestEndpoint,
				TlsConfig:      &tls.Config{},
				ExportInterval: 5 * time.Second,
			},
			opts:        []Option{},
			expectedErr: nil,
		},
	}

	for i, tt := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			cleanup, err := InitProviders(ctx, tt.cfg, tt.opts...)

			assert.Equal(t, tt.expectedErr, err)
			cleanup()
		})
	}
}

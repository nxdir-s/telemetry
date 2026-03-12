package telemetry

import (
	"context"
	"crypto/tls"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	TestServiceName string = "testservice"
	TestEndpoint    string = "http://127.0.0.1:5860"
)

func TestInitProviders(t *testing.T) {
	cases := []struct {
		cfg         *Config
		expectedErr error
	}{
		{
			cfg: &Config{
				ServiceName:  TestServiceName,
				OtelEndpoint: TestEndpoint,
				TlsConfig:    &tls.Config{},
			},
			expectedErr: nil,
		},
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	for i, tt := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			cleanup, err := InitProviders(ctx, tt.cfg)

			assert.Equal(t, tt.expectedErr, err)
			cleanup()
		})
	}
}

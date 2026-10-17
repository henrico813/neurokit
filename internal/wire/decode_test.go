package wire_test

import (
	"strings"
	"testing"

	"github.com/henrico813/neurokit/internal/wire"
)

// Every body neurokit reads is meant to go through DecodeOneJSONValue: request
// arguments, handler and broker answers, and the manifest. Two existing
// brokers cap no body size and accept an empty body. A write value such as
// "Sent" must fail here, not pass on as the handler's own outcome.
func TestDecodeRejectsLooseJSON(t *testing.T) {
	const body = `{"error":{"code":"unexpected","message":"x"}}`
	tests := []struct {
		name     string
		input    string
		maxBytes int64
		wantErr  bool
	}{
		{
			name:     "exactly at the limit",
			input:    body,
			maxBytes: int64(len(body)),
			wantErr:  false,
		},
		{
			name:     "one byte over the limit",
			input:    body + " ",
			maxBytes: int64(len(body)),
			wantErr:  true,
		},
		{
			name:     "empty",
			input:    "  \n",
			maxBytes: 100,
			wantErr:  true,
		},
		{
			name:     "null",
			input:    "null",
			maxBytes: 100,
			wantErr:  true,
		},
		{
			name:     "trailing data",
			input:    body + " {}",
			maxBytes: 100,
			wantErr:  true,
		},
		{
			name:     "unknown field",
			input:    `{"error":{"code":"unexpected","message":"x"},"wirte":"sent"}`,
			maxBytes: 100,
			wantErr:  true,
		},
		{
			name:     "unrecognized write value",
			input:    `{"error":{"code":"unexpected","message":"x"},"write":"Sent"}`,
			maxBytes: 100,
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				var got wire.Reply

				err := wire.DecodeOneJSONValue(
					strings.NewReader(tt.input),
					tt.maxBytes,
					&got,
				)

				if (err != nil) != tt.wantErr {
					t.Errorf(
						"DecodeOneJSONValue(%q, %d) error = %v; want error: %t",
						tt.input,
						tt.maxBytes,
						err,
						tt.wantErr,
					)
				}
			},
		)
	}
}

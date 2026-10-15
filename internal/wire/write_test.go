package wire_test

import (
	"testing"

	"github.com/henrico813/neurokit/internal/wire"
)

// Ensures that MarkUnclearChangeAsUnknown does not return contradictory
// outputs.
func TestUnclearChangeBecomesUnknown(t *testing.T) {
	tests := []struct {
		name           string
		code           string
		writeState     wire.WriteState
		wantCode       string
		wantWriteState wire.WriteState
	}{
		{
			name:           "success without write",
			code:           "",
			writeState:     "",
			wantCode:       wire.CodeChangeUnknown,
			wantWriteState: wire.WriteStateUnknown,
		},
		{
			name:           "error without write",
			code:           wire.CodeProviderRejected,
			writeState:     "",
			wantCode:       wire.CodeChangeUnknown,
			wantWriteState: wire.WriteStateUnknown,
		},
		{
			name:           "success only sent",
			code:           "",
			writeState:     wire.WriteStateSent,
			wantCode:       wire.CodeChangeUnknown,
			wantWriteState: wire.WriteStateUnknown,
		},
		{
			name:           "change-unknown claiming sent",
			code:           wire.CodeChangeUnknown,
			writeState:     wire.WriteStateSent,
			wantCode:       wire.CodeChangeUnknown,
			wantWriteState: wire.WriteStateUnknown,
		},
		{
			name:           "verification-failed claiming not-sent",
			code:           wire.CodeVerificationFailed,
			writeState:     wire.WriteStateNotSent,
			wantCode:       wire.CodeChangeUnknown,
			wantWriteState: wire.WriteStateUnknown,
		},
		{
			name:           "error claiming verified",
			code:           wire.CodeProviderRejected,
			writeState:     wire.WriteStateVerified,
			wantCode:       wire.CodeChangeUnknown,
			wantWriteState: wire.WriteStateUnknown,
		},
		{
			name:           "verification-failed after send",
			code:           wire.CodeVerificationFailed,
			writeState:     wire.WriteStateSent,
			wantCode:       wire.CodeVerificationFailed,
			wantWriteState: wire.WriteStateSent,
		},
		{
			name:           "no-op verified success",
			code:           "",
			writeState:     wire.WriteStateVerified,
			wantCode:       "",
			wantWriteState: wire.WriteStateVerified,
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				code,
					writeState := wire.MarkUnclearChangeAsUnknown(
					tt.code,
					tt.writeState,
				)

				if code != tt.wantCode || writeState != tt.wantWriteState {
					t.Errorf(
						"MarkUnclearChangeAsUnknown(%q, %q) = %q, %q; want %q, %q",
						tt.code,
						tt.writeState,
						code,
						writeState,
						tt.wantCode,
						tt.wantWriteState,
					)
				}
			},
		)
	}
}

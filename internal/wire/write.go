package wire

import "fmt"

type WriteState string

const (
	WriteStateUnknown  WriteState = "unknown"
	WriteStateNotSent  WriteState = "not-sent"
	WriteStateSent     WriteState = "sent"
	WriteStateVerified WriteState = "verified"
)

func ParseWriteState(s string) (WriteState, error) {
	switch ws := WriteState(s); ws {
	case WriteStateUnknown, WriteStateNotSent, WriteStateSent, WriteStateVerified:
		return ws, nil
	}
	return "", fmt.Errorf("write %q is not unknown, not-sent, sent or verified", s)
}

// JSON decoding rejects a write value that ParseWriteState does not
// accept, such as "Sent" or "done". A JSON null skips this check and
// leaves the value empty, which later counts as unknown.
func (ws *WriteState) UnmarshalText(text []byte) error {
	parsed, err := ParseWriteState(string(text))
	if err != nil {
		return err
	}
	*ws = parsed
	return nil
}

// Returns change-unknown with write unknown when code and writeState do not
// show whether the change happened, such as a success that was only sent;
// otherwise returns them unchanged. Callers retry not-sent and trust
// verified, so neither may be a guess.
func MarkUnclearChangeAsUnknown(code string, writeState WriteState) (string, WriteState) {
	switch {
	case writeState != WriteStateNotSent && writeState != WriteStateSent && writeState != WriteStateVerified,
		code == CodeChangeUnknown,
		code == "" && writeState == WriteStateSent,
		code == CodeVerificationFailed && writeState != WriteStateSent,
		code != "" && writeState == WriteStateVerified:
		return CodeChangeUnknown, WriteStateUnknown
	}
	return code, writeState
}

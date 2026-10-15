package wire

import "fmt"

type WriteState string

const (
	WriteStateUnknown  WriteState = "unknown"
	WriteStateNotSent  WriteState = "not-sent"
	WriteStateSent     WriteState = "sent"
	WriteStateVerified WriteState = "verified"
)

func ParseWriteState(s string) (
	WriteState,
	error,
) {
	switch ws := WriteState(s); ws {
	case WriteStateUnknown,
		WriteStateNotSent,
		WriteStateSent,
		WriteStateVerified:
		return ws,
			nil
	}
	return "",
		fmt.Errorf(
			"write %q is not unknown, not-sent, sent or verified",
			s,
		)
}

// JSON decoding rejects a write value that ParseWriteState does not
// accept, such as "Sent" or "done". A JSON null skips this check and
// leaves the value empty, which later counts as unknown.
func (ws *WriteState) UnmarshalText(text []byte) error {
	parsed,
		err := ParseWriteState(string(text))
	if err != nil {
		return err
	}
	*ws = parsed
	return nil
}

// In cases where the write status is missing or unknown, or contradicts
// the error code, this function marks the change as unknown. This function
// prevents outputs like "success" + "WriteStateUnknown" and
// "provider-rejected" + "WriteStateUnknown" and replaces them with
// "change-unknown" + "WriteStateUnknown".
func MarkUnclearChangeAsUnknown(
	code string,
	writeState WriteState,
) (
	string,
	WriteState,
) {
	switch {
	case writeState != WriteStateNotSent && writeState != WriteStateSent && writeState != WriteStateVerified,
		code == CodeChangeUnknown,
		code == "" && writeState == WriteStateSent,
		code == CodeVerificationFailed && writeState != WriteStateSent,
		code != "" && writeState == WriteStateVerified:
		return CodeChangeUnknown,
			WriteStateUnknown
	}
	return code,
		writeState
}

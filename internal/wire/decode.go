package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func DecodeOneJSONValue(
	r io.Reader,
	maxBytes int64,
	v any,
) error {
	data,
		err := io.ReadAll(io.LimitReader(
		r,
		maxBytes+1,
	))
	if err != nil {
		return err
	}
	if int64(len(data)) > maxBytes {
		return fmt.Errorf(
			"JSON is larger than %d bytes",
			maxBytes,
		)
	}
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(
		trimmed,
		[]byte("null"),
	) {
		return errors.New("JSON is null")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	if _,
		err := decoder.Token(); err != io.EOF {
		return errors.New("data follows the JSON value")
	}
	return nil
}

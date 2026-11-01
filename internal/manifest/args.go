package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"unicode/utf8"

	"github.com/henrico813/neurokit/internal/wire"
)

const maxApprovalLength = 200

// Returns the arguments, with numbers as json.Number, only if the body is one
// JSON object within MaxBodyBytes holding exactly the declared arguments, each
// within its bounds. An approval label, when the command allows one, comes
// back on its own so the handler never sees it.
func (c *Command) DecodeAndValidateArgs(raw []byte) (
	args map[string]any,
	approvalLabel string,
	err error,
) {
	if err := wire.DecodeOneJSONValue(
		bytes.NewReader(raw),
		c.MaxBodyBytes,
		&args,
	); err != nil {
		return nil, "", err
	}
	if label, ok := args["approval"]; ok && c.Approval {
		if !isTextWithin(
			label,
			0,
			maxApprovalLength,
		) {
			return nil, "", fmt.Errorf(
				"approval must be text of at most %d characters",
				maxApprovalLength,
			)
		}
		approvalLabel = label.(string)
		delete(args, "approval")
	}
	if err := validateArgValues(c.Args, args); err != nil {
		return nil, "", err
	}
	return args, approvalLabel, nil
}

func validateArgValues(fields map[string]Arg, values map[string]any) error {
	for _, name := range slices.Sorted(maps.Keys(values)) {
		a, ok := fields[name]
		if !ok {
			return fmt.Errorf("unknown argument %q", name)
		}
		if err := validateArgValue(name, a, values[name]); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if _, ok := values[name]; !ok && !fields[name].Optional {
			return fmt.Errorf("%s is required", name)
		}
	}
	return nil
}

func validateArgValue(name string, a Arg, value any) error {
	switch a.Type {
	case "string":
		if !isTextWithin(
			value,
			a.MinLength,
			a.MaxLength,
		) {
			return fmt.Errorf(
				"%s must be %s",
				name,
				a.DescribeText(),
			)
		}
	case "integer":
		number, ok := value.(json.Number)
		if ok {
			n, err := number.Int64()
			ok = err == nil && n >= *a.Min && n <= *a.Max
		}
		if !ok {
			return fmt.Errorf(
				"%s must be a whole number from %d to %d",
				name,
				*a.Min,
				*a.Max,
			)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be true or false", name)
		}
	case "list":
		items, ok := value.([]any)
		if !ok || len(items) > a.MaxItems {
			return fmt.Errorf(
				"%s must be a list of at most %d items",
				name,
				a.MaxItems,
			)
		}
		for _, item := range items {
			if err := validateArgValue(name, *a.Items, item); err != nil {
				return err
			}
		}
	case "object":
		fields, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", name)
		}
		return validateArgValues(a.Fields, fields)
	default:
		return fmt.Errorf("%s has unsupported type %q", name, a.Type)
	}
	return nil
}

func isTextWithin(value any, minLength, maxLength int) bool {
	s, ok := value.(string)
	length := utf8.RuneCountInString(s)
	return ok && length >= minLength && length <= maxLength
}

// Returns a string argument's limits in words, such as "text of 1 to 20
// characters", for error messages and help.
func (a Arg) DescribeText() string {
	if a.MinLength > 0 {
		return fmt.Sprintf(
			"text of %d to %d characters",
			a.MinLength,
			a.MaxLength,
		)
	}
	return fmt.Sprintf(
		"text of at most %d characters",
		a.MaxLength,
	)
}

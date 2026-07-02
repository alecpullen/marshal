package registry

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrInvalidArgs = errors.New("invalid tool arguments")

func ValidateArgs(tool Tool, args json.RawMessage) error {
	if len(args) == 0 {
		return nil
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(args, &decoded); err != nil {
		return fmt.Errorf("%w for %q: %v", ErrInvalidArgs, tool.Name, err)
	}
	if decoded == nil {
		return fmt.Errorf("%w for %q: expected JSON object", ErrInvalidArgs, tool.Name)
	}
	return nil
}

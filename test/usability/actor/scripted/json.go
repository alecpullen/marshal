package scripted

import (
	"encoding/json"
	"os"
)

// Load reads a scripted scenario from a JSON file.
func Load(path string) (*Scripted, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Scripted
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

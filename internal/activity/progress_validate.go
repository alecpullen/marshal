package activity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

var errInvalidProgress = errors.New("invalid public progress update")

// DecodeProgress strictly decodes and validates a bounded public update.
// Errors deliberately do not include the supplied payload.
func DecodeProgress(raw []byte) (ProgressUpdate, error) {
	var update ProgressUpdate
	if len(raw) == 0 || len(raw) > MaxProgressBytes {
		return update, fmt.Errorf("%w: payload size must be 1..%d bytes", errInvalidProgress, MaxProgressBytes)
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return update, fmt.Errorf("%w: malformed JSON object", errInvalidProgress)
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(raw, &shape); err != nil || shape == nil {
		return update, fmt.Errorf("%w: expected a JSON object", errInvalidProgress)
	}
	for _, key := range []string{"mode", "headline", "body", "current_action", "sections"} {
		if value, ok := shape[key]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return update, fmt.Errorf("%w: %s cannot be null", errInvalidProgress, key)
		}
	}
	if rawSections, ok := shape["sections"]; ok {
		var sections []map[string]json.RawMessage
		if err := json.Unmarshal(rawSections, &sections); err == nil {
			for i, section := range sections {
				for _, key := range []string{"kind", "text", "evidence_refs"} {
					if value, ok := section[key]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
						return update, fmt.Errorf("%w: sections[%d].%s cannot be null", errInvalidProgress, i, key)
					}
				}
			}
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&update); err != nil {
		return ProgressUpdate{}, fmt.Errorf("%w: malformed shape", errInvalidProgress)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return ProgressUpdate{}, fmt.Errorf("%w: trailing JSON value", errInvalidProgress)
	}
	if err := ValidateProgress(update); err != nil {
		return ProgressUpdate{}, err
	}
	return update, nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var value func(json.Token) error
	value = func(token json.Token) error {
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("duplicate key")
				}
				seen[key] = true
				child, err := dec.Token()
				if err != nil {
					return err
				}
				if err := value(child); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil {
				return err
			}
			if end != json.Delim('}') {
				return errors.New("invalid object")
			}
		case '[':
			for dec.More() {
				child, err := dec.Token()
				if err != nil {
					return err
				}
				if err := value(child); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil {
				return err
			}
			if end != json.Delim(']') {
				return errors.New("invalid array")
			}
		default:
			return errors.New("unexpected delimiter")
		}
		return nil
	}
	first, err := dec.Token()
	if err != nil {
		return err
	}
	if err := value(first); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return errors.New("trailing value")
		}
		return err
	}
	return nil
}

// ValidateProgress validates programmatically constructed updates using the
// same bounds and text rules as DecodeProgress.
func ValidateProgress(u ProgressUpdate) error {
	if u.Mode != ProgressBegin && u.Mode != ProgressRevise {
		return fmt.Errorf("%w: unsupported mode", errInvalidProgress)
	}
	if u.Mode == ProgressBegin && (u.Headline == nil || strings.TrimSpace(*u.Headline) == "") {
		return fmt.Errorf("%w: begin requires a nonblank headline", errInvalidProgress)
	}
	if u.Headline != nil {
		if strings.TrimSpace(*u.Headline) == "" || !validText(*u.Headline, MaxHeadlineRunes, false) {
			return fmt.Errorf("%w: invalid headline", errInvalidProgress)
		}
	}
	if u.Body != nil && !validText(*u.Body, MaxBodyRunes, true) {
		return fmt.Errorf("%w: invalid body", errInvalidProgress)
	}
	if u.CurrentAction != nil && !validText(*u.CurrentAction, MaxActionRunes, false) {
		return fmt.Errorf("%w: invalid current_action", errInvalidProgress)
	}
	if u.Sections == nil {
		return nil
	}
	if len(*u.Sections) > MaxProgressSections {
		return fmt.Errorf("%w: too many sections", errInvalidProgress)
	}
	seen := make(map[SectionKind]bool, len(*u.Sections))
	for _, section := range *u.Sections {
		switch section.Kind {
		case SectionChange, SectionEvidence, SectionChecking, SectionNext, SectionWork:
		default:
			return fmt.Errorf("%w: unsupported section kind", errInvalidProgress)
		}
		if seen[section.Kind] {
			return fmt.Errorf("%w: duplicate section kind", errInvalidProgress)
		}
		seen[section.Kind] = true
		if !validText(section.Text, MaxSectionRunes, true) {
			return fmt.Errorf("%w: invalid section text", errInvalidProgress)
		}
		if len(section.EvidenceRefs) > MaxSectionRefs {
			return fmt.Errorf("%w: too many evidence references", errInvalidProgress)
		}
		for _, ref := range section.EvidenceRefs {
			if len(ref) == 0 || len(ref) > 128 || strings.TrimSpace(ref) != ref || strings.ContainsAny(ref, "\r\n\t") {
				return fmt.Errorf("%w: invalid evidence reference", errInvalidProgress)
			}
			for _, r := range ref {
				if unicode.IsControl(r) {
					return fmt.Errorf("%w: invalid evidence reference", errInvalidProgress)
				}
			}
		}
	}
	return nil
}

func validText(s string, maxRunes int, whitespace bool) bool {
	if len([]rune(s)) > maxRunes {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && !(whitespace && (r == '\n' || r == '\r' || r == '\t')) {
			return false
		}
	}
	return true
}

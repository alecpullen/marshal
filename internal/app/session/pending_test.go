package session

import (
	"encoding/json"
	"testing"
)

func TestQuestionOptionString(t *testing.T) {
	var o QuestionOption
	if err := json.Unmarshal([]byte(`"foo"`), &o); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if o.Label != "foo" {
		t.Fatalf("Label = %q, want foo", o.Label)
	}
	if o.Description != "" {
		t.Fatalf("Description = %q, want empty", o.Description)
	}
}

func TestQuestionOptionObject(t *testing.T) {
	var o QuestionOption
	if err := json.Unmarshal([]byte(`{"label":"foo","description":"bar"}`), &o); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if o.Label != "foo" {
		t.Fatalf("Label = %q, want foo", o.Label)
	}
	if o.Description != "bar" {
		t.Fatalf("Description = %q, want bar", o.Description)
	}
}

func TestQuestionOptionMixedArray(t *testing.T) {
	var q Question
	if err := json.Unmarshal([]byte(`{"question":"pick","options":["simple",{"label":"rich","description":"more"}]}`), &q); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(q.Options) != 2 {
		t.Fatalf("options len = %d, want 2", len(q.Options))
	}
	if q.Options[0].Label != "simple" || q.Options[0].Description != "" {
		t.Fatalf("options[0] = %+v, want {simple }", q.Options[0])
	}
	if q.Options[1].Label != "rich" || q.Options[1].Description != "more" {
		t.Fatalf("options[1] = %+v, want {rich more}", q.Options[1])
	}
}

func TestQuestionOptionInvalidShape(t *testing.T) {
	// null is the subtle one: json.Unmarshal treats it as a no-op success
	// for every target type, so without an explicit guard it would take the
	// string branch and decode as an empty label.
	for _, in := range []string{`{"description":"x"}`, `123`, `null`, `{"label":""}`} {
		var o QuestionOption
		if err := json.Unmarshal([]byte(in), &o); err == nil {
			t.Fatalf("unmarshal(%s) = nil error, want error", in)
		}
	}
}

func TestQuestionOptionNullInArrayIsRejected(t *testing.T) {
	var q Question
	err := json.Unmarshal([]byte(`{"question":"pick","options":["a",null]}`), &q)
	if err == nil {
		t.Fatalf("unmarshal with null option = nil error, want error; got %+v", q.Options)
	}
}

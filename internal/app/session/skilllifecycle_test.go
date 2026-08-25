package session

import (
	"reflect"
	"testing"
)

func TestDeactivateSkillRemovesFromActive(t *testing.T) {
	s := &State{activeSkills: map[string]bool{}, skillOrder: map[string]int64{}, skillBodyAge: map[string]int{}}
	s.ActivateSkill("alpha")
	if !s.HasActiveSkill("alpha") {
		t.Fatal("alpha should be active after ActivateSkill")
	}
	if !s.DeactivateSkill("alpha") {
		t.Fatal("DeactivateSkill should report true when it removed a skill")
	}
	if s.HasActiveSkill("alpha") {
		t.Error("alpha should not be active after DeactivateSkill")
	}
	if s.DeactivateSkill("alpha") {
		t.Error("DeactivateSkill should report false for an inactive skill")
	}
}

func TestActiveSkillsByAgeIsActivationOrder(t *testing.T) {
	s := &State{activeSkills: map[string]bool{}, skillOrder: map[string]int64{}, skillBodyAge: map[string]int{}}
	s.ActivateSkill("first")
	s.ActivateSkill("second")
	s.ActivateSkill("third")

	got := s.ActiveSkillsByAge()
	want := []string{"first", "second", "third"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ActiveSkillsByAge = %v, want %v", got, want)
	}
}

func TestReActivatingDoesNotChangeAge(t *testing.T) {
	s := &State{activeSkills: map[string]bool{}, skillOrder: map[string]int64{}, skillBodyAge: map[string]int{}}
	s.ActivateSkill("first")
	s.ActivateSkill("second")
	s.ActivateSkill("first") // re-activation must not make "first" the newest

	got := s.ActiveSkillsByAge()
	want := []string{"first", "second"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ActiveSkillsByAge = %v, want %v", got, want)
	}
}

// ActiveSkills must stay alphabetically sorted regardless of activation
// order: it renders into messages[0], the provider's cache prefix.
func TestActiveSkillsStaysSortedNotActivationOrdered(t *testing.T) {
	s := &State{activeSkills: map[string]bool{}, skillOrder: map[string]int64{}, skillBodyAge: map[string]int{}}
	s.ActivateSkill("zebra")
	s.ActivateSkill("alpha")

	got := s.ActiveSkills()
	want := []string{"alpha", "zebra"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ActiveSkills = %v, want %v (sort is load-bearing for prefix caching)", got, want)
	}
}

func TestSkillBodyAgeTicksAndResets(t *testing.T) {
	s := &State{
		activeSkills: map[string]bool{},
		skillOrder:   map[string]int64{},
		skillBodyAge: map[string]int{},
	}
	s.ActivateSkill("alpha")
	if got := s.SkillBodyAge("alpha"); got != 0 {
		t.Fatalf("fresh skill age = %d, want 0", got)
	}

	s.TickSkillBodyAges()
	s.TickSkillBodyAges()
	if got := s.SkillBodyAge("alpha"); got != 2 {
		t.Fatalf("age after two ticks = %d, want 2", got)
	}

	s.ResetSkillBodyAge("alpha")
	if got := s.SkillBodyAge("alpha"); got != 0 {
		t.Fatalf("age after reset = %d, want 0", got)
	}
}

func TestResetAllSkillBodyAges(t *testing.T) {
	s := &State{
		activeSkills: map[string]bool{},
		skillOrder:   map[string]int64{},
		skillBodyAge: map[string]int{},
	}
	s.ActivateSkill("alpha")
	s.ActivateSkill("beta")
	s.TickSkillBodyAges()
	s.ResetAllSkillBodyAges()

	for _, n := range []string{"alpha", "beta"} {
		if got := s.SkillBodyAge(n); got != 0 {
			t.Errorf("age of %s after ResetAll = %d, want 0", n, got)
		}
	}
}

package agent

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/skills"
)

func TestAppendSkillBodiesSendsFullBodyWhenYoung(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("tdd", skills.Skill{Name: "tdd", Description: "d", Body: "FULL BODY TEXT"})
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.Config.Skills.BodyFullTurns = 3
	state.ActivateSkill("tdd")
	r := &Runner{SkillIndex: idx, State: state}

	got := r.appendSkillBodies(nil)

	if len(got) != 1 || !strings.Contains(got[0].Content, "FULL BODY TEXT") {
		t.Fatalf("young skill must send its full body, got %+v", got)
	}
}

func TestAppendSkillBodiesSendsStubWhenAged(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("tdd", skills.Skill{Name: "tdd", Description: "d", Body: "FULL BODY TEXT"})
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.Config.Skills.BodyFullTurns = 2
	state.ActivateSkill("tdd")
	state.TickSkillBodyAges()
	state.TickSkillBodyAges()
	r := &Runner{SkillIndex: idx, State: state}

	got := r.appendSkillBodies(nil)

	if len(got) != 1 {
		t.Fatalf("aged skill must still emit a message, got %d", len(got))
	}
	if strings.Contains(got[0].Content, "FULL BODY TEXT") {
		t.Error("aged skill must not resend its full body")
	}
	if !strings.Contains(got[0].Content, "skill.load") {
		t.Error("stub must tell the model how to get the body back")
	}
	if !strings.Contains(got[0].Content, "tdd") {
		t.Error("stub must name the skill")
	}
}

func TestBodyFullTurnsZeroAlwaysSendsFullBody(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("tdd", skills.Skill{Name: "tdd", Description: "d", Body: "FULL BODY TEXT"})
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.Config.Skills.BodyFullTurns = 0
	state.ActivateSkill("tdd")
	for i := 0; i < 20; i++ {
		state.TickSkillBodyAges()
	}
	r := &Runner{SkillIndex: idx, State: state}

	got := r.appendSkillBodies(nil)

	if len(got) != 1 || !strings.Contains(got[0].Content, "FULL BODY TEXT") {
		t.Fatal("BodyFullTurns=0 must always send the full body")
	}
}

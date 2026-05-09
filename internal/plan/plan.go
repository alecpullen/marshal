// Package plan provides audit-to-fix integration with discrete goal tracking.
// It parses audit findings, generates fix goals, and tracks progress.
package plan

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/alecpullen/marshal/internal/session"
)

// Priority levels for goals
const (
	PriorityCritical = "critical"
	PriorityHigh     = "high"
	PriorityMedium   = "medium"
	PriorityLow      = "low"
)

// Status values for goals
const (
	GoalPending   = "pending"
	GoalActive    = "active"
	GoalCompleted = "completed"
	GoalFailed    = "failed"
	GoalSkipped   = "skipped"
)

// Goal represents a discrete fix task generated from audit findings
type Goal struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Priority    string    `json:"priority"`
	Files       []string  `json:"files"`
	DependsOn   []string  `json:"depends_on"`
	Findings    []Finding `json:"findings"`
	Status      string    `json:"status"`
	TaskID      *string   `json:"task_id,omitempty"`
}

// Finding represents a single audit finding
type Finding struct {
	Severity    string   `json:"severity"`
	Category    string   `json:"category"`
	File        string   `json:"file"`
	Line        int      `json:"line,omitempty"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Impact      string   `json:"impact,omitempty"`
	Files       []string `json:"files,omitempty"`
}

// Plan represents a generated fix plan from audit findings
type Plan struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	SourceTask  string    `json:"source_task"` // Original audit task ID
	Goals       []*Goal   `json:"goals"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Progress represents the overall completion status
type Progress struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"`
	Active    int `json:"active"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
}

// CalculateProgress computes completion statistics for a plan
func (p *Plan) CalculateProgress() Progress {
	prog := Progress{Total: len(p.Goals)}
	for _, g := range p.Goals {
		switch g.Status {
		case GoalPending:
			prog.Pending++
		case GoalActive:
			prog.Active++
		case GoalCompleted:
			prog.Completed++
		case GoalFailed:
			prog.Failed++
		case GoalSkipped:
			prog.Skipped++
		}
	}
	return prog
}

// IsComplete returns true if all goals are completed, failed, or skipped
func (p *Plan) IsComplete() bool {
	for _, g := range p.Goals {
		if g.Status == GoalPending || g.Status == GoalActive {
			return false
		}
	}
	return len(p.Goals) > 0
}

// NextGoal returns the next pending goal that has all dependencies satisfied
func (p *Plan) NextGoal() *Goal {
	// Build map of completed goals
	completed := make(map[string]bool)
	for _, g := range p.Goals {
		if g.Status == GoalCompleted {
			completed[g.ID] = true
		}
	}

	// Find first pending goal with all dependencies met
	for _, g := range p.Goals {
		if g.Status != GoalPending {
			continue
		}

		// Check if all dependencies are satisfied
		depsMet := true
		for _, depID := range g.DependsOn {
			if !completed[depID] {
				depsMet = false
				break
			}
		}

		if depsMet {
			return g
		}
	}
	return nil
}

// GetGoalByID finds a goal by its ID
func (p *Plan) GetGoalByID(goalID string) *Goal {
	for _, g := range p.Goals {
		if g.ID == goalID {
			return g
		}
	}
	return nil
}

// Generator creates fix plans from audit findings
type Generator struct {
	store *session.Store
}

// NewGenerator creates a plan generator with the given session store.
func NewGenerator(store *session.Store) *Generator {
	return &Generator{store: store}
}

// GenerateFromAudit creates a fix plan from audit output text.
// It parses findings and generates discrete goals.
func (g *Generator) GenerateFromAudit(sessionID string, sourceTaskID string, auditOutput string) (*Plan, error) {
	// Parse findings from audit output
	findings := ParseAuditFindings(auditOutput)

	if len(findings) == 0 {
		return nil, fmt.Errorf("no audit findings detected in output")
	}

	planID := generateID()
	plan := &Plan{
		ID:          planID,
		SessionID:   sessionID,
		Name:        fmt.Sprintf("Fix Plan from %s", sourceTaskID),
		Description: fmt.Sprintf("Generated from audit with %d findings", len(findings)),
		SourceTask:  sourceTaskID,
		Goals:       make([]*Goal, 0),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	// Group findings by severity and category
	groups := groupFindings(findings)

	// Create goals for each group
	goalIndex := 0
	for _, group := range groups {
		goal := createGoalFromFindings(planID, sessionID, group, goalIndex)
		plan.Goals = append(plan.Goals, goal)
		goalIndex++
	}

	// Set up dependencies (critical goals should be done first, then high, etc.)
	setGoalDependencies(plan.Goals)

	// Store in database
	if err := g.storePlan(plan); err != nil {
		return nil, err
	}

	return plan, nil
}

// storePlan saves a plan and its goals to the database
func (g *Generator) storePlan(p *Plan) error {
	// Convert goals to JSON for storage
	goalsJSON, err := json.Marshal(p.Goals)
	if err != nil {
		return fmt.Errorf("marshaling goals: %w", err)
	}

	sessionPlan := &session.Plan{
		ID:          p.ID,
		SessionID:   p.SessionID,
		Name:        p.Name,
		Description: p.Description,
		SourceTask:  p.SourceTask,
		GoalsJSON:   string(goalsJSON),
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}

	if err := g.store.InsertPlan(sessionPlan); err != nil {
		return fmt.Errorf("inserting plan: %w", err)
	}

	// Store individual goals
	for _, goal := range p.Goals {
		if err := g.storeGoal(p.SessionID, p.ID, goal); err != nil {
			return err
		}
	}

	return nil
}

// storeGoal saves a single goal to the database
func (g *Generator) storeGoal(sessionID, planID string, goal *Goal) error {
	filesJSON, _ := json.Marshal(goal.Files)
	dependsJSON, _ := json.Marshal(goal.DependsOn)
	findingsJSON, _ := json.Marshal(goal.Findings)

	sessionGoal := &session.Goal{
		ID:           goal.ID,
		PlanID:       planID,
		SessionID:    sessionID,
		Title:        goal.Title,
		Description:  goal.Description,
		Priority:     string(goal.Priority),
		Status:       string(goal.Status),
		FilesJSON:    string(filesJSON),
		DependsJSON:  string(dependsJSON),
		CreatedAt:    time.Now(),
		FindingsJSON: string(findingsJSON),
	}

	return g.store.InsertGoal(sessionGoal)
}

// LoadPlan retrieves a plan from the database by ID
func (g *Generator) LoadPlan(planID string) (*Plan, error) {
	// Get goals from database
	goals, err := g.store.GetGoalsForPlan(planID)
	if err != nil {
		return nil, err
	}

	// Convert session.Goal to plan.Goal
	planGoals := make([]*Goal, len(goals))
	for i, sg := range goals {
		var files []string
		var depends []string
		var findings []Finding
		json.Unmarshal([]byte(sg.FilesJSON), &files)
		json.Unmarshal([]byte(sg.DependsJSON), &depends)
		json.Unmarshal([]byte(sg.FindingsJSON), &findings)

		planGoals[i] = &Goal{
			ID:          sg.ID,
			Title:       sg.Title,
			Description: sg.Description,
			Priority:    sg.Priority,
			Files:       files,
			DependsOn:   depends,
			Findings:    findings,
			Status:      sg.Status,
			TaskID:      sg.TaskID,
		}
	}

	// Get plan info from first goal (they all share same plan)
	if len(goals) == 0 {
		return nil, fmt.Errorf("no goals found for plan %s", planID)
	}

	return &Plan{
		ID:        planID,
		SessionID: goals[0].SessionID,
		Goals:     planGoals,
	}, nil
}

// LoadActivePlanForSession retrieves the most recently updated plan for a session
func (g *Generator) LoadActivePlanForSession(sessionID string) (*Plan, error) {
	sp, err := g.store.GetActivePlanForSession(sessionID)
	if err != nil {
		return nil, err
	}
	if sp == nil {
		return nil, nil
	}

	return g.LoadPlan(sp.ID)
}

// UpdateGoalStatus updates a goal's status
func (g *Generator) UpdateGoalStatus(goalID string, status string, taskID *string) error {
	return g.store.UpdateGoalStatus(goalID, status, taskID)
}

// ParseAuditFindings extracts security findings from audit output text
func ParseAuditFindings(auditOutput string) []Finding {
	var findings []Finding
	seenTitles := make(map[string]bool)

	// Pattern 1: Markdown headings with severity inside: ### 1. **CRITICAL: Title**
	// or ### **HIGH** - Title
	// or **CRITICAL:** Title (with colon outside the bold)
	headingPattern := regexp.MustCompile(`(?i)(?:^|\n)(?:#{1,3}\s*(?:\d+\.?\s*)?)?\*\*(CRITICAL|HIGH|MEDIUM|LOW)\s*[:\-]?\s*([^*\n]*)\*\*(?:\s*[:\-]?\s*([^\n]*))?`)
	headingMatches := headingPattern.FindAllStringSubmatch(auditOutput, -1)

	for _, m := range headingMatches {
		severity := strings.ToLower(m[1])
		// Title can be either inside the bold (m[2]) or after it (m[3])
		title := strings.TrimSpace(m[2])
		if title == "" && len(m) > 3 {
			title = strings.TrimSpace(m[3])
		}
		if title == "" {
			continue
		}

		// Deduplicate by title
		if seenTitles[title] {
			continue
		}
		seenTitles[title] = true

		findings = append(findings, Finding{
			Severity: severity,
			Title:    title,
		})
	}

	// Pattern 2: Standalone severity lines: **CRITICAL**: Description
	// (severity in bold, colon after, description follows)
	standalonePattern := regexp.MustCompile(`(?i)(?:^|\n)\s*[-*]*\s*\*\*(CRITICAL|HIGH|MEDIUM|LOW)\*\*\s*[:\-]\s*([^\n]+)`)
	standaloneMatches := standalonePattern.FindAllStringSubmatch(auditOutput, -1)

	for _, m := range standaloneMatches {
		severity := strings.ToLower(m[1])
		title := strings.TrimSpace(m[2])

		// Check for duplicates
		if seenTitles[title] {
			continue
		}
		seenTitles[title] = true

		findings = append(findings, Finding{
			Severity: severity,
			Title:    title,
		})
	}

	// Pattern 3: Table rows with severity: | **CRITICAL** | Finding |
	tablePattern := regexp.MustCompile(`(?i)\|\s*\*\*(CRITICAL|HIGH|MEDIUM|LOW)\*\*\s*\|\s*([^|]+)\|`)
	tableMatches := tablePattern.FindAllStringSubmatch(auditOutput, -1)

	for _, m := range tableMatches {
		severity := strings.ToLower(m[1])
		title := strings.TrimSpace(m[2])

		if seenTitles[title] {
			continue
		}
		seenTitles[title] = true

		findings = append(findings, Finding{
			Severity: severity,
			Title:    title,
		})
	}

	// Extract file paths and descriptions for each finding
	// We need to associate files with the correct finding
	filePattern := regexp.MustCompile(`(?i)(?:^|\n)\s*(?:[-*]\s*)?(?:\*\*)?(?:File|Location|Path)(?:\*\*)?\s*[:\-]?\s*[` + "`" + `"']?([^` + "`" + `'"\n\(]+)[` + "`" + `"']?`)
	fileMatches := filePattern.FindAllStringSubmatchIndex(auditOutput, -1)

	// For each finding, find the closest file reference after it
	for i := range findings {
		// Find heading position for this finding
		var headingPos int
		if i < len(findings)-1 {
			// Find position of next heading to limit search
			nextTitle := findings[i+1].Title
			nextIdx := strings.Index(auditOutput, nextTitle)
			if nextIdx > headingPos {
				headingPos = nextIdx
			}
		}

		// Look for file references between this finding and the next one
		for _, fm := range fileMatches {
			if fm[0] >= headingPos && (i == len(findings)-1 || fm[0] < strings.Index(auditOutput, findings[i+1].Title)) {
				findings[i].File = strings.TrimSpace(auditOutput[fm[2]:fm[3]])
				break
			}
		}
	}

	return findings
}

// groupFindings groups similar findings together for goal generation
func groupFindings(findings []Finding) [][]Finding {
	// Simple grouping by severity
	groups := make(map[string][]Finding)
	for _, f := range findings {
		key := f.Severity
		if f.Category != "" {
			key = f.Severity + ":" + f.Category
		}
		groups[key] = append(groups[key], f)
	}

	// Convert map to slice (ordered by severity)
	result := make([][]Finding, 0)
	severities := []string{PriorityCritical, PriorityHigh, PriorityMedium, PriorityLow}
	for _, sev := range severities {
		for key, group := range groups {
			if strings.HasPrefix(key, sev) && len(group) > 0 {
				result = append(result, group)
			}
		}
	}

	return result
}

// createGoalFromFindings creates a goal from a group of related findings
func createGoalFromFindings(planID, sessionID string, findings []Finding, index int) *Goal {
	if len(findings) == 0 {
		return nil
	}

	// Collect unique files
	fileSet := make(map[string]bool)
	for _, f := range findings {
		if f.File != "" {
			fileSet[f.File] = true
		}
		for _, ff := range f.Files {
			fileSet[ff] = true
		}
	}
	files := make([]string, 0, len(fileSet))
	for f := range fileSet {
		files = append(files, f)
	}

	// Determine priority from first finding
	priority := findings[0].Severity
	if priority == "" {
		priority = PriorityMedium
	}

	// Create goal title based on finding category
	title := findings[0].Title
	if len(findings) > 1 {
		title = fmt.Sprintf("Fix %d %s issues", len(findings), priority)
	}

	// Build description
	desc := "Fix the following issues:\n"
	for _, f := range findings {
		desc += fmt.Sprintf("- %s\n", f.Title)
		if f.Description != "" {
			desc += fmt.Sprintf("  %s\n", f.Description)
		}
	}

	return &Goal{
		ID:          fmt.Sprintf("%s-goal-%d", planID[:8], index),
		Title:       title,
		Description: desc,
		Priority:    priority,
		Files:       files,
		DependsOn:   []string{}, // Will be set by setGoalDependencies
		Findings:    findings,
		Status:      GoalPending,
	}
}

// setGoalDependencies establishes dependencies between goals
// Critical goals have no dependencies, others depend on more critical ones
func setGoalDependencies(goals []*Goal) {
	// Group goals by priority
	byPriority := make(map[string][]*Goal)
	for _, g := range goals {
		byPriority[g.Priority] = append(byPriority[g.Priority], g)
	}

	// Set up chain: low -> medium -> high -> critical
	priorities := []string{PriorityLow, PriorityMedium, PriorityHigh, PriorityCritical}
	for i := 1; i < len(priorities); i++ {
		lowerGoals := byPriority[priorities[i-1]]
		higherGoals := byPriority[priorities[i]]

		// Lower priority goals depend on at least one higher priority goal
		if len(higherGoals) > 0 && len(lowerGoals) > 0 {
			depID := higherGoals[0].ID
			for _, g := range lowerGoals {
				g.DependsOn = append(g.DependsOn, depID)
			}
		}
	}
}

// generateID creates a unique ID
func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// FormatPlanStatus returns a formatted status string for display
func FormatPlanStatus(p *Plan) string {
	if p == nil {
		return "No active plan"
	}

	prog := p.CalculateProgress()
	status := fmt.Sprintf("Plan: %s\n", p.Name)
	status += fmt.Sprintf("Progress: %d/%d goals\n", prog.Completed, prog.Total)
	status += fmt.Sprintf("  Pending: %d | Active: %d | Completed: %d | Failed: %d\n",
		prog.Pending, prog.Active, prog.Completed, prog.Failed)

	// Show next goal
	if next := p.NextGoal(); next != nil {
		status += fmt.Sprintf("\nNext: [%s] %s\n", next.Priority, next.Title)
	}

	return status
}

// GetGoalPrompt generates a task prompt for a goal
func GetGoalPrompt(g *Goal) string {
	prompt := fmt.Sprintf("Fix the following security issue: %s\n\n", g.Title)
	prompt += fmt.Sprintf("Priority: %s\n\n", g.Priority)
	prompt += "Description:\n" + g.Description + "\n\n"

	if len(g.Files) > 0 {
		prompt += "Related files:\n"
		for _, f := range g.Files {
			prompt += fmt.Sprintf("- %s\n", f)
		}
		prompt += "\n"
	}

	if len(g.Findings) > 0 {
		prompt += "Specific findings to address:\n"
		for i, f := range g.Findings {
			prompt += fmt.Sprintf("%d. [%s] %s\n", i+1, f.Severity, f.Title)
			if f.Description != "" {
				prompt += fmt.Sprintf("   %s\n", f.Description)
			}
		}
	}

	prompt += "\nImplement the fix following best practices for security."
	return prompt
}

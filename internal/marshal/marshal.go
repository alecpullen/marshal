// Package marshal provides the primary orchestrator.
// The Marshal receives tasks from users, plans implementations, and commands the agents.
//
// Architecture:
//   - User talks to the Marshal
//   - Marshal commands the agents (Executor, Critic, Compactor)
//   - Agents report back to the Marshal
//   - Marshal makes decisions (retry, commit, revert)
//
// This package replaces the loop-centric architecture with a commander-centric one.
package marshal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/alecpullen/marshal/internal/agents/compactor"
	"github.com/alecpullen/marshal/internal/agents/critic"
	"github.com/alecpullen/marshal/internal/agents/executor"
	"github.com/alecpullen/marshal/internal/agents/planner"
	"github.com/alecpullen/marshal/internal/backend"
	"github.com/alecpullen/marshal/internal/config"
	"github.com/alecpullen/marshal/internal/conversation"
	"github.com/alecpullen/marshal/internal/git"
	"github.com/alecpullen/marshal/internal/store"
	"github.com/alecpullen/marshal/internal/types"
)

// Marshal is the primary orchestrator. The user talks to the Marshal;
// the Marshal commands the agents.
type Marshal struct {
	cfg         *config.Config
	backend     backend.Backend            // marshal's own backend for conversational responses
	toolBackend backend.ToolCapableBackend // for autonomous exploration with tools
	agents      *AgentSet
	git         git.Layer
	store       *store.Store
	repoRoot    string
}

// AgentSet holds all the agents the Marshal can command.
type AgentSet struct {
	Executor  *executor.Executor
	Critic    *critic.Critic
	Compactor *compactor.Compactor
}

// Session represents a single task execution managed by the Marshal.
type Session struct {
	ID               string
	Task             string
	Rounds           []Round
	Status           string // PENDING, RUNNING, SUCCESS, EXHAUSTED, FAILED
	Branch           string // Isolation branch name
	PromptTokens     int
	CompletionTokens int
}

// Round represents a single executor-critic cycle within a session.
type Round struct {
	Number       int
	ExecutorReq  executor.Request
	ExecutorResp string
	Diff         string
	CriticResp   string
	Verdict      critic.Verdict
	Tokens       TokenUsage
	ThinkBlock   string
}

// TokenUsage tracks prompt and completion tokens for a round.
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
}

// Result is the final outcome of a Marshal session.
type Result struct {
	Status       string // "SUCCESS", "FAILED", "EXHAUSTED"
	FinalVerdict *critic.Verdict
	Rounds       []Round
	TotalTokens  TokenUsage
	SHA          string // Git commit SHA on success
}

// MarshalResponse is the Marshal's response to a user message in conversation mode
type MarshalResponse struct {
	Type         conversation.ResponseType
	Content      string
	ThinkContent string // Extracted <think>...</think> block, if any
	Questions    []string
	TaskPlans    []conversation.TaskPlan
	PlanGraph    *planner.TaskGraph
	Intent       conversation.Intent
	ExecuteTask  *string // Set if Marshal decided to execute a task directly
}

// New creates a new Marshal with the given configuration and dependencies.
func New(cfg *config.Config, gitLayer git.Layer, s *store.Store, skills []types.Skill) *Marshal {
	// Load stored model preferences and override cfg values for each role.
	execCfg := cfg.Executor
	criticCfg := cfg.Critic
	marshalCfg := cfg.GetMarshalConfig()
	plannerCfg := cfg.Planner

	if s != nil {
		applyPref := func(ac *config.AgentConfig, role string) {
			if pref, err := s.GetModelPreference(role); err == nil && pref != nil {
				ac.Model = pref.Model
				ac.Provider = pref.Provider
				if pref.BaseURL != "" {
					ac.BaseURL = pref.BaseURL
				}
			}
		}
		applyPref(&execCfg, "executor")
		applyPref(&criticCfg, "critic")
		applyPref(&marshalCfg, "marshal")
		applyPref(&plannerCfg, "planner")
	}

	// Setup backends for agents using the factory
	// Each role can use a different provider (ollama, fireworks, openai, etc.)

	execBackend := mustCreateBackend(execCfg.Provider, "executor", execCfg)
	criticBackend := mustCreateBackend(criticCfg.Provider, "critic", criticCfg)
	marshalBackend := mustCreateBackend(marshalCfg.Provider, "marshal", config.AgentConfig{
		Provider:    marshalCfg.Provider,
		Model:       marshalCfg.Model,
		BaseURL:     marshalCfg.BaseURL,
		APIKey:      marshalCfg.APIKey,
		Temperature: 0.7,
		MaxTokens:   2048,
		JSONOutput:  false,
	})

	// Compactor uses executor config by default (can be overridden in future)
	compactorBackend := mustCreateBackend(execCfg.Provider, "compactor", config.AgentConfig{
		Provider:    execCfg.Provider,
		Model:       execCfg.Model,
		BaseURL:     execCfg.BaseURL,
		APIKey:      execCfg.APIKey,
		Temperature: 0.0,
		MaxTokens:   2048,
	})
	_ = plannerCfg // plannerCfg used when planner backend is created in future

	m := &Marshal{
		cfg:      cfg,
		backend:  marshalBackend,
		git:      gitLayer,
		store:    s,
		repoRoot: cfg.RepoRoot,
		agents: &AgentSet{
			Executor:  executor.New(execBackend, execCfg, skills, cfg.RepoRoot),
			Critic:    critic.New(criticBackend, criticCfg),
			Compactor: compactor.New(compactorBackend, execCfg),
		},
	}

	// Initialize tool backend if marshal's backend supports tools
	if tb := backend.AsToolCapable(marshalBackend); tb != nil {
		m.toolBackend = tb
	}

	return m
}

// mustCreateBackend creates a backend for an agent role.
// It applies role-specific configuration (temperature, max_tokens, json_output).
func mustCreateBackend(provider, name string, cfg config.AgentConfig) backend.Backend {
	be, err := backend.FactoryForAgent(provider, name, cfg.BaseURL, cfg.APIKey,
		cfg.Temperature, cfg.MaxTokens, cfg.JSONOutput, cfg.ContextWindow)
	if err != nil {
		// In production, this should be handled gracefully
		// For now, panic on factory error during initialization
		panic(fmt.Sprintf("failed to create %s backend: %v", name, err))
	}
	return be
}

// ProcessMessage handles a user message in the context of a conversation.
// This is the primary entry point for the agent-centric conversational model.
func (m *Marshal) ProcessMessage(ctx context.Context, conv *conversation.Conversation, userMsg string) (*MarshalResponse, error) {
	// Classify intent before adding the message (classification reads existing history)
	intent := conversation.ClassifyIntent(userMsg, conv)

	// Record user message now so buildChatMessages sees it once in history
	conv.AddMessage("user", userMsg)

	// Update conversation context
	m.updateContext(conv, userMsg)

	var resp *MarshalResponse
	var err error

	switch intent {
	case conversation.IntentCancel:
		resp, err = m.handleCancel(conv, userMsg)

	case conversation.IntentConfirm:
		resp, err = m.handleConfirm(conv, userMsg)

	case conversation.IntentDecline:
		resp, err = m.handleDecline(conv, userMsg)

	case conversation.IntentProvideContext:
		resp, err = m.handleContextProvision(conv, userMsg)

	case conversation.IntentRequestWork:
		resp, err = m.handleWorkRequest(ctx, conv, userMsg)

	default: // IntentChat
		resp, err = m.handleChat(ctx, conv, userMsg)
	}

	// Store assistant reply in conversation history so future turns have full context
	if err == nil && resp != nil {
		conv.AddMessage("marshal", resp.Content)
	}

	return resp, err
}

// updateContext updates the conversation context based on user message
func (m *Marshal) updateContext(conv *conversation.Conversation, msg string) {
	// Extract files mentioned
	files := conversation.ExtractFilesFromMessage(msg)
	conv.Context.FilesMentioned = append(conv.Context.FilesMentioned, files...)

	// Simple confidence scoring based on message characteristics
	score := 0.5
	if len(files) > 0 {
		score += 0.1 * float64(len(files))
	}
	if len(msg) > 50 {
		score += 0.1
	}
	if conversation.IsComplexRequest(msg, conv.Context) {
		score -= 0.2 // Complex requests reduce confidence until clarified
	}
	if score > 1.0 {
		score = 1.0
	}
	if score < 0.0 {
		score = 0.0
	}
	conv.Context.Confidence = score
}

// handleCancel handles cancel intent
func (m *Marshal) handleCancel(conv *conversation.Conversation, msg string) (*MarshalResponse, error) {
	conv.State = conversation.StateChatting
	conv.Context.OpenQuestions = nil
	conv.PendingTasks = nil

	return &MarshalResponse{
		Type:    conversation.ResponseChat,
		Content: "Cancelled. What would you like to do instead?",
		Intent:  conversation.IntentCancel,
	}, nil
}

// handleConfirm handles confirmation intent (user approves a plan)
func (m *Marshal) handleConfirm(conv *conversation.Conversation, msg string) (*MarshalResponse, error) {
	if len(conv.PendingTasks) == 0 {
		return &MarshalResponse{
			Type:    conversation.ResponseChat,
			Content: "I don't have any pending tasks to confirm. What would you like me to do?",
			Intent:  conversation.IntentConfirm,
		}, nil
	}

	// User confirmed - move to executing state
	conv.State = conversation.StateExecuting

	// Check if we should auto-execute
	canAuto := conv.CanAutoExecute()

	if canAuto {
		// Execute immediately
		return &MarshalResponse{
			Type:        conversation.ResponseTaskProgress,
			Content:     fmt.Sprintf("Starting %d task(s)...", len(conv.PendingTasks)),
			TaskPlans:   conv.PendingTasks,
			Intent:      conversation.IntentConfirm,
			ExecuteTask: &conv.PendingTasks[0].Description, // Signal to execute first task
		}, nil
	}

	// Return the confirmed task plans for execution
	return &MarshalResponse{
		Type:      conversation.ResponseTaskProgress,
		Content:   fmt.Sprintf("Confirmed. Starting %d task(s).", len(conv.PendingTasks)),
		TaskPlans: conv.PendingTasks,
		Intent:    conversation.IntentConfirm,
	}, nil
}

// handleDecline handles decline intent (user rejects a plan)
func (m *Marshal) handleDecline(conv *conversation.Conversation, msg string) (*MarshalResponse, error) {
	conv.State = conversation.StateChatting
	conv.PendingTasks = nil

	return &MarshalResponse{
		Type:    conversation.ResponseChat,
		Content: "No problem. Let me know how you'd like to proceed instead.",
		Intent:  conversation.IntentDecline,
	}, nil
}

// handleContextProvision handles when user provides context/answers questions
func (m *Marshal) handleContextProvision(conv *conversation.Conversation, msg string) (*MarshalResponse, error) {
	// Clear the answered question
	if len(conv.Context.OpenQuestions) > 0 {
		conv.Context.OpenQuestions = conv.Context.OpenQuestions[1:]
	}

	// Check if we now have sufficient context to proceed
	hasGoal := conv.Context.UserGoal != ""
	hasFiles := len(conv.Context.FilesMentioned) > 0
	noMoreQuestions := len(conv.Context.OpenQuestions) == 0

	// If we have a goal and files, proceed to planning regardless of remaining questions
	if hasGoal && hasFiles {
		conv.State = conversation.StatePlanning
		conv.Context.Confidence = minFloat(conv.Context.Confidence+0.3, 1.0)
		return m.planWork(conv)
	}

	// If no more questions but still missing critical info, ask specifically about files
	if noMoreQuestions && hasGoal && !hasFiles {
		conv.Context.OpenQuestions = append(conv.Context.OpenQuestions, "Which file(s) should I work on?")
		return &MarshalResponse{
			Type:      conversation.ResponseClarification,
			Content:   "Which file(s) should I work on?",
			Questions: conv.Context.OpenQuestions,
			Intent:    conversation.IntentProvideContext,
		}, nil
	}

	// If no more questions and no goal, treat this as a chat
	if noMoreQuestions && !hasGoal {
		conv.State = conversation.StateChatting
		return &MarshalResponse{
			Type:    conversation.ResponseChat,
			Content: "Thanks for the info. What would you like me to do?",
			Intent:  conversation.IntentProvideContext,
		}, nil
	}

	// Still have questions to answer
	return &MarshalResponse{
		Type:      conversation.ResponseClarification,
		Content:   "Thanks for the clarification. " + conv.Context.OpenQuestions[0],
		Questions: conv.Context.OpenQuestions,
		Intent:    conversation.IntentProvideContext,
	}, nil
}

// isCodebaseWideRequest detects if the user is asking about the entire project/codebase
func isCodebaseWideRequest(msg string) bool {
	msgLower := strings.ToLower(msg)
	codebasePhrases := []string{
		"this codebase", "this project", "this directory", "this repo", "this repository",
		"the codebase", "the project", "the directory", "the repo", "the repository",
		"all files", "everything", "whole project", "entire project",
		"analyze codebase", "analyse codebase", "analyze project", "analyse project",
		"explore codebase", "explore project", "understand codebase", "understand project",
		"review codebase", "review project", "codebase analysis", "project analysis",
	}
	for _, phrase := range codebasePhrases {
		if strings.Contains(msgLower, phrase) {
			return true
		}
	}
	return false
}

// handleWorkRequest handles when user wants work done
func (m *Marshal) handleWorkRequest(ctx context.Context, conv *conversation.Conversation, msg string) (*MarshalResponse, error) {
	// Store the user's goal (only if not already set - preserves original goal during clarification)
	if conv.Context.UserGoal == "" {
		conv.Context.UserGoal = msg
	}

	// Check if we should operate autonomously
	orchestratorCfg := m.cfg.Orchestrator
	isAutonomous := orchestratorCfg.GetMode() == "autonomous"
	lowConfidence := conv.Context.Confidence < orchestratorCfg.GetMinConfidence()
	isComplex := conversation.IsComplexRequest(msg, conv.Context)
	// Check total files in context (from all previous messages), not just current message
	hasFilesInContext := len(conv.Context.FilesMentioned) > 0

	// If this is a codebase-wide request, mark repoRoot as the implicit file context
	// and proceed directly to exploration/planning without asking "which files"
	isCodebaseWide := isCodebaseWideRequest(conv.Context.UserGoal)
	if isCodebaseWide && !hasFilesInContext {
		// Mark the repo root as the file scope - executor will explore from here
		conv.Context.FilesMentioned = append(conv.Context.FilesMentioned, m.repoRoot)
		conv.Context.Confidence = 0.8 // High confidence for codebase-wide requests
		hasFilesInContext = true
	}

	// In autonomous mode, explore when we lack confidence OR no files are in context
	// This ensures "analyze this codebase" triggers exploration instead of asking questions
	shouldExplore := isAutonomous && m.toolBackend != nil && (lowConfidence || !hasFilesInContext)

	if shouldExplore {
		conv.State = conversation.StateExploring

		// Perform autonomous exploration
		exploration, err := m.ExploreForTask(ctx, conv.Context.UserGoal)
		if err != nil {
			// Exploration failed - fall back to asking questions
			return m.askClarifyingQuestions(conv, msg)
		}

		// Update context with exploration findings
		conv.Context.ExplorationResult = &conversation.ExplorationSummary{
			Summary:      exploration.Summary,
			FilesFound:   exploration.FilesFound,
			Architecture: exploration.Architecture,
			Confidence:   exploration.Confidence,
			StepsTaken:   exploration.StepsTaken,
		}
		conv.Context.FilesMentioned = append(conv.Context.FilesMentioned, exploration.FilesFound...)

		// Increase confidence based on exploration
		if exploration.Confidence > 0 {
			conv.Context.Confidence = minFloat(conv.Context.Confidence+exploration.Confidence*0.3, 1.0)
		}

		// If exploration was successful, proceed with planning
		if exploration.Confidence >= 0.5 {
			conv.State = conversation.StatePlanning
			return m.planWorkWithExploration(conv, exploration)
		}

		// Exploration didn't yield enough info - fall back to asking
		return m.askClarifyingQuestions(conv, msg)
	}

	// Interactive mode or no tool backend: use traditional clarifying questions
	// Only ask questions if we lack files OR (isComplex AND low confidence)
	if !hasFilesInContext || (isComplex && lowConfidence) {
		return m.askClarifyingQuestions(conv, msg)
	}

	// We have sufficient info (files in context) - proceed to planning
	conv.State = conversation.StatePlanning
	return m.planWork(conv)
}

// askClarifyingQuestions generates and returns clarifying questions to the user.
func (m *Marshal) askClarifyingQuestions(conv *conversation.Conversation, msg string) (*MarshalResponse, error) {
	conv.State = conversation.StateClarifying

	questions := m.generateQuestions(conv, msg)
	conv.Context.OpenQuestions = questions

	return &MarshalResponse{
		Type:      conversation.ResponseClarification,
		Content:   "I want to make sure I understand correctly. " + questions[0],
		Questions: questions,
		Intent:    conversation.IntentRequestWork,
	}, nil
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// generateQuestions creates clarifying questions based on context
func (m *Marshal) generateQuestions(conv *conversation.Conversation, msg string) []string {
	var questions []string

	// Check for missing files - but skip if this is a codebase-wide request
	// (repoRoot is already set as implicit context in that case)
	hasExplicitFiles := false
	for _, f := range conv.Context.FilesMentioned {
		if f != m.repoRoot { // repoRoot is implicit, not explicit
			hasExplicitFiles = true
			break
		}
	}
	if !hasExplicitFiles && !isCodebaseWideRequest(conv.Context.UserGoal) {
		questions = append(questions, "Which file(s) should I work on?")
	}

	// Check for unclear scope
	if conversation.IsComplexRequest(msg, conv.Context) {
		questions = append(questions, "What's the scope of this change? Just the specific issue or broader refactoring?")
	}

	// Check for constraints
	if _, hasConstraints := conv.Context.KnownConstraints["deadline"]; !hasConstraints {
		questions = append(questions, "Are there any constraints I should be aware of (deadlines, dependencies)?")
	}

	// If no specific questions, ask for confirmation
	if len(questions) == 0 {
		questions = append(questions, fmt.Sprintf("To confirm: you want me to %s?", conv.Context.UserGoal))
	}

	return questions
}

// planWork creates a task plan based on conversation context
func (m *Marshal) planWork(conv *conversation.Conversation) (*MarshalResponse, error) {
	// Create a task plan
	taskID := generateSessionID()
	task := conversation.TaskPlan{
		ID:                  taskID,
		Description:         conv.Context.UserGoal,
		FilesLikelyAffected: conv.Context.FilesMentioned,
		Complexity:          "medium",
		AutoExecute:         !conversation.IsComplexRequest(conv.Context.UserGoal, conv.Context) && conv.Context.Confidence >= 0.7,
	}

	// If only one file mentioned and simple request, mark as low complexity
	if len(conv.Context.FilesMentioned) == 1 && conv.Context.Confidence >= 0.7 {
		task.Complexity = "low"
		task.AutoExecute = true
	}

	conv.PendingTasks = []conversation.TaskPlan{task}

	// Build response based on auto-execute capability
	if task.AutoExecute {
		conv.State = conversation.StateExecuting
		return &MarshalResponse{
			Type:        conversation.ResponseTaskProgress,
			Content:     fmt.Sprintf("I'll work on: %s", task.Description),
			TaskPlans:   conv.PendingTasks,
			Intent:      conversation.IntentRequestWork,
			ExecuteTask: &task.Description,
		}, nil
	}

	// Need confirmation
	conv.State = conversation.StateChatting
	return &MarshalResponse{
		Type:      conversation.ResponseTaskPlan,
		Content:   fmt.Sprintf("I can help with that. Here's what I plan to do:\n\n%s\n\nShould I proceed?", task.Description),
		TaskPlans: conv.PendingTasks,
		Intent:    conversation.IntentRequestWork,
	}, nil
}

// planWorkWithExploration creates a task plan enriched with exploration findings.
func (m *Marshal) planWorkWithExploration(conv *conversation.Conversation, exploration *ExplorationResult) (*MarshalResponse, error) {
	// Create a task plan using exploration findings
	taskID := generateSessionID()

	// Determine complexity based on exploration and request
	complexity := "medium"
	if exploration.StepsTaken >= 10 {
		complexity = "high"
	} else if len(exploration.FilesFound) <= 2 {
		complexity = "low"
	}

	// In autonomous mode, auto-execute more aggressively
	orchestratorCfg := m.cfg.Orchestrator
	autoExecute := exploration.Confidence >= 0.5
	if !orchestratorCfg.AutoConfirmComplex && complexity == "high" {
		autoExecute = false
	}

	// Build enriched description
	description := conv.Context.UserGoal
	if len(exploration.FilesFound) > 0 {
		description += fmt.Sprintf("\n\nBased on my exploration, I'll work with these files: %s", strings.Join(exploration.FilesFound, ", "))
	}

	task := conversation.TaskPlan{
		ID:                  taskID,
		Description:         description,
		FilesLikelyAffected: conv.Context.FilesMentioned,
		Complexity:          complexity,
		AutoExecute:         autoExecute,
	}

	conv.PendingTasks = []conversation.TaskPlan{task}

	// Build response - autonomous mode shows findings and proceeds
	if autoExecute {
		conv.State = conversation.StateExecuting
		content := fmt.Sprintf("I'll work on: %s\n\nBased on my exploration:\n%s", conv.Context.UserGoal, exploration.Summary)
		return &MarshalResponse{
			Type:        conversation.ResponseTaskProgress,
			Content:     content,
			TaskPlans:   conv.PendingTasks,
			Intent:      conversation.IntentRequestWork,
			ExecuteTask: &task.Description,
		}, nil
	}

	// High complexity or low confidence - ask for confirmation
	conv.State = conversation.StateChatting
	content := fmt.Sprintf("I explored the codebase and found:\n\n%s\n\nI can help with: %s\n\nShould I proceed?",
		exploration.Summary, conv.Context.UserGoal)
	return &MarshalResponse{
		Type:      conversation.ResponseTaskPlan,
		Content:   content,
		TaskPlans: conv.PendingTasks,
		Intent:    conversation.IntentRequestWork,
	}, nil
}

// handleChat handles casual conversation using the LLM backend.
func (m *Marshal) handleChat(ctx context.Context, conv *conversation.Conversation, msg string) (*MarshalResponse, error) {
	// Build messages from conversation history for context
	messages := m.buildChatMessages(conv, msg)

	resp, err := m.backend.Complete(ctx, m.cfg.GetMarshalConfig().Model, messages)
	if err != nil {
		// Fallback to simple response on backend error
		return m.fallbackChat(msg), nil
	}

	think, cleaned := ExtractThinkBlock(resp.Content)
	content := cleaned
	if content == "" {
		content = resp.Content // no think tags — use raw content as-is
	}

	return &MarshalResponse{
		Type:         conversation.ResponseChat,
		Content:      content,
		ThinkContent: think,
		Intent:       conversation.IntentChat,
	}, nil
}

// buildChatMessages constructs the message list for a chat completion.
func (m *Marshal) buildChatMessages(conv *conversation.Conversation, msg string) []backend.Message {
	var messages []backend.Message

	// System prompt for conversational mode
	systemPrompt := `You are Marshal, an AI coding assistant orchestrator. The user is having a casual conversation with you. Keep responses helpful, concise, and conversational. If the user wants to code something, guide them toward using the work request flow.`

	messages = append(messages, backend.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	// Add conversation history (last 10 messages for context).
	// The current user message is already in conv.Messages — do not append it again.
	start := 0
	if len(conv.Messages) > 10 {
		start = len(conv.Messages) - 10
	}
	for _, m := range conv.Messages[start:] {
		role := m.Role
		if role == "marshal" {
			role = "assistant"
		}
		messages = append(messages, backend.Message{
			Role:    role,
			Content: m.Content,
		})
	}

	return messages
}

// fallbackChat returns a canned response when the backend is unavailable.
func (m *Marshal) fallbackChat(msg string) *MarshalResponse {
	responses := []string{
		"I'm here to help. What would you like me to work on?",
		"Got it. Let me know if you need anything implemented or fixed.",
		"Understood. I'm ready when you have a task for me.",
		"Let me know how I can help with this project.",
	}

	idx := 0
	if len(msg) > 0 {
		idx = int(msg[0]) % len(responses)
	}

	return &MarshalResponse{
		Type:    conversation.ResponseChat,
		Content: responses[idx],
		Intent:  conversation.IntentChat,
	}
}

// ExecuteTask runs a single task from planning through completion.
// This is the primary entry point for task execution.
func (m *Marshal) ExecuteTask(ctx context.Context, task string) (*Result, error) {
	session := &Session{
		ID:     generateSessionID(),
		Task:   task,
		Status: "RUNNING",
		Branch: fmt.Sprintf("marshal-session-%s", generateSessionID()),
	}

	// Store session if we have a store
	if m.store != nil {
		s := &store.Session{
			ID:              session.ID,
			RepoRoot:        m.cfg.RepoRoot,
			Task:            task,
			Status:          "RUNNING",
			BaseBranch:      "main",
			IsolationBranch: session.Branch,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		}
		if err := m.store.CreateSession(s); err != nil {
			// Log but continue
			_ = err
		}
	}

	// Execute the task
	result, err := m.runSingleTask(ctx, session)

	// Update final status
	if err != nil {
		session.Status = "FAILED"
	} else if result.Status == "SUCCESS" {
		session.Status = "SUCCESS"
	} else {
		session.Status = "EXHAUSTED"
	}

	// Update store
	if m.store != nil {
		completedAt := time.Now()
		s := &store.Session{
			ID:               session.ID,
			RepoRoot:         m.cfg.RepoRoot,
			Task:             task,
			Status:           session.Status,
			BaseBranch:       "main",
			IsolationBranch:  session.Branch,
			UpdatedAt:        time.Now(),
			CompletedAt:      &completedAt,
			PromptTokens:     result.TotalTokens.PromptTokens,
			CompletionTokens: result.TotalTokens.CompletionTokens,
		}
		_ = m.store.UpdateSession(s)
	}

	return result, err
}

// ExecuteTaskStreaming runs a task with streaming callback support.
// Callbacks are invoked as content chunks arrive from the executor and critic.
// onExecutorChunk is called with each executor output chunk.
// onCriticChunk is called with each critic output chunk.
// onThinkBlock is called when a complete <thinking>...</thinking> block is extracted.
func (m *Marshal) ExecuteTaskStreaming(ctx context.Context, task string, onExecutorChunk func(string), onCriticChunk func(string), onThinkBlock func(string)) (*Result, error) {
	session := &Session{
		ID:     generateSessionID(),
		Task:   task,
		Status: "RUNNING",
		Branch: fmt.Sprintf("marshal-session-%s", generateSessionID()),
	}

	// Store session if we have a store
	if m.store != nil {
		s := &store.Session{
			ID:              session.ID,
			RepoRoot:        m.cfg.RepoRoot,
			Task:            task,
			Status:          "RUNNING",
			BaseBranch:      "main",
			IsolationBranch: session.Branch,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		}
		if err := m.store.CreateSession(s); err != nil {
			_ = err
		}
	}

	// Execute the task with streaming
	result, err := m.runSingleTaskStreaming(ctx, session, onExecutorChunk, onCriticChunk, onThinkBlock)

	// Update final status
	if err != nil {
		session.Status = "FAILED"
	} else if result.Status == "SUCCESS" {
		session.Status = "SUCCESS"
	} else {
		session.Status = "EXHAUSTED"
	}

	// Update store
	if m.store != nil {
		completedAt := time.Now()
		s := &store.Session{
			ID:               session.ID,
			RepoRoot:         m.cfg.RepoRoot,
			Task:             task,
			Status:           session.Status,
			BaseBranch:       "main",
			IsolationBranch:  session.Branch,
			UpdatedAt:        time.Now(),
			CompletedAt:      &completedAt,
			PromptTokens:     result.TotalTokens.PromptTokens,
			CompletionTokens: result.TotalTokens.CompletionTokens,
		}
		_ = m.store.UpdateSession(s)
	}

	return result, err
}

// runSingleTaskStreaming executes one task with streaming and potential retries.
func (m *Marshal) runSingleTaskStreaming(ctx context.Context, session *Session, onExecutorChunk func(string), onCriticChunk func(string), onThinkBlock func(string)) (*Result, error) {
	// Create isolation branch
	if err := m.git.CreateIsolationBranch(session.Branch); err != nil {
		return nil, fmt.Errorf("marshal: create isolation branch: %w", err)
	}

	result, err := m.runSingleTaskOnBranchStreaming(ctx, session, onExecutorChunk, onCriticChunk, onThinkBlock)
	if err != nil {
		return nil, err
	}

	// Merge on success
	if result.Status == "SUCCESS" {
		mergeMsg := fmt.Sprintf("Merge %s: %s", session.Branch, result.FinalVerdict.Summary)
		if err := m.git.MergeBranch(session.Branch, mergeMsg); err != nil {
			m.cleanup(session.Branch)
			return nil, fmt.Errorf("marshal: merge branch: %w", err)
		}
		m.git.DeleteBranch(session.Branch)

		sha := ""
		if g, ok := m.git.(*git.Git); ok {
			sha = g.HeadSHA()
		}
		result.SHA = sha
	}

	return result, nil
}

// runSingleTaskOnBranchStreaming executes one task on an existing branch with streaming.
// Does NOT merge the branch - caller is responsible for merging.
func (m *Marshal) runSingleTaskOnBranchStreaming(ctx context.Context, session *Session, onExecutorChunk func(string), onCriticChunk func(string), onThinkBlock func(string)) (*Result, error) {
	var feedback string
	var compacted bool
	var compactionSummary string

	for roundNum := 1; roundNum <= m.cfg.Loop.MaxRounds; roundNum++ {
		// Check compaction
		if !compacted && m.agents.Compactor.ShouldCompact(roundNum, m.cfg.Loop.CompactAfter) {
			result, err := m.CommandCompactor(ctx, session.Rounds, 2)
			if err == nil && len(result.DroppedRounds) > 0 {
				session.Rounds = session.Rounds[len(session.Rounds)-2:]
				compactionSummary = result.Summary
			}
			compacted = true
		}

		// Execute one round with streaming
		round, err := m.executeRoundStreaming(ctx, roundNum, session.Task, feedback, compactionSummary, onExecutorChunk, onCriticChunk, onThinkBlock)
		if err != nil {
			return nil, err
		}

		session.Rounds = append(session.Rounds, round)

		// Store round if we have a store
		if m.store != nil {
			r := &store.RoundRecord{
				SessionID:        session.ID,
				RoundNumber:      round.Number,
				ExecutorRequest:  round.ExecutorReq.Task,
				ExecutorResponse: round.ExecutorResp,
				Diff:             round.Diff,
				Verdict:          round.Verdict.Verdict,
				Summary:          round.Verdict.Summary,
				Issue:            round.Verdict.Issue,
				Fix:              round.Verdict.Fix,
				Concerns:         round.Verdict.Concerns,
				PromptTokens:     round.Tokens.PromptTokens,
				CompletionTokens: round.Tokens.CompletionTokens,
				ThinkBlock:       round.ThinkBlock,
				CreatedAt:        time.Now(),
			}
			_ = m.store.CreateRound(r)
		}

		// Check verdict
		if round.Verdict.IsPass() {
			return &Result{
				Status:       "SUCCESS",
				FinalVerdict: &round.Verdict,
				Rounds:       session.Rounds,
				TotalTokens:  m.calculateTotalTokens(session.Rounds),
			}, nil
		}

		// Prepare feedback for next round
		feedbackParts := []string{
			fmt.Sprintf("Previous attempt failed:\nIssue: %s\nFix needed: %s",
				round.Verdict.Issue, round.Verdict.Fix),
		}
		if compactionSummary != "" {
			feedbackParts = append(feedbackParts, "\nContext from earlier rounds:\n"+compactionSummary)
		}
		feedback = strings.Join(feedbackParts, "\n")
	}

	// Exhausted max rounds
	return &Result{
		Status:       "EXHAUSTED",
		FinalVerdict: &session.Rounds[len(session.Rounds)-1].Verdict,
		Rounds:       session.Rounds,
		TotalTokens:  m.calculateTotalTokens(session.Rounds),
	}, fmt.Errorf("marshal: exhausted max rounds (%d)", m.cfg.Loop.MaxRounds)
}

// executeRoundStreaming runs one executor-critic cycle with streaming callbacks.
func (m *Marshal) executeRoundStreaming(ctx context.Context, roundNum int, task string, feedback string, compactionSummary string, onExecutorChunk func(string), onCriticChunk func(string), onThinkBlock func(string)) (Round, error) {
	execReq := executor.Request{
		Task:          task,
		PriorFeedback: feedback,
	}

	// Create think block accumulators
	execThinkAcc := NewThinkBlockAccumulator()
	criticThinkAcc := NewThinkBlockAccumulator()

	// Command Executor: use tool loop when enabled (non-streaming); fall back to streaming otherwise.
	var (
		execResult *executor.Result
		err        error
	)
	if m.cfg.Executor.EnableTools {
		execResult, err = m.CommandExecutorWithTools(ctx, execReq)
		if err != nil {
			return Round{}, fmt.Errorf("marshal: executor: %w", err)
		}
		// Surface the full response as a single chunk for the TUI
		if onExecutorChunk != nil && execResult.Content != "" {
			think, cleaned := ExtractThinkBlock(execResult.Content)
			if think != "" && onThinkBlock != nil {
				onThinkBlock(think)
			}
			if cleaned != "" {
				onExecutorChunk(cleaned)
			} else if think == "" {
				onExecutorChunk(execResult.Content)
			}
		}
	} else {
		execResult, err = m.CommandExecutorStreaming(ctx, execReq, func(chunk string) error {
			think, cleaned, _ := execThinkAcc.AddChunk(chunk)
			if think != "" && onThinkBlock != nil {
				onThinkBlock(think)
			}
			// Only forward non-think-block content as executor output.
			if cleaned != "" && onExecutorChunk != nil {
				onExecutorChunk(cleaned)
			}
			return nil
		})
		if err != nil {
			return Round{}, fmt.Errorf("marshal: executor: %w", err)
		}
	}

	// Flush any remaining executor content (e.g. content after last think block, or no-think responses).
	execFlushThink, execFlushCleaned := execThinkAcc.Flush()
	if execFlushThink != "" && onThinkBlock != nil {
		onThinkBlock(execFlushThink)
	}
	if execFlushCleaned != "" && onExecutorChunk != nil {
		onExecutorChunk(execFlushCleaned)
	}

	// Get diff from git
	diff, err := m.git.GetDiff()
	if err != nil {
		return Round{}, fmt.Errorf("marshal: get diff: %w", err)
	}

	// Command Critic with streaming
	criticResult, err := m.CommandCriticStreaming(ctx, diff, task, func(chunk string) error {
		think, cleaned, _ := criticThinkAcc.AddChunk(chunk)
		if think != "" && onThinkBlock != nil {
			onThinkBlock(think)
		}
		if cleaned != "" && onCriticChunk != nil {
			onCriticChunk(cleaned)
		}
		return nil
	})
	if err != nil {
		return Round{}, fmt.Errorf("marshal: critic: %w", err)
	}

	// Flush any remaining critic content.
	criticFlushThink, criticFlushCleaned := criticThinkAcc.Flush()
	if criticFlushThink != "" && onThinkBlock != nil {
		onThinkBlock(criticFlushThink)
	}
	if criticFlushCleaned != "" && onCriticChunk != nil {
		onCriticChunk(criticFlushCleaned)
	}

	// Extract final think blocks from complete responses (for storage)
	execThink, execClean := ExtractThinkBlock(execResult.Content)
	criticThink, criticClean := ExtractThinkBlock(criticResult.RawResponse)

	combinedThink := ""
	if execThink != "" {
		combinedThink += "**Executor reasoning:**\n" + execThink + "\n\n"
	}
	if criticThink != "" {
		combinedThink += "**Critic reasoning:**\n" + criticThink
	}

	return Round{
		Number:       roundNum,
		ExecutorReq:  execReq,
		ExecutorResp: execClean,
		Diff:         diff,
		CriticResp:   criticClean,
		Verdict:      *criticResult.Verdict,
		Tokens: TokenUsage{
			PromptTokens:     execResult.PromptTokens + criticResult.PromptTokens,
			CompletionTokens: execResult.CompletionTokens + criticResult.CompletionTokens,
		},
		ThinkBlock: combinedThink,
	}, nil
}

// ExecutePipelineTask executes a single task on a pre-created branch.
// The branch is NOT merged - the caller is responsible for merging on success.
// This is used by the pipeline runner for multi-task execution.
func (m *Marshal) ExecutePipelineTask(ctx context.Context, task string, branch string) (*Result, error) {
	session := &Session{
		ID:     generateSessionID(),
		Task:   task,
		Status: "RUNNING",
		Branch: branch,
	}

	// Execute the task (branch already exists)
	result, err := m.runSingleTaskOnBranch(ctx, session)

	return result, err
}

// runSingleTask executes one task with potential retries.
func (m *Marshal) runSingleTask(ctx context.Context, session *Session) (*Result, error) {
	// Create isolation branch
	if err := m.git.CreateIsolationBranch(session.Branch); err != nil {
		return nil, fmt.Errorf("marshal: create isolation branch: %w", err)
	}

	result, err := m.runSingleTaskOnBranch(ctx, session)
	if err != nil {
		return nil, err
	}

	// Merge on success
	if result.Status == "SUCCESS" {
		mergeMsg := fmt.Sprintf("Merge %s: %s", session.Branch, result.FinalVerdict.Summary)
		if err := m.git.MergeBranch(session.Branch, mergeMsg); err != nil {
			m.cleanup(session.Branch)
			return nil, fmt.Errorf("marshal: merge branch: %w", err)
		}
		m.git.DeleteBranch(session.Branch)

		sha := ""
		if g, ok := m.git.(*git.Git); ok {
			sha = g.HeadSHA()
		}
		result.SHA = sha
	}

	return result, nil
}

// runSingleTaskOnBranch executes one task on an existing branch.
// Does NOT merge the branch - caller is responsible for merging.
func (m *Marshal) runSingleTaskOnBranch(ctx context.Context, session *Session) (*Result, error) {
	var feedback string
	var compacted bool
	var compactionSummary string

	for roundNum := 1; roundNum <= m.cfg.Loop.MaxRounds; roundNum++ {
		// Check compaction
		if !compacted && m.agents.Compactor.ShouldCompact(roundNum, m.cfg.Loop.CompactAfter) {
			result, err := m.CommandCompactor(ctx, session.Rounds, 2)
			if err == nil && len(result.DroppedRounds) > 0 {
				// Actually drop rounds from history
				session.Rounds = session.Rounds[len(session.Rounds)-2:]
				compactionSummary = result.Summary
			}
			compacted = true
		}

		// Execute one round
		round, err := m.executeRound(ctx, roundNum, session.Task, feedback, compactionSummary)
		if err != nil {
			return nil, err
		}

		session.Rounds = append(session.Rounds, round)

		// Store round if we have a store
		if m.store != nil {
			r := &store.RoundRecord{
				SessionID:        session.ID,
				RoundNumber:      round.Number,
				ExecutorRequest:  round.ExecutorReq.Task,
				ExecutorResponse: round.ExecutorResp,
				Diff:             round.Diff,
				Verdict:          round.Verdict.Verdict,
				Summary:          round.Verdict.Summary,
				Issue:            round.Verdict.Issue,
				Fix:              round.Verdict.Fix,
				Concerns:         round.Verdict.Concerns,
				PromptTokens:     round.Tokens.PromptTokens,
				CompletionTokens: round.Tokens.CompletionTokens,
				ThinkBlock:       round.ThinkBlock,
				CreatedAt:        time.Now(),
			}
			_ = m.store.CreateRound(r)
		}

		// Check verdict
		if round.Verdict.IsPass() {
			return &Result{
				Status:       "SUCCESS",
				FinalVerdict: &round.Verdict,
				Rounds:       session.Rounds,
				TotalTokens:  m.calculateTotalTokens(session.Rounds),
			}, nil
		}

		// Prepare feedback for next round (include compaction summary if available)
		feedbackParts := []string{
			fmt.Sprintf("Previous attempt failed:\nIssue: %s\nFix needed: %s",
				round.Verdict.Issue, round.Verdict.Fix),
		}
		if compactionSummary != "" {
			feedbackParts = append(feedbackParts, "\nContext from earlier rounds:\n"+compactionSummary)
		}
		feedback = strings.Join(feedbackParts, "\n")
	}

	// Exhausted max rounds
	return &Result{
		Status:       "EXHAUSTED",
		FinalVerdict: &session.Rounds[len(session.Rounds)-1].Verdict,
		Rounds:       session.Rounds,
		TotalTokens:  m.calculateTotalTokens(session.Rounds),
	}, fmt.Errorf("marshal: exhausted max rounds (%d)", m.cfg.Loop.MaxRounds)
}

// executeRound runs one executor-critic cycle.
func (m *Marshal) executeRound(ctx context.Context, roundNum int, task string, feedback string, compactionSummary string) (Round, error) {
	// Build executor request
	execReq := executor.Request{
		Task:          task,
		PriorFeedback: feedback,
	}

	// Command Executor (with tool loop when enabled, falls back automatically)
	execResult, err := m.CommandExecutorWithTools(ctx, execReq)
	if err != nil {
		return Round{}, fmt.Errorf("marshal: executor: %w", err)
	}

	// Get diff from git
	diff, err := m.git.GetDiff()
	if err != nil {
		return Round{}, fmt.Errorf("marshal: get diff: %w", err)
	}

	// Command Critic
	reviewResult, err := m.CommandCritic(ctx, diff, task)
	if err != nil {
		return Round{}, fmt.Errorf("marshal: critic: %w", err)
	}

	// Extract think blocks
	execThink, execClean := ExtractThinkBlock(execResult.Content)
	criticThink, criticClean := ExtractThinkBlock(reviewResult.RawResponse)

	// Combine think blocks
	combinedThink := ""
	if execThink != "" {
		combinedThink += "**Executor reasoning:**\n" + execThink + "\n\n"
	}
	if criticThink != "" {
		combinedThink += "**Critic reasoning:**\n" + criticThink
	}

	return Round{
		Number:       roundNum,
		ExecutorReq:  execReq,
		ExecutorResp: execClean,
		Diff:         diff,
		CriticResp:   criticClean,
		Verdict:      *reviewResult.Verdict,
		Tokens: TokenUsage{
			PromptTokens:     execResult.PromptTokens + reviewResult.PromptTokens,
			CompletionTokens: execResult.CompletionTokens + reviewResult.CompletionTokens,
		},
		ThinkBlock: combinedThink,
	}, nil
}

// IntegrationCriticResult is the outcome of a cross-task coherence review.
type IntegrationCriticResult struct {
	Verdict         string // "PASS" or "FAIL"
	Summary         string
	CrossTaskIssues []string
}

// IntegrationCritic reviews the combined diff from all completed pipeline tasks for
// cross-task coherence issues (naming conflicts, API contract violations, etc.).
func (m *Marshal) IntegrationCritic(ctx context.Context, feature string, taskDescriptions []string, combinedDiff string) (*IntegrationCriticResult, error) {
	result, err := m.agents.Critic.ReviewIntegration(ctx, feature, taskDescriptions, combinedDiff)
	if err != nil {
		return nil, err
	}
	return &IntegrationCriticResult{
		Verdict:         result.Verdict,
		Summary:         result.Summary,
		CrossTaskIssues: result.CrossTaskIssues,
	}, nil
}

// CommandExecutor dispatches an order to the Executor agent.
func (m *Marshal) CommandExecutor(ctx context.Context, req executor.Request) (*executor.Result, error) {
	return m.agents.Executor.Execute(ctx, req)
}

// CommandExecutorWithTools dispatches to the Executor with the agentic tool loop.
// Falls back to Execute when tools are disabled in config or the backend lacks tool support.
func (m *Marshal) CommandExecutorWithTools(ctx context.Context, req executor.Request) (*executor.Result, error) {
	return m.agents.Executor.ExecuteWithTools(ctx, req)
}

// CommandCritic dispatches an order to the Critic agent.
func (m *Marshal) CommandCritic(ctx context.Context, diff string, task string) (*critic.Result, error) {
	return m.agents.Critic.Review(ctx, diff, task)
}

// CommandExecutorStreaming dispatches to Executor with streaming callback.
func (m *Marshal) CommandExecutorStreaming(ctx context.Context, req executor.Request, onChunk func(string) error) (*executor.Result, error) {
	return m.agents.Executor.ExecuteStreaming(ctx, req, onChunk)
}

// CommandCriticStreaming dispatches to Critic with streaming callback.
func (m *Marshal) CommandCriticStreaming(ctx context.Context, diff string, task string, onChunk func(string) error) (*critic.Result, error) {
	return m.agents.Critic.ReviewStreaming(ctx, diff, task, onChunk)
}

// CommandCompactor dispatches an order to the Compactor agent.
func (m *Marshal) CommandCompactor(ctx context.Context, history []Round, keepRecent int) (*compactor.Result, error) {
	// Convert []Round to []compactor.Round interface
	// This is a bit awkward due to the interface definition
	// For now, we'll implement a simple version

	if len(history) <= keepRecent {
		return &compactor.Result{
			Summary:       "",
			KeptRounds:    []int{},
			DroppedRounds: []int{},
			TokensSaved:   0,
		}, nil
	}

	// Simple implementation: just drop the rounds
	dropCount := len(history) - keepRecent
	toDrop := history[:dropCount]

	dropped := make([]int, len(toDrop))
	for i, round := range toDrop {
		dropped[i] = round.Number
	}

	kept := make([]int, keepRecent)
	for i := 0; i < keepRecent; i++ {
		kept[i] = history[dropCount+i].Number
	}

	tokensSaved := 0
	for _, round := range toDrop {
		tokensSaved += round.Tokens.PromptTokens + round.Tokens.CompletionTokens
	}

	return &compactor.Result{
		Summary:       "Context summarized from previous rounds",
		KeptRounds:    kept,
		DroppedRounds: dropped,
		TokensSaved:   tokensSaved,
	}, nil
}

// cleanup switches to base branch and deletes the isolation branch.
func (m *Marshal) cleanup(branchName string) {
	_ = m.git.CheckoutBranch("main")
	_ = m.git.DeleteBranch(branchName)
}

// calculateTotalTokens sums token usage across all rounds.
func (m *Marshal) calculateTotalTokens(rounds []Round) TokenUsage {
	var total TokenUsage
	for _, round := range rounds {
		total.PromptTokens += round.Tokens.PromptTokens
		total.CompletionTokens += round.Tokens.CompletionTokens
	}
	return total
}

// generateSessionID creates a unique session identifier.
func generateSessionID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("%d-%s", time.Now().Unix(), hex.EncodeToString(b))
}

// ExtractThinkBlock extracts think-block content from a complete response string.
// Handles both <think>...</think> (DeepSeek/Qwen) and <thinking>...</thinking> (Claude).
// Returns the think content and the cleaned content without the think block.
func ExtractThinkBlock(content string) (think string, cleaned string) {
	matches := thinkBlockRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return "", content
	}

	var thinkParts []string
	for _, match := range matches {
		if len(match) >= 2 {
			thinkParts = append(thinkParts, strings.TrimSpace(match[1]))
		}
	}

	return strings.Join(thinkParts, "\n\n"), strings.TrimSpace(thinkBlockRe.ReplaceAllString(content, ""))
}

// thinkBlockRe matches both <think>...</think> (DeepSeek/Qwen) and <thinking>...</thinking> (Claude).
var thinkBlockRe = regexp.MustCompile(`(?s)<think(?:ing)?>(.*?)</think(?:ing)?>`)

// ThinkBlockAccumulator tracks partial think block content as chunks arrive.
// It supports both <think> and <thinking> tags used by different model families.
type ThinkBlockAccumulator struct {
	pending string
}

// NewThinkBlockAccumulator creates an accumulator for progressive think block extraction.
func NewThinkBlockAccumulator() *ThinkBlockAccumulator {
	return &ThinkBlockAccumulator{}
}

// AddChunk processes a new content chunk.
// Returns completedThink (content of a just-closed block) and cleaned (non-think content ready to display).
// For non-streaming backends the full response arrives as one chunk and is processed completely.
// For content with no think tags, cleaned is returned immediately so output isn't silently buffered.
func (a *ThinkBlockAccumulator) AddChunk(chunk string) (completedThink string, cleaned string, err error) {
	a.pending += chunk

	// Fast path: no think tag anywhere — return everything as clean output immediately.
	if !strings.Contains(a.pending, "<think") {
		cleaned = a.pending
		a.pending = ""
		return
	}

	// Have a potential think tag. Check for a complete block.
	if !strings.Contains(a.pending, "</think") {
		// Opening tag present but no closing tag yet — keep buffering.
		return
	}

	// Complete think block found: extract all blocks and return cleaned remainder.
	var thinkParts []string
	for _, m := range thinkBlockRe.FindAllStringSubmatch(a.pending, -1) {
		if len(m) >= 2 {
			thinkParts = append(thinkParts, strings.TrimSpace(m[1]))
		}
	}
	if len(thinkParts) > 0 {
		completedThink = strings.Join(thinkParts, "\n\n")
	}
	cleaned = strings.TrimSpace(thinkBlockRe.ReplaceAllString(a.pending, ""))
	a.pending = ""
	return
}

// Flush drains any remaining buffered content after the stream ends.
func (a *ThinkBlockAccumulator) Flush() (think string, cleaned string) {
	if a.pending == "" {
		return
	}
	s := a.pending
	a.pending = ""

	// Try to extract any complete blocks (handles edge case of closing tag arriving late).
	matches := thinkBlockRe.FindAllStringSubmatch(s, -1)
	if len(matches) > 0 {
		var parts []string
		for _, m := range matches {
			if len(m) >= 2 {
				parts = append(parts, strings.TrimSpace(m[1]))
			}
		}
		think = strings.Join(parts, "\n\n")
		cleaned = strings.TrimSpace(thinkBlockRe.ReplaceAllString(s, ""))
		return
	}

	// No complete block — surface everything as cleaned content (e.g. incomplete think block).
	cleaned = s
	return
}

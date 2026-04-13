// Package voice implements voice recording and transcription for the /voice command.
// This is a stub implementation for systems without portaudio.
//
//go:build !portaudio
// +build !portaudio

package voice

import "fmt"

// Recorder is a stub type.
type Recorder struct{}

// NewRecorder creates a stub recorder that always returns an error.
func NewRecorder() (*Recorder, error) {
	return nil, fmt.Errorf("voice recording requires portaudio library (build with -tags portaudio)")
}

// Transcriber handles transcription of audio using Whisper API.
type Transcriber struct {
	apiKey  string
	baseURL string
}

// NewTranscriber creates a new transcriber.
func NewTranscriber(apiKey, baseURL string) *Transcriber {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &Transcriber{
		apiKey:  apiKey,
		baseURL: baseURL,
	}
}

// Transcribe sends audio data to the Whisper API and returns the transcription.
func (t *Transcriber) Transcribe(audioData []byte) (string, error) {
	if len(audioData) == 0 {
		return "", fmt.Errorf("no audio data")
	}
	// In stub mode, we can still transcribe if audio is provided via file
	return "", fmt.Errorf("voice transcription requires portaudio library (build with -tags portaudio)")
}

// Manager manages voice recording and transcription.
type Manager struct {
	transcriber *Transcriber
	available   bool
}

// NewManager creates a new voice manager (stub always returns unavailable).
func NewManager(apiKey, baseURL string) (*Manager, error) {
	return &Manager{
		transcriber: NewTranscriber(apiKey, baseURL),
		available:   false,
	}, nil
}

// IsAvailable returns false in stub mode.
func (m *Manager) IsAvailable() bool {
	return false
}

// StartRecording returns an error in stub mode.
func (m *Manager) StartRecording() error {
	return fmt.Errorf("voice recording requires portaudio library (build with -tags portaudio)")
}

// StopRecording returns an error in stub mode.
func (m *Manager) StopRecording() (string, error) {
	return "", fmt.Errorf("voice recording requires portaudio library (build with -tags portaudio)")
}

// Cleanup does nothing in stub mode.
func (m *Manager) Cleanup() error {
	return nil
}

// SaveRecording returns an error in stub mode.
func (m *Manager) SaveRecording(path string) error {
	return fmt.Errorf("voice recording requires portaudio library (build with -tags portaudio)")
}

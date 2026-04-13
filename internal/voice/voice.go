// Package voice implements voice recording and transcription for the /voice command.
// It uses gordonklaus/portaudio for recording and OpenAI-compatible Whisper API for transcription.
package voice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gordonklaus/portaudio"
)

// Recorder handles audio recording from the microphone.
type Recorder struct {
	stream     *portaudio.Stream
	buffer     []int16
	sampleRate float64
	channels   int
	isRunning  bool
}

// NewRecorder creates a new voice recorder.
func NewRecorder() (*Recorder, error) {
	if err := portaudio.Initialize(); err != nil {
		return nil, fmt.Errorf("initialize portaudio: %w", err)
	}

	return &Recorder{
		sampleRate: 16000, // 16kHz is good for speech recognition
		channels:   1,      // Mono
		buffer:     make([]int16, 0),
	}, nil
}

// Start begins recording audio.
func (r *Recorder) Start() error {
	if r.isRunning {
		return nil
	}

	// Open default input stream
	stream, err := portaudio.OpenDefaultStream(
		r.channels, 0, r.sampleRate, 1024,
		func(in []int16) {
			r.buffer = append(r.buffer, in...)
		},
	)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}

	if err := stream.Start(); err != nil {
		return fmt.Errorf("start stream: %w", err)
	}

	r.stream = stream
	r.isRunning = true
	return nil
}

// Stop stops recording and returns the recorded audio as WAV bytes.
func (r *Recorder) Stop() ([]byte, error) {
	if !r.isRunning {
		return nil, nil
	}

	if err := r.stream.Stop(); err != nil {
		return nil, fmt.Errorf("stop stream: %w", err)
	}

	if err := r.stream.Close(); err != nil {
		return nil, fmt.Errorf("close stream: %w", err)
	}

	r.isRunning = false

	// Convert buffer to WAV format
	wavData := r.encodeWAV()
	return wavData, nil
}

// encodeWAV converts the raw audio buffer to WAV format.
func (r *Recorder) encodeWAV() []byte {
	// WAV header constants
	const bitsPerSample = 16
	const byteRate = int(r.sampleRate) * r.channels * bitsPerSample / 8
	const blockAlign = r.channels * bitsPerSample / 8

	dataSize := len(r.buffer) * 2 // int16 = 2 bytes
	headerSize := 44
	totalSize := headerSize + dataSize

	wav := make([]byte, totalSize)

	// RIFF chunk descriptor
	copy(wav[0:4], "RIFF")
	wav[4] = byte(totalSize & 0xFF)
	wav[5] = byte((totalSize >> 8) & 0xFF)
	wav[6] = byte((totalSize >> 16) & 0xFF)
	wav[7] = byte((totalSize >> 24) & 0xFF)
	copy(wav[8:12], "WAVE")

	// fmt sub-chunk
	copy(wav[12:16], "fmt ")
	wav[16] = 16 // Subchunk1Size (16 for PCM)
	wav[17] = 0
	wav[18] = 0
	wav[19] = 0
	wav[20] = 1 // AudioFormat (1 = PCM)
	wav[21] = 0
	wav[22] = byte(r.channels)
	wav[23] = 0
	wav[24] = byte(int(r.sampleRate) & 0xFF)
	wav[25] = byte((int(r.sampleRate) >> 8) & 0xFF)
	wav[26] = byte((int(r.sampleRate) >> 16) & 0xFF)
	wav[27] = byte((int(r.sampleRate) >> 24) & 0xFF)
	wav[28] = byte(byteRate & 0xFF)
	wav[29] = byte((byteRate >> 8) & 0xFF)
	wav[30] = byte((byteRate >> 16) & 0xFF)
	wav[31] = byte((byteRate >> 24) & 0xFF)
	wav[32] = byte(blockAlign)
	wav[33] = 0
	wav[34] = bitsPerSample
	wav[35] = 0

	// data sub-chunk
	copy(wav[36:40], "data")
	wav[40] = byte(dataSize & 0xFF)
	wav[41] = byte((dataSize >> 8) & 0xFF)
	wav[42] = byte((dataSize >> 16) & 0xFF)
	wav[43] = byte((dataSize >> 24) & 0xFF)

	// Write audio data
	for i, sample := range r.buffer {
		offset := headerSize + i*2
		wav[offset] = byte(sample & 0xFF)
		wav[offset+1] = byte((sample >> 8) & 0xFF)
	}

	return wav
}

// Terminate cleans up portaudio resources.
func (r *Recorder) Terminate() error {
	return portaudio.Terminate()
}

// Transcriber handles transcription of audio using Whisper API.
type Transcriber struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewTranscriber creates a new transcriber.
func NewTranscriber(apiKey, baseURL string) *Transcriber {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &Transcriber{
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// Transcribe sends audio data to the Whisper API and returns the transcription.
func (t *Transcriber) Transcribe(audioData []byte) (string, error) {
	if len(audioData) == 0 {
		return "", fmt.Errorf("no audio data")
	}

	// Create multipart form
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add audio file
	part, err := writer.CreateFormFile("file", "recording.wav")
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(audioData); err != nil {
		return "", fmt.Errorf("write audio data: %w", err)
	}

	// Add model parameter
	if err := writer.WriteField("model", "whisper-1"); err != nil {
		return "", fmt.Errorf("write model field: %w", err)
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close writer: %w", err)
	}

	// Create request
	url := t.baseURL + "/audio/transcriptions"
	req, err := http.NewRequest("POST", url, &buf)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Send request
	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var result struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	return result.Text, nil
}

// Manager manages voice recording and transcription.
type Manager struct {
	recorder   *Recorder
	transcriber *Transcriber
	tempDir    string
}

// NewManager creates a new voice manager.
func NewManager(apiKey, baseURL string) (*Manager, error) {
	recorder, err := NewRecorder()
	if err != nil {
		return nil, err
	}

	tempDir, err := os.MkdirTemp("", "marshal-voice-*")
	if err != nil {
		recorder.Terminate()
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	return &Manager{
		recorder:    recorder,
		transcriber: NewTranscriber(apiKey, baseURL),
		tempDir:     tempDir,
	}, nil
}

// IsAvailable returns true if voice functionality is available.
func (m *Manager) IsAvailable() bool {
	return m.recorder != nil && m.transcriber != nil && m.transcriber.apiKey != ""
}

// StartRecording starts recording audio.
func (m *Manager) StartRecording() error {
	// Clear previous buffer
	m.recorder.buffer = m.recorder.buffer[:0]
	return m.recorder.Start()
}

// StopRecording stops recording and transcribes the audio.
func (m *Manager) StopRecording() (string, error) {
	audioData, err := m.recorder.Stop()
	if err != nil {
		return "", err
	}

	return m.transcriber.Transcribe(audioData)
}

// Cleanup cleans up resources.
func (m *Manager) Cleanup() error {
	os.RemoveAll(m.tempDir)
	return m.recorder.Terminate()
}

// SaveRecording saves the last recording to a file.
func (m *Manager) SaveRecording(path string) error {
	wavData := m.recorder.encodeWAV()
	return os.WriteFile(path, wavData, 0644)
}

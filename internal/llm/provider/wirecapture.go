package provider

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// wireCaptureEnvVar enables raw provider-response capture. Set it to a
// directory and every Chat response body is teed into a file there; unset
// (the default) disables capture entirely. Responses only — request bodies
// carry conversation content and API keys travel in headers, so neither is
// ever written.
const wireCaptureEnvVar = "MARSHAL_WIRE_CAPTURE"

// wireCapture tees one Chat response body into a file and accepts marker
// annotations from the decode loop. newWireCapture returns nil when capture
// is disabled or the capture file cannot be created: capture is a debugging
// aid and must never fail a request.
type wireCapture struct {
	mu sync.Mutex
	f  *os.File
}

func newWireCapture(providerName string) *wireCapture {
	dir := os.Getenv(wireCaptureEnvVar)
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	name := fmt.Sprintf("%s-%s.stream", sanitizeCaptureName(providerName), time.Now().UTC().Format("20060102T150405.000000000"))
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return nil
	}
	return &wireCapture{f: f}
}

// sanitizeCaptureName makes a provider name safe for use in a filename.
func sanitizeCaptureName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, name)
}

// Write implements io.Writer for the tee side of the capture.
func (w *wireCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Write(p)
}

// annotate writes a bracketed marker line into the capture file. Decode
// loops use it to flag chunks that carried nothing they recognize, so a
// later investigation can see what the endpoint actually sent. Safe on a
// nil receiver.
func (w *wireCapture) annotate(marker, raw string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	fmt.Fprintf(w.f, "\n%s %s\n", marker, raw)
}

// wrap tees body into the capture file; the file closes when body closes.
// A nil capture passes body through unchanged.
func (w *wireCapture) wrap(body io.ReadCloser) io.ReadCloser {
	if w == nil {
		return body
	}
	return &wireCaptureBody{ReadCloser: body, tee: io.TeeReader(body, w), capture: w}
}

type wireCaptureBody struct {
	io.ReadCloser
	tee     io.Reader
	capture *wireCapture
}

func (b *wireCaptureBody) Read(p []byte) (int, error) { return b.tee.Read(p) }

func (b *wireCaptureBody) Close() error {
	bodyErr := b.ReadCloser.Close()
	b.capture.mu.Lock()
	fileErr := b.capture.f.Close()
	b.capture.mu.Unlock()
	if bodyErr != nil {
		return bodyErr
	}
	return fileErr
}

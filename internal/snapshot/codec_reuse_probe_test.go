//go:build latencyprobe

package snapshot

// Component-level investigation only: no store, policy, publication or capture
// path is changed. Real source files are encoded in Git's loose-object format.
import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"crypto/sha1"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func probeEncode(raw []byte) ([]byte, error) {
	var out bytes.Buffer
	w, err := zlib.NewWriterLevel(&out, flate.NoCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(raw); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// A bounded reusable verifier for this experiment. It still consumes/hashes
// every decoded byte, checks EOF/checksum, rejects length/hash mismatches, and
// reinitializes after errors. It is not a production replacement for the store's
// header/type, filesystem identity, budget, Git-agreement and durability checks.
type probeCodecVerifier struct {
	reader io.ReadCloser
	buffer []byte
}

func (c *probeCodecVerifier) reset(encoded []byte) error {
	var err error
	if c.reader == nil {
		c.reader, err = zlib.NewReader(bytes.NewReader(encoded))
	} else {
		err = c.reader.(zlib.Resetter).Reset(bytes.NewReader(encoded), nil)
	}
	if err == nil && c.buffer == nil {
		c.buffer = make([]byte, 32*1024)
	}
	return err
}

func (c *probeCodecVerifier) verify(encoded, expected []byte) error {
	return c.verifyHash(encoded, len(expected), sha1.Sum(expected))
}

func (c *probeCodecVerifier) verifyHash(encoded []byte, expectedLen int, want [sha1.Size]byte) error {
	if err := c.reset(encoded); err != nil {
		return err
	}
	defer c.reader.Close()
	h := sha1.New()
	n, err := io.CopyBuffer(h, io.LimitReader(c.reader, int64(expectedLen)+1), c.buffer)
	if err != nil {
		return err
	}
	if n != int64(expectedLen) {
		return fmt.Errorf("decoded length %d, want %d", n, expectedLen)
	}
	var sum [sha1.Size]byte
	if !bytes.Equal(h.Sum(sum[:0]), want[:]) {
		return fmt.Errorf("decoded object hash mismatch")
	}
	return nil
}

// When the source bytes have already been hashed and retained by readBlob,
// direct comparison can prove stored byte equality without hashing them again.
// Large streaming sources would continue using the existing hash verifier.
func (c *probeCodecVerifier) compare(encoded, expected []byte) error {
	if err := c.reset(encoded); err != nil {
		return err
	}
	defer c.reader.Close()
	for offset := 0; offset < len(expected); {
		n := min(len(c.buffer), len(expected)-offset)
		if _, err := io.ReadFull(c.reader, c.buffer[:n]); err != nil {
			return err
		}
		if !bytes.Equal(c.buffer[:n], expected[offset:offset+n]) {
			return fmt.Errorf("stored bytes differ from captured source")
		}
		offset += n
	}
	var probe [1]byte
	n, err := c.reader.Read(probe[:])
	if n != 0 {
		return fmt.Errorf("extra decoded byte")
	}
	if err != io.EOF {
		return fmt.Errorf("missing verified EOF: %v", err)
	}
	return nil
}

func TestCodecReuseProbePreservesBytesAndRejectsCorruption(t *testing.T) {
	w, err := zlib.NewWriterLevel(io.Discard, flate.NoCompression)
	if err != nil {
		t.Fatal(err)
	}
	c := &probeCodecVerifier{}
	for _, body := range [][]byte{nil, []byte("source one\n"), bytes.Repeat([]byte("different source\n"), 4000), []byte("small again\n")} {
		raw := append(objectHeader("blob", int64(len(body))), body...)
		fresh, err := probeEncode(raw)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		w.Reset(&out)
		if _, err := w.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(fresh, out.Bytes()) {
			t.Fatal("reset writer changed encoded object bytes")
		}
		if err := c.verify(fresh, raw); err != nil {
			t.Fatal(err)
		}
		if err := c.verify(fresh[:len(fresh)-1], raw); err == nil {
			t.Fatal("truncated zlib stream accepted")
		}
		corrupt := append([]byte(nil), fresh...)
		corrupt[len(corrupt)-1] ^= 1
		if err := c.verify(corrupt, raw); err == nil {
			t.Fatal("checksum corruption accepted")
		}
		if err := c.verify(fresh, raw); err != nil {
			t.Fatalf("reader could not reset after checksum error: %v", err)
		}
		if err := c.verify([]byte("invalid header"), raw); err == nil {
			t.Fatal("invalid zlib header accepted")
		}
		if err := c.verify(fresh, raw); err != nil {
			t.Fatalf("reader could not reset after header error: %v", err)
		}
		changed := append([]byte(nil), raw...)
		changed[len(changed)-1] ^= 1
		wrong, err := probeEncode(changed)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.verify(wrong, raw); err == nil {
			t.Fatal("valid zlib stream with wrong object hash accepted")
		}
		extra, err := probeEncode(append(append([]byte(nil), raw...), byte('x')))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.verify(extra, raw); err == nil {
			t.Fatal("extra decoded byte accepted")
		}
		for _, invalid := range [][]byte{corrupt, fresh[:len(fresh)-1], []byte("invalid header"), wrong, extra} {
			if err := c.compare(invalid, raw); err == nil {
				t.Fatal("direct comparison accepted invalid object")
			}
			if err := c.compare(fresh, raw); err != nil {
				t.Fatalf("direct comparison failed after reset: %v", err)
			}
		}
	}
}

var codecBytesSink int

func BenchmarkSnapshotCodecReuseProbe(b *testing.B) {
	root := os.Getenv("MARSHAL_LATENCY_SOURCE_DIR")
	if root == "" {
		b.Skip("set MARSHAL_LATENCY_SOURCE_DIR to the actual Marshal source checkout")
	}
	for _, path := range []string{"go.mod", "internal/agent/metrics.go", "internal/app/runtime.go"} {
		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			b.Fatal(err)
		}
		raw := append(objectHeader("blob", int64(len(body))), body...)
		encoded, err := probeEncode(raw)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(path, func(b *testing.B) {
			b.Run("new_writer_per_object", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(raw)))
				for i := 0; i < b.N; i++ {
					w, err := zlib.NewWriterLevel(io.Discard, flate.NoCompression)
					if err != nil {
						b.Fatal(err)
					}
					n, err := w.Write(raw)
					if err != nil {
						b.Fatal(err)
					}
					if err := w.Close(); err != nil {
						b.Fatal(err)
					}
					codecBytesSink = n
				}
			})
			b.Run("reset_writer_per_capture", func(b *testing.B) {
				w, err := zlib.NewWriterLevel(io.Discard, flate.NoCompression)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := w.Write(raw); err != nil {
					b.Fatal(err)
				}
				if err := w.Close(); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.SetBytes(int64(len(raw)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					w.Reset(io.Discard)
					n, err := w.Write(raw)
					if err != nil {
						b.Fatal(err)
					}
					if err := w.Close(); err != nil {
						b.Fatal(err)
					}
					codecBytesSink = n
				}
			})
			b.Run("new_reader_and_copy_buffer", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(raw)))
				want := sha1.Sum(raw)
				for i := 0; i < b.N; i++ {
					r, err := zlib.NewReader(bytes.NewReader(encoded))
					if err != nil {
						b.Fatal(err)
					}
					h := sha1.New()
					n, err := io.Copy(h, io.LimitReader(r, int64(len(raw))+1))
					if err != nil {
						b.Fatal(err)
					}
					if err := r.Close(); err != nil {
						b.Fatal(err)
					}
					var sum [sha1.Size]byte
					if n != int64(len(raw)) || !bytes.Equal(h.Sum(sum[:0]), want[:]) {
						b.Fatal("decoded bytes changed")
					}
				}
			})
			b.Run("reset_reader_and_reuse_buffer", func(b *testing.B) {
				c := &probeCodecVerifier{}
				want := sha1.Sum(raw)
				if err := c.verifyHash(encoded, len(raw), want); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.SetBytes(int64(len(raw)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := c.verifyHash(encoded, len(raw), want); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("reset_reader_compare_buffered_source", func(b *testing.B) {
				c := &probeCodecVerifier{}
				if err := c.compare(encoded, raw); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.SetBytes(int64(len(raw)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := c.compare(encoded, raw); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

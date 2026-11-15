package snapshot

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"errors"
	"io"
	"os"
	"testing"
)

// Unchanged buffered blobs should not allocate their source and codec working
// sets again for each file. This measures the production read/verify path.
func TestUnchangedBlobReusesCaptureScratch(t *testing.T) {
	requireGit(t)
	_, _, _, w, _ := testObjectWriterEnv(t, 2<<20)
	body := bytes.Repeat([]byte("source bytes\n"), 10000)
	if _, err := w.writeBlob(&bytesBlobSource{desc: "source", data: body}); err != nil {
		t.Fatal(err)
	}
	result := testing.Benchmark(func(b *testing.B) {
		for range b.N {
			if _, err := w.writeBlob(&bytesBlobSource{desc: "source", data: body}); err != nil {
				b.Fatal(err)
			}
		}
	})
	if n := result.AllocedBytesPerOp(); n > 16<<10 {
		t.Fatalf("unchanged blob reallocates working set: %d bytes/op", n)
	}
}

func TestReusedObjectVerifierRejectsCorruptionAfterSuccess(t *testing.T) {
	requireGit(t)
	_, _, _, w, _ := testObjectWriterEnv(t, 2<<20)
	body := []byte("captured source\n")
	hash, err := w.writeBlob(&bytesBlobSource{desc: "source", data: body})
	if err != nil {
		t.Fatal(err)
	}
	path, err := w.objectPath(hash)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	header := objectHeader("blob", int64(len(body)))
	encode := func(raw []byte) []byte {
		t.Helper()
		var out bytes.Buffer
		z, err := zlib.NewWriterLevel(&out, flate.NoCompression)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := z.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	checksum := append([]byte(nil), valid...)
	checksum[len(checksum)-1] ^= 1
	wrong := append(append([]byte(nil), header...), []byte("different bytes\n")...)
	extra := append(append(append([]byte(nil), header...), body...), 'x')
	for _, data := range [][]byte{body, nil} { // buffered comparison and streamed hash
		for _, invalid := range [][]byte{
			checksum, valid[:len(valid)-1], []byte("bad zlib header"),
			encode(wrong), encode(extra), encode(append([]byte("tree 16\x00"), body...)),
		} {
			if err := os.WriteFile(path, valid, 0o644); err != nil {
				t.Fatal(err)
			}
			if ok, err := w.storedObjectMatches(path, hash, header, int64(len(body)), data); err != nil || !ok {
				t.Fatalf("valid before corruption: %v %v", ok, err)
			}
			if err := os.WriteFile(path, invalid, 0o644); err != nil {
				t.Fatal(err)
			}
			if ok, err := w.storedObjectMatches(path, hash, header, int64(len(body)), data); err == nil || ok {
				t.Fatalf("accepted corruption after valid verification: %v %v", ok, err)
			}
		}
	}
	if err := os.WriteFile(path, valid, 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, err := w.storedObjectMatches(path, hash, header, int64(len(body)), body); err != nil || !ok {
		t.Fatalf("failed to reset after errors: %v %v", ok, err)
	}
}

type failedObjectDestination struct{}

func (failedObjectDestination) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestObjectCodecWriterResetsAfterFailure(t *testing.T) {
	var codec objectCodec
	w, err := codec.writer(failedObjectDestination{})
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := w.Write([]byte("failed object"))
	closeErr := w.Close()
	if !errors.Is(writeErr, io.ErrClosedPipe) && !errors.Is(closeErr, io.ErrClosedPipe) {
		t.Fatalf("missing injected failure: %v %v", writeErr, closeErr)
	}
	codec.releaseWriter()
	for _, body := range [][]byte{nil, []byte("next object\n"), bytes.Repeat([]byte("b"), 200000), []byte("small again")} {
		var out, fresh bytes.Buffer
		w, err = codec.writer(&out)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		codec.releaseWriter()
		z, err := zlib.NewWriterLevel(&fresh, flate.NoCompression)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := z.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.Bytes(), fresh.Bytes()) {
			t.Fatal("reused writer changed encoded bytes")
		}
	}
}

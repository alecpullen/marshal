package snapshot

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/zlib"
	"fmt"
	"io"
)

// objectCodec is private to one sequential capture. No plaintext or codec
// state is pooled across workspaces/captures. Buffers are bounded by the
// buffered-blob limit and one streaming chunk.
type objectCodec struct {
	encoder *zlib.Writer
	decoder io.ReadCloser
	input   *bufio.Reader
	copyBuf []byte
	blobBuf []byte
}

func (c *objectCodec) writer(dst io.Writer) (*zlib.Writer, error) {
	if c.encoder == nil {
		var err error
		c.encoder, err = zlib.NewWriterLevel(dst, flate.NoCompression)
		return c.encoder, err
	}
	c.encoder.Reset(dst)
	return c.encoder, nil
}

func (c *objectCodec) releaseWriter() {
	// Reset after success and failure: do not retain an object file or its
	// capped writer, and never let one object's write error poison the next.
	c.encoder.Reset(io.Discard)
}

func (c *objectCodec) reader(src io.Reader) (io.ReadCloser, error) {
	if c.input == nil {
		c.input = bufio.NewReader(src)
	} else {
		c.input.Reset(src)
	}
	var err error
	if c.decoder == nil {
		c.decoder, err = zlib.NewReader(c.input)
	} else {
		err = c.decoder.(zlib.Resetter).Reset(c.input, nil)
	}
	if err != nil {
		c.input.Reset(nil)
		return nil, err
	}
	return c.decoder, nil
}

func (c *objectCodec) releaseReader() {
	_ = c.decoder.Close()
	// The decoder references this reusable reader, rather than an individual
	// file. It is reset to a real source before its next read.
	c.input.Reset(nil)
}

func (c *objectCodec) buffer() []byte {
	if c.copyBuf == nil {
		c.copyBuf = make([]byte, 64<<10)
	}
	return c.copyBuf
}

// compare consumes exactly the expected bytes; the caller separately proves
// EOF/checksum after all header/body parts have been consumed.
func (c *objectCodec) compare(r io.Reader, expected []byte) error {
	buf := c.buffer()
	for len(expected) > 0 {
		n := min(len(buf), len(expected))
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			return err
		}
		if !bytes.Equal(buf[:n], expected[:n]) {
			return fmt.Errorf("stored object holds different bytes")
		}
		expected = expected[n:]
	}
	return nil
}

func objectEOF(r io.Reader) error {
	var probe [1]byte
	n, err := r.Read(probe[:])
	if n != 0 {
		return fmt.Errorf("stored object has trailing decoded data")
	}
	if err != io.EOF {
		return fmt.Errorf("stored object has no verified EOF: %v", err)
	}
	return nil
}

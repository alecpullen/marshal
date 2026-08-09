package provider

import "strings"

// inlineThinkParser extracts  thinking... response blocks from a streaming
// content channel, emitting the segments as either normal content or
// reasoning text. Thinking text is buffered until the close tag completes so
// it is emitted as a single block. Tags that are split across SSE chunks are
// handled by keeping a small suffix of uncommitted bytes until a complete
// open or close tag can be resolved.
type inlineThinkParser struct {
	inThink  bool
	pending  strings.Builder // uncommitted bytes (may hold a partial tag)
	thinking strings.Builder // buffered thinking text awaiting its close tag
}

// feed appends s to the parser's buffer and returns (content, thinking)
// segments that can be emitted safely.
func (p *inlineThinkParser) feed(s string) (string, string) {
	if s == "" {
		return "", ""
	}
	p.pending.WriteString(s)
	data := p.pending.String()
	var content, thinking strings.Builder

	for data != "" {
		if p.inThink {
			idx := strings.Index(data, " response")
			if idx == -1 {
				// No close tag yet. Buffer everything except a possible
				// split close-tag prefix.
				keep := longestTagPrefix(data, " response")
				if len(data) > keep {
					p.thinking.WriteString(data[:len(data)-keep])
					data = data[len(data)-keep:]
				}
				break
			}
			p.thinking.WriteString(data[:idx])
			data = data[idx+len(" response"):]
			p.inThink = false
			// Emit the buffered thinking as one block.
			thinking.WriteString(p.thinking.String())
			p.thinking.Reset()
			continue
		}

		idx := strings.Index(data, " thinking")
		if idx == -1 {
			// No open tag yet. Emit content except a possible split
			// open-tag prefix.
			keep := longestTagPrefix(data, " thinking")
			if len(data) > keep {
				content.WriteString(data[:len(data)-keep])
				data = data[len(data)-keep:]
			}
			break
		}
		content.WriteString(data[:idx])
		data = data[idx+len(" thinking"):]
		p.inThink = true
	}

	p.pending.Reset()
	p.pending.WriteString(data)
	return content.String(), thinking.String()
}

// flush emits any remaining bytes. It must be called at end of stream.
func (p *inlineThinkParser) flush() (string, string) {
	data := p.pending.String()
	p.pending.Reset()
	if p.inThink {
		// Unclosed think block: emit the buffered thinking plus any
		// remaining bytes as reasoning.
		p.thinking.WriteString(data)
		th := p.thinking.String()
		p.thinking.Reset()
		return "", th
	}
	return data, ""
}

// longestTagPrefix returns the length of the longest suffix of s that is a
// prefix of tag. It is used to decide how many trailing bytes to hold back
// when a tag may be split across streaming chunks.
func longestTagPrefix(s, tag string) int {
	maxN := len(s)
	if l := len(tag) - 1; l < maxN {
		maxN = l
	}
	for n := maxN; n > 0; n-- {
		if strings.HasPrefix(tag, s[len(s)-n:]) {
			return n
		}
	}
	return 0
}

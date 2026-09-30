package tui

import (
	"marshal/internal/app/tui/conversation"
)

// defaultCopyTarget picks the target a copy action uses when the user has not
// chosen one: the block's dominant text.
//
// It is the FIRST target the adapter attached, and the adapter attaches the
// answer before any code block, so "the first one" is not an accident of
// ordering — it is the rule, and the adapter's ordering is what implements it.
// A block carrying only alternatives (a tool call with output and a path, but
// no dominant text) still resolves: output is the primary promise there.
func defaultCopyTarget(block conversation.Block) (conversation.CopyTarget, bool) {
	if target, ok := blockTarget(block, block.Source); ok {
		return target, true
	}
	if len(block.CopyTargets) == 0 {
		return conversation.CopyTarget{}, false
	}
	return block.CopyTargets[0], true
}

// blockTarget returns the block's target for one source.
func blockTarget(block conversation.Block, source conversation.CopySource) (conversation.CopyTarget, bool) {
	for _, t := range block.CopyTargets {
		if t.Source == source {
			return t, true
		}
	}
	return conversation.CopyTarget{}, false
}

// blockTargets returns every target of one source, in order.
func blockTargets(block conversation.Block, source conversation.CopySource) []conversation.CopyTarget {
	var out []conversation.CopyTarget
	for _, t := range block.CopyTargets {
		if t.Source == source {
			out = append(out, t)
		}
	}
	return out
}

// codeTargets returns a block's code targets. Several code blocks in one
// answer are several targets, which is why the code action may have to ask
// which one rather than copying the first: silently copying the first block
// is how a user pastes the wrong snippet without ever being told there was a
// choice.
func codeTargets(block conversation.Block) []conversation.CopyTarget {
	return blockTargets(block, conversation.SourceCode)
}

// hasCopyTarget reports whether a block offers a target of this source. It
// backs the availability rules, so an action is offered exactly when the block
// under consideration can actually satisfy it.
func hasCopyTarget(block conversation.Block, source conversation.CopySource) bool {
	_, ok := blockTarget(block, source)
	return ok
}

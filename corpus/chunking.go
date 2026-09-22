package corpus

import "strings"

// The store counts chunk size in words, and Chinese has almost none.
//
// Ingesting a 12KB Chinese proposal with ChunkSize 1200 produced two chunks.
// The reason is that the store splits on strings.Fields, so "words" means
// whitespace-separated runs — and a Chinese paragraph is one run. The whole
// document came in under the limit and was stored as a single passage, with a
// character ceiling of chunkSize×4 as the only thing that split it at all.
//
// The effect is not a tidiness problem. A citation is supposed to be the
// paragraph that answers the question; what came back was the first 4,800
// characters of the document, which for a proposal is the cover page. The
// retrieval was right and the passage was useless, which is the worst
// combination because nothing reports it.
//
// So the size is computed from the text rather than assumed. Measure how many
// characters a "word" actually is in this document and pick the word count that
// lands a chunk near the target. English comes out around 80 words; Chinese
// around 12 runs. Both are roughly targetChars on the page, which is the thing
// that was meant all along.

// targetChars is how long a citable passage should be: long enough to carry a
// claim and its reason, short enough that a reader can see why it was quoted.
const targetChars = 500

// chunkWordsFor returns the ChunkSize, in the store's units, that lands a chunk
// of roughly targetChars for this text.
func chunkWordsFor(text string) int {
	words := len(strings.Fields(text))
	if words == 0 {
		return 1
	}
	chars := len([]rune(text))
	perWord := chars / words
	if perWord < 1 {
		perWord = 1
	}
	n := targetChars / perWord
	if n < 1 {
		// A document whose every "word" is already longer than the target —
		// one unbroken Chinese paragraph. One run per chunk, and the store's
		// character ceiling does the rest.
		return 1
	}
	return n
}

// chunkOverlapFor is a tenth of the chunk, floored at one unit: enough that a
// sentence split across a boundary still appears whole somewhere, without
// storing every passage twice.
func chunkOverlapFor(words int) int {
	if n := words / 10; n >= 1 {
		return n
	}
	return 0
}

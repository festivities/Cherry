package lpn

import (
	"log"

	"cherry/internal/httpx"
	"cherry/internal/metrics"
)

// Every chat path (Square and its whispers, Room Party, Circle, Lounge Chat, the 1:1 chat) checks the typed text with
// unsafeChat before anyone else sees it, then relays and stores economy.MaskBannedWords of it. The chat log keeps the text
// as typed, for staff.

// unsafeChat reports whether text from aid breaks the text shape rule (httpx.SafeText: a tall stack of combining marks that
// crashes the client of everyone who draws it). A refusal is counted (metrics.TextRefused) and logged with the sender, so
// staff can find who sends it; the text itself is not logged.
func unsafeChat(kind string, aid uint64, text string) bool {
	if httpx.SafeText(text) {
		return false
	}
	metrics.TextRefused.Add(1)
	log.Printf("%s chat refused aid=%d: unsafe text (more than %d combining marks in a row)", kind, aid, httpx.MaxMarks)
	return true
}

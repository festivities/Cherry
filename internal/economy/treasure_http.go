package economy

import (
	"net/http"

	"cherry/internal/httpx"
)

// Treasure World money answers (transport-money.md section 3.4 and 7.3). Nothing is sold: the treasure pass is not
// offered, and the special pack list is empty. The spot routes (bill/prod/spot/gem, cash, heart) are shared with the
// generic lack-of-money popup and are answered by RegisterEconomyRoutes with an empty list; they are not repeated here.

// treasureIconBody: ResBlueMarblePakageAvailable reads result.available; false leaves the board where it is.
const treasureIconBody = `{"result":{"available":false}}`

// treasurePackageInfoBody: the special pack list is empty (ResBlueMarbleSpecialPackList is a no-op on success).
const treasurePackageInfoBody = `{"result":{}}`

// RegisterTreasureRoutes serves the two Treasure World billing answers above.
func RegisterTreasureRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v4/bill/prod/treasure/icon", httpx.HandleJSONBody(treasureIconBody))
	mux.HandleFunc("/v4/bill/package2/bluemarble/info", httpx.HandleJSONBody(treasurePackageInfoBody))
}

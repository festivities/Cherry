package store

// StyleSlot is one saved Closet styling slot (economy/styling.go): a named outfit, bookmarked or not. ID is per account, starts
// at 1 and is never reused (Account.StyleNextID). Replaced, never edited in place.
type StyleSlot struct {
	ID       int64       `json:"id"`
	Name     string      `json:"name"`
	Bookmark bool        `json:"bookmark,omitempty"`
	Items    []StyleItem `json:"items"`
}

// StyleItem is one garment of a styling slot: its item code and the closet serial the client saved it with. The replies
// number an owned garment from the closet order at reply time (the serial the closet shows now); InventorySeq answers
// for a garment the account no longer owns.
type StyleItem struct {
	ItemCode     string `json:"itemCode"`
	InventorySeq int64  `json:"inventorySeq,omitempty"`
}

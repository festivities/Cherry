package opdata

import (
	"errors"
	"fmt"
	"strings"
)

// Provenance is the marking every operator data file carries (AGENTS.md "Cherry-made marking pass"). Origin
// is "cherry" when the whole file is Cherry-made, and "mixed" when it combines original ids or art with
// Cherry-made values (the note then says per field which is which). Note is text for operators; it is never
// sent to the client. Embed it in a file's type, so the JSON keys "origin" and "note" are required by the
// strict decoder, and call CheckOrigin with the origin the file declares.
type Provenance struct {
	Origin string `json:"origin"`
	Note   string `json:"note"`
}

// Origins a data file may declare.
const (
	OriginCherry = "cherry"
	OriginMixed  = "mixed"
)

// CheckOrigin requires the origin the file declares (want) and a note that is not empty.
func (p Provenance) CheckOrigin(want string) error {
	if p.Origin != want {
		return fmt.Errorf("origin must be %q", want)
	}
	if strings.TrimSpace(p.Note) == "" {
		return errors.New("note must say what is original and what Cherry made")
	}
	return nil
}

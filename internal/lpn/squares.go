package lpn

import (
	"errors"

	"cherry/data"
	"cherry/internal/opdata"
)

// Squares is data/squares.json: the Square in the Square list (theme 6, map 2401039000). The name is
// Cherry's choice from the client's string table ("LINE City", string 9050); the client prints it as
// both ThemeName and ThemeListName.
//
//	name  the Square's name, not empty
type Squares struct {
	Name string `json:"name"`
}

var squaresFile = opdata.New("squares.json", data.SquaresJSON, checkSquares, nil)

// LoadOperatorData loads the game-connection operator data (squares.json) at startup.
func LoadOperatorData() error { return squaresFile.Load() }

func checkSquares(s *Squares) error {
	if s.Name == "" {
		return errors.New("name must not be empty")
	}
	return nil
}

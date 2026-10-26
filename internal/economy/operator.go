package economy

import (
	"fmt"
	"image/color"
	"strconv"
)

// LoadOperatorData loads every operator-editable data file (AGENTS "Operator-editable fabricated
// content"): the defaults embedded from data/, or the operator files in %LOCALAPPDATA%\Cherry\config
// that replace them. cmd/cherry calls it once at startup, before the listeners open; an error stops
// Cherry and names the file and the entry. The order matters: the Atelier names gathering drops, so
// gathering loads before it.
func LoadOperatorData() error {
	for _, load := range []func() error{
		pricesFile.Load, gachaFile.Load, gatheringFile.Load, fishingFile.Load, fishbossFile.Load,
		calendarsFile.Load, exchangeFile.Load, rewardsFile.Load, atelierFile.Load, racingFile.Load, rideShopFile.Load,
		gardensFile.Load,
	} {
		if err := load(); err != nil {
			return err
		}
	}
	return nil
}

// parseHexColor reads an opaque "#rrggbb" colour.
func parseHexColor(s string) (color.NRGBA, error) {
	if len(s) != 7 || s[0] != '#' {
		return color.NRGBA{}, fmt.Errorf("colour %q must be #rrggbb", s)
	}
	n, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return color.NRGBA{}, fmt.Errorf("colour %q must be #rrggbb", s)
	}
	return color.NRGBA{R: uint8(n >> 16), G: uint8(n >> 8), B: uint8(n), A: 255}, nil
}

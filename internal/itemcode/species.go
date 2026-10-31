package itemcode

// HumanOnly reports whether an ANIMAL avatar cannot wear code: a human hair (CU HA) or a human face (CU HE, EB, EY, NO,
// MO). Clothes are not human-only: they render on animals too. Only the shape is checked here; the catalog is the caller's.
func HumanOnly(code string) bool {
	if len(code) != 9 || code[:2] != "CU" {
		return false
	}
	switch code[2:4] {
	case "HA", "HE", "EB", "EY", "NO", "MO":
		return true
	}
	return false
}

// AnimalOnly reports whether a human cannot wear code: an animal face, ear or tail part (CA FA, EA, TL, EY, NO, MO).
// The Face Shop sells these to ANIMAL accounts only (economy/faceshop.go isAnimalPart).
func AnimalOnly(code string) bool {
	if len(code) != 9 || code[:2] != "CA" {
		return false
	}
	switch code[2:4] {
	case "FA", "EA", "TL", "EY", "NO", "MO":
		return true
	}
	return false
}

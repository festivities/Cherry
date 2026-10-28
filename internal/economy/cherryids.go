package economy

// inCherryBlock reports whether id is inside one of Cherry's own id blocks, [first, last]. Each block is declared
// next to the feature that uses it (CherryFishFirst, CherryQuestFirst, ...) and its startup check refuses an id
// outside it, so a Cherry-made id never reads as an original one.
func inCherryBlock(id, first, last int64) bool { return id >= first && id <= last }

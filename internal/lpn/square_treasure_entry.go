package lpn

// The Treasure World entry is a Square NPC, not a map object (entry spec, %TEMP%\opencode\cherry\treasure-island-20261117\
// entry-spec.md, sections 1 and 4): NpcIndex 0x3010033 creates NaGosomewhereLinkNpc, whose link text (field 73, KeyLink)
// goSomewhere(treasureWorld) is GetLinkType 93, which opens Treasure World's login (SceneChangeByLinkType case 0x5D).
// Only Treasure Island carries it (user, 2026-11-17: Treasure World is on Treasure Island only). The NPC needs its host
// object on the map to show its button (ShowAnimationButton returns early without one), so it stands on the stock
// root floor RUTD000R8 (item 121700980) at tile (73,105), seven tiles south of the spawn. Cherry-made: the serial (6),
// the host choice and the tile; the NpcIndex, the field numbers and the link text are the client's.
const (
	squareTreasureNpcIndex   = 0x3010033
	squareTreasureNpcSerial  = 6
	squareTreasureInteriorID = 121700980
	squareTreasureLink       = "goSomewhere(treasureWorld)"
)

// squareTreasureTile is the entry's tile on Treasure Island (host RUTD000R8).
var squareTreasureTile = Tile{73, 105}

// squareTreasureNpcEntry is one rc_room_enter_res NpcList entry: the Treasure World entry (field 75 AniButtonVisible
// is 1; whether the client needs it is [OPEN] in the spec: device test with and without).
func squareTreasureNpcEntry() []byte {
	px, py := tilePx(squareIslandSize, squareTreasureTile)
	npc := squareInteriorNpc(squareTreasureNpcSerial, squareTreasureNpcIndex, squareTreasureInteriorID, px, py)
	npc = pbLen(npc, 73, []byte(squareTreasureLink)) // KeyLink
	npc = pbVar(npc, 75, 1)                          // AniButtonVisible
	return pbLen(nil, 1, pbLen(nil, 2, npc))
}

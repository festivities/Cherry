package economy

import (
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// Butler memories (Garden LPN agent 10, ch 69 -> hc 80 list, ch 70 -> hc 81 open; butler-robo-spec section 3). A roster
// butler may have memories. The Memories popup lists them as cards (title and list picture); opening one shows its scenes
// in order, each a picture with one line of dialogue and no voice. The pictures are the robo iOS cache's original art
// (cache/butler/butler_memory_master/, served at /cherry/butler/memory/<file>.png). The ids, titles, lines and the top
// message are Cherry-made.

// memoryFolder is the store folder of the memory pictures, relative to the device files/ tree (butlerSkinFS).
const memoryFolder = "cache/butler/butler_memory_master/"

// ButlerMemory is one memory of a roster butler.
//
//	id            the memory's number in its butler: 1 and up, listed once (Cherry-made; the wire memoryId)
//	title         the card's title (Cherry-made)
//	list_picture  the card's picture: the only picture a list row carries (SIMPLE_INFO f6, the list cell's background)
//	thumbnail     the 608 x 430 thumbnail. No protocol field carries it, so it is kept and served but never sent
//	scenes        one or more scenes, in order
type ButlerMemory struct {
	ID          int64         `json:"id"`
	Title       string        `json:"title"`
	ListPicture string        `json:"list_picture"`
	Thumbnail   string        `json:"thumbnail"`
	Scenes      []ButlerScene `json:"scenes"`
}

// ButlerScene is one scene of a memory: its picture (the scene's back image, SCENE_INFO f4) and its one line of dialogue
// (Cherry-made; DETAIL f3). The line has no voice.
type ButlerScene struct {
	Picture string `json:"picture"`
	Line    string `json:"line"`
}

// Memory returns d's memory with id id.
func (d ButlerDef) Memory(id int64) (ButlerMemory, bool) {
	for _, m := range d.Memories {
		if m.ID == id {
			return m, true
		}
	}
	return ButlerMemory{}, false
}

// memoryPictures lists the store paths of every picture of m: the list picture, the thumbnail and each scene's picture.
func memoryPictures(m ButlerMemory) []string {
	paths := []string{m.ListPicture, m.Thumbnail}
	for _, s := range m.Scenes {
		paths = append(paths, s.Picture)
	}
	return paths
}

// isMemoryPath reports whether p is a .png file directly in memoryFolder.
func isMemoryPath(p string) bool {
	name, ok := strings.CutPrefix(p, memoryFolder)
	return ok && len(name) > len(".png") && strings.HasSuffix(name, ".png") && !strings.Contains(name, "/")
}

// checkButlerMemories checks d's memories at load: ids from 1 listed once, a title, at least one scene, a line for every
// scene, and every picture a .png directly in memoryFolder.
func checkButlerMemories(d ButlerDef) error {
	ids := map[int64]bool{}
	for _, m := range d.Memories {
		switch {
		case m.ID < 1 || ids[m.ID]:
			return fmt.Errorf("butler %d: memory id %d must be at least 1 and listed once", d.ID, m.ID)
		case strings.TrimSpace(m.Title) == "":
			return fmt.Errorf("butler %d: memory %d: title must not be empty", d.ID, m.ID)
		case len(m.Scenes) == 0:
			return fmt.Errorf("butler %d: memory %d: scenes must list at least one scene", d.ID, m.ID)
		}
		ids[m.ID] = true
		for _, p := range memoryPictures(m) {
			if !isMemoryPath(p) {
				return fmt.Errorf("butler %d: memory %d: picture %q must be a .png directly in %s", d.ID, m.ID, p, memoryFolder)
			}
		}
		for _, s := range m.Scenes {
			if strings.TrimSpace(s.Line) == "" {
				return fmt.Errorf("butler %d: memory %d: every scene needs a line", d.ID, m.ID)
			}
		}
	}
	return nil
}

// verifyButlerMemories checks at startup that every picture of d's memories is in the archive.
func verifyButlerMemories(d ButlerDef) error {
	for _, m := range d.Memories {
		for _, p := range memoryPictures(m) {
			if _, err := fs.Stat(butlerSkinFS, p); err != nil {
				return fmt.Errorf("butler %d: memory %d: picture %s is not in the archive", d.ID, m.ID, p)
			}
		}
	}
	return nil
}

// memoryListed reports whether a roster butler's memory lists the store path p.
func memoryListed(p string) bool {
	for _, d := range ActiveButlers().Butlers {
		for _, m := range d.Memories {
			if slices.Contains(memoryPictures(m), p) {
				return true
			}
		}
	}
	return false
}

// memoryPicture reads the memory picture file (the route's name without .png) when the data lists it. Any other file of
// the folder is refused, so the route serves the memories' pictures and nothing else.
func memoryPicture(file string) ([]byte, bool) {
	p := memoryFolder + file + ".png"
	if !memoryListed(p) {
		return nil, false
	}
	b, err := fs.ReadFile(butlerSkinFS, p)
	return b, err == nil
}

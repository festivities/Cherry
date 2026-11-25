package economy

import (
	"io/fs"
	"net/http"
	"strings"

	"cherry/internal/assets"
)

const questArtFallbackIcon = "cherry/quest/common_quest_today_quest_icon.png"

// questArtFS is the original UI art tree (UIResource/UIImage_hd) read from the store.
var questArtFS fs.FS = assets.DeviceFiles("UIResource/UIImage_hd")

func safeQuestArtName(name string) bool {
	if !strings.HasSuffix(name, ".png") || len(name) <= len(".png") || strings.Contains(name, "..") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return fs.ValidPath(name)
}

// HandleQuestArt serves GET /cherry/quest/<file>.png from static/ui/UIImage_hd/<file>.png.
// The PNG bytes are original game UI assets; the /cherry/quest/ route and which icon or NPC portrait
// is paired with each Cherry-made quest (defaultQuestIcon, defaultQuestNPC, data/rewards.json) are Cherry-made.
func HandleQuestArt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/cherry/quest/")
	if !safeQuestArtName(name) {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(questArtFS, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(b)
}

func defaultQuestIcon(key string, display bool) string {
	if display {
		return "cherry/quest/quest_symbol_01.png"
	}
	switch key {
	case "garden-own":
		return "cherry/quest/common_quest_list_icon_01.png"
	case "garden-visit":
		return "cherry/quest/common_quest_list_icon_02.png"
	case "square":
		return "cherry/quest/common_quest_list_icon_03.png"
	case "diary":
		return "cherry/quest/common_quest_list_icon_04.png"
	case "gather3":
		return "cherry/quest/quest_symbol_02.png"
	case "atelier-craft":
		return "cherry/quest/quest_symbol_03.png"
	case "square-fish":
		return "cherry/quest/quest_symbol_04.png"
	default:
		return questArtFallbackIcon
	}
}

func defaultQuestNPC(id int64) string {
	switch id {
	case 9401:
		return "cherry/quest/intro_tutorial_npc_1_1.png"
	case 9402:
		return "cherry/quest/intro_tutorial_npc_2_1.png"
	case 9403:
		return "cherry/quest/intro_tutorial_npc_3_1.png"
	case 9404:
		return "cherry/quest/intro_tutorial_npc_4_1.png"
	case 9405:
		return "cherry/quest/butler_tutorial_avatar1.png"
	case 9406:
		return "cherry/quest/butler_tutorial_avatar2.png"
	case 9501:
		return "cherry/quest/anniv_cherry_1.png"
	case 9502:
		return "cherry/quest/birth_ava_cherry_b.png"
	case 9503:
		return "cherry/quest/intro_tutorial_npc_1_2.png"
	case 9504:
		return "cherry/quest/intro_tutorial_npc_2_2.png"
	case 9505:
		return "cherry/quest/intro_tutorial_npc_3_2.png"
	case 9506:
		return "cherry/quest/intro_tutorial_npc_4_2.png"
	case 9507:
		return "cherry/quest/anniv_cherry_2.png"
	default:
		return "cherry/quest/common_quest_popup_npc.png"
	}
}

// QuestIcon returns def's configured icon or the default icon for its trigger key.
func QuestIcon(def QuestDef) string {
	if def.Icon != "" {
		return def.Icon
	}
	return defaultQuestIcon(def.Key, def.Display)
}

func resolveQuestIcon(icon, key string, display bool) string {
	if icon != "" {
		return icon
	}
	return defaultQuestIcon(key, display)
}

func resolveQuestNPC(npcIcon string, id int64) string {
	if npcIcon != "" {
		return npcIcon
	}
	return defaultQuestNPC(id)
}

package admin

import "cherry/internal/store"

// staffRole is a signed-in person's permission tier in the staff panel. Tiers are ordered so every higher tier includes
// everything below it:
//   - Helper:    Item names only (/items, previews, save/reset). Visiting / redirects straight to /items.
//   - Moderator: Dashboard, Players (view, ban/unban, kick, rename, remove content, single-player grant/mail, deleted
//     avatars), Moderation (reports, chat, diary, circles, photos), Insights, Item names, Audit log, Search.
//   - Owner:     Everything above plus Settings, Operator Documents (/docs), Broadcast, All balances, mass mail,
//     and Avatar move/restore.
type staffRole int

const (
	roleNone staffRole = iota
	roleHelper
	roleModerator
	roleOwner
)

// Label is the human-readable role name shown in the top bar and Settings table.
func (r staffRole) Label() string {
	switch r {
	case roleOwner:
		return "Owner"
	case roleModerator:
		return "Moderator"
	case roleHelper:
		return "Helper"
	default:
		return ""
	}
}

// Slug is the lowercase CSS class and stored role value ("owner", "moderator", "helper").
func (r staffRole) Slug() string {
	switch r {
	case roleOwner:
		return store.StaffRoleOwner
	case roleModerator:
		return store.StaffRoleModerator
	case roleHelper:
		return store.StaffRoleHelper
	default:
		return ""
	}
}

// roleLabelOf maps a stored staff role string ("moderator" or "helper") to its display label.
func roleLabelOf(stored string) string {
	switch stored {
	case store.StaffRoleHelper:
		return "Helper"
	default:
		return "Moderator"
	}
}

// roleOf returns the current live role of identity: owner from config, else the role stored in the panel's staff list,
// else Moderator when listed in staff.allowlist-file, else roleNone. Checked on every request so a demotion or removal
// takes effect immediately even if a session somehow outlived it.
func (s *Server) roleOf(identity string) staffRole {
	if identity == "" {
		return roleNone
	}
	if identity == s.owner {
		return roleOwner
	}
	if stored, ok := store.StaffRoleOf(identity); ok {
		if stored == store.StaffRoleHelper {
			return roleHelper
		}
		return roleModerator
	}
	if s.allow.Has(identity) {
		return roleModerator
	}
	return roleNone
}

// mayStaff reports whether identity holds any staff role (owner, moderator or helper).
func (s *Server) mayStaff(identity string) bool {
	return s.roleOf(identity) >= roleHelper
}

// isOwner reports whether the session belongs to the configured owner.
func (s *Server) isOwner(sess session) bool {
	return s.roleOf(sess.identity) == roleOwner
}

package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cherry/internal/config"
)

// Player pictures (slice 8.6b part 3, PLAN P11). Every stored picture has a row in media_files: its kind, the avatar id of the
// account that uploaded it, the name of its file in the kind's folder and its size. Quotas count one owner's rows; a removal
// (the player's own post delete, or a staff action) clears the references to a file and deletes the file with its row. The
// rows are the record: they are read and written in the database, not kept in memory.

// Picture kinds: the folder is the kind plus "-media" under the data directory.
const (
	MediaDiary   = "diary"   // diary photos (/lineplay/d/)
	MediaRoom    = "room"    // My Room preset thumbnails (/lineplay/r/)
	MediaProfile = "profile" // profile head and whole-body pictures (/lineplay/pr/)
)

const (
	roomDownloadPath          = "/lineplay/r/download.nhn"
	profileDownloadPrefix     = "/lineplay/pr/"
	profileDownloadRootPrefix = "/r/lineplay/pr/"
)

// MediaFile is one stored picture. ID is the row id, which is what staff pages name a picture by.
type MediaFile struct {
	ID      int64
	Kind    string
	Owner   string // the avatar id of the uploading account
	File    string // the name in the kind's folder: hex SHA-256 of the key tuple, then ".img"
	Size    int64
	Created string // RFC 3339, UTC
}

// MediaFileName is the stored name of the key tuple a client names a picture by: its userid, its ctime and its oid.
func MediaFileName(userID, ctime, oid string) string {
	sum := sha256.Sum256([]byte(userID + "\x00" + ctime + "\x00" + oid))
	return hex.EncodeToString(sum[:]) + ".img"
}

// mediaFileOK accepts only the names MediaFileName makes, so a name from a row or a URL is never a path.
func mediaFileOK(name string) bool {
	if len(name) != 68 || !strings.HasSuffix(name, ".img") {
		return false
	}
	for i := 0; i < 64; i++ {
		if c := name[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// MediaDir is the folder of one kind under the data directory.
func MediaDir(kind string) (string, error) {
	switch kind {
	case MediaDiary, MediaRoom, MediaProfile:
	default:
		return "", fmt.Errorf("unknown picture kind %q", kind)
	}
	dataDir, err := config.DataDir()
	if err != nil {
		return "", err
	}
	if dataDir == "" {
		return "", errors.New("empty data directory")
	}
	return filepath.Join(dataDir, kind+"-media"), nil
}

// AddMediaFile records a picture the caller has just written under its owner.
func AddMediaFile(f MediaFile) error {
	d := attachedDB()
	if d == nil {
		return ErrNoDatabase
	}
	f.Created = time.Now().UTC().Format(time.RFC3339)
	return d.Update(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO media_files (kind, owner, file, size, created) VALUES (?, ?, ?, ?, ?)`,
			f.Kind, f.Owner, f.File, f.Size, f.Created)
		return err
	})
}

// MediaUsage is the bytes of every picture (all kinds together) that owner holds: the player's quota counts these.
func MediaUsage(owner string) (bytes int64, err error) {
	d := attachedDB()
	if d == nil {
		return 0, ErrNoDatabase
	}
	err = d.View(func(q Querier) error {
		return q.QueryRow(`SELECT COALESCE(SUM(size), 0) FROM media_files WHERE owner = ?`, owner).Scan(&bytes)
	})
	return bytes, err
}

// MediaTotals is the pictures the database holds per kind (one query over media_files; a kind with none is absent). The
// server-wide cap sums their bytes and the dashboard shows them.
func MediaTotals() (map[string]MediaKindTotal, error) {
	d := attachedDB()
	if d == nil {
		return nil, ErrNoDatabase
	}
	out := map[string]MediaKindTotal{}
	err := d.View(func(q Querier) error {
		r, err := q.Query(`SELECT kind, COUNT(*), COALESCE(SUM(size), 0) FROM media_files GROUP BY kind`)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var t MediaKindTotal
			if err := r.Scan(&t.Kind, &t.Files, &t.Bytes); err != nil {
				return err
			}
			out[t.Kind] = t
		}
		return r.Err()
	})
	return out, err
}

// MediaKindTotal is the pictures of one kind that the database holds: their count and their bytes.
type MediaKindTotal struct {
	Kind  string
	Files int64
	Bytes int64
}

// mediaRow reads the row where column = arg; found is false when there is none.
func mediaRow(q Querier, column string, arg any) (f MediaFile, found bool, err error) {
	err = q.QueryRow(`SELECT id, kind, owner, file, size, created FROM media_files WHERE `+column+` = ?`, arg).
		Scan(&f.ID, &f.Kind, &f.Owner, &f.File, &f.Size, &f.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return MediaFile{}, false, nil
	}
	return f, err == nil, err
}

// MediaRefFile is the stored file that a picture URL names: a diary or My Room download (userid, ctime and oid in the query)
// or a profile download (oid = userid_ctime in the path), relative or absolute. ok is false for any other URL.
func MediaRefFile(raw string) (file string, ok bool) {
	if len(raw) == 0 || len(raw) > 1024 {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	p := u.Path
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	switch {
	case p == DiaryDownloadPath || p == roomDownloadPath:
		q := u.Query()
		userID, okUser := OneDiaryMediaQueryValue(q, "userid")
		ctime, okTime := OneDiaryMediaQueryValue(q, "ctime")
		oid, okOID := OneDiaryMediaQueryValue(q, "oid")
		if !okUser || !okTime || !okOID || !ValidDiaryMediaUserID(userID) || !ValidDiaryMediaCTime(ctime) || len(oid) > 256 || oid == "" {
			return "", false
		}
		return MediaFileName(userID, ctime, oid), true
	case strings.HasPrefix(p, profileDownloadPrefix) || strings.HasPrefix(p, profileDownloadRootPrefix):
		oid := p[strings.LastIndex(p, "/")+1:]
		i := strings.LastIndex(oid, "_")
		if i < 0 || !ValidDiaryMediaTuple(oid[:i], oid[i+1:], oid) {
			return "", false
		}
		return MediaFileName(oid[:i], oid[i+1:], oid), true
	}
	return "", false
}

// MediaRowByFile returns the row of stored file name, if one exists.
func MediaRowByFile(file string) (MediaFile, bool, error) {
	d := attachedDB()
	if d == nil {
		return MediaFile{}, false, ErrNoDatabase
	}
	var f MediaFile
	var found bool
	err := d.View(func(q Querier) (err error) {
		f, found, err = mediaRow(q, "file", file)
		return err
	})
	return f, found, err
}

// MediaOwnedBy reports whether the picture URL raw names a stored picture that the account aid uploaded. A URL with no row (a
// file from before slice 8.6b part 3, or none) is not owned, and neither is one without a database.
func MediaOwnedBy(raw, aid string) bool {
	file, ok := MediaRefFile(raw)
	if !ok || aid == "" {
		return false
	}
	f, found, err := MediaRowByFile(file)
	return err == nil && found && f.Owner == aid
}

// PlayerMedia returns the newest limit pictures the player with row id uploaded, newest first.
func PlayerMedia(id int64, limit int) ([]MediaFile, error) {
	AccountsMu.Lock()
	acc := playerLocked(id)
	aid := ""
	if acc != nil {
		aid = acc.Aid
	}
	AccountsMu.Unlock()
	if acc == nil {
		return nil, ErrNoPlayer
	}
	rows := []MediaFile{}
	d := attachedDB()
	if d == nil {
		return rows, nil
	}
	err := d.View(func(q Querier) error {
		r, err := q.Query(`SELECT id, kind, owner, file, size, created FROM media_files WHERE owner = ? ORDER BY id DESC LIMIT ?`, aid, limit)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var f MediaFile
			if err := r.Scan(&f.ID, &f.Kind, &f.Owner, &f.File, &f.Size, &f.Created); err != nil {
				return err
			}
			rows = append(rows, f)
		}
		return r.Err()
	})
	return rows, err
}

// refersTo reports whether the URL raw names file.
func refersTo(raw, file string) bool {
	f, ok := MediaRefFile(raw)
	return ok && f == file
}

// referencedFilesLocked is the set of stored files the live data names: diary post images, profile photos and pictures, and
// room thumbnails. Caller holds AccountsMu and SocialMu.
func referencedFilesLocked() map[string]bool {
	refs := map[string]bool{}
	add := func(raw string) {
		if f, ok := MediaRefFile(raw); ok {
			refs[f] = true
		}
	}
	for _, p := range DiaryPosts {
		for _, img := range p.Images {
			add(img.ImageURL)
		}
	}
	for _, acc := range accountList {
		add(acc.ProfileImage.Image)
		add(acc.ProfileImage.WholeBody)
		if acc.DiaryProfile != nil {
			for _, ph := range acc.DiaryProfile.Photos {
				add(ph.ImageURL)
			}
		}
		for _, pr := range acc.Presets {
			add(pr.Image)
		}
	}
	return refs
}

// unusedMediaLocked returns the rows of the pictures that the removed posts named, that owner uploaded and that the live data
// no longer names. Call it after DiaryPosts holds the new list. Caller holds AccountsMu and SocialMu.
func unusedMediaLocked(owner string, removed []DiaryPost) ([]MediaFile, error) {
	d := attachedDB()
	if d == nil {
		return nil, nil
	}
	var candidates []string
	for _, p := range removed {
		for _, img := range p.Images {
			if f, ok := MediaRefFile(img.ImageURL); ok && !slices.Contains(candidates, f) {
				candidates = append(candidates, f)
			}
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	refs := referencedFilesLocked()
	var out []MediaFile
	err := d.View(func(q Querier) error {
		for _, f := range candidates {
			if refs[f] {
				continue
			}
			row, found, err := mediaRow(q, "file", f)
			if err != nil {
				return err
			}
			if found && row.Owner == owner {
				out = append(out, row)
			}
		}
		return nil
	})
	return out, err
}

// saveSocialMediaLocked is SaveSocialLocked with the rows of media removed in the same transaction and, when audit is set, its
// audit row. Without a database it saves nothing (memory only) unless an audit row is due, which fails with ErrNoDatabase.
// Caller holds SocialMu.
func saveSocialMediaLocked(c SocialChange, media []MediaFile, audit *staffAudit) error {
	d := attachedDB()
	if d == nil {
		if audit != nil {
			return ErrNoDatabase
		}
		return SaveSocialLocked(c)
	}
	site := callerSite()
	if err := d.Update(func(tx *sql.Tx) error {
		if err := saveSocialRowsTx(tx, c); err != nil {
			return err
		}
		for _, m := range media {
			if _, err := tx.Exec(`DELETE FROM media_files WHERE id = ?`, m.ID); err != nil {
				return err
			}
		}
		if audit != nil {
			return auditTx(tx, audit.actor, audit.action, audit.target, audit.detail)
		}
		return nil
	}); err != nil {
		return err
	}
	if testing.Testing() {
		netCheckSocial(d, site)
	}
	return nil
}

// AccountMediaLocked returns every picture row owner uploaded, for Delete Avatar, which takes all of them. Nil without a
// database. Caller holds AccountsMu.
func AccountMediaLocked(owner string) ([]MediaFile, error) {
	d := attachedDB()
	if d == nil {
		return nil, nil
	}
	var out []MediaFile
	err := d.View(func(q Querier) error {
		r, err := q.Query(`SELECT id, kind, owner, file, size, created FROM media_files WHERE owner = ? ORDER BY id`, owner)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var f MediaFile
			if err := r.Scan(&f.ID, &f.Kind, &f.Owner, &f.File, &f.Size, &f.Created); err != nil {
				return err
			}
			out = append(out, f)
		}
		return r.Err()
	})
	return out, err
}

// RemoveMediaFiles deletes the files of rows that a committed save removed. A failed delete is logged: the row is gone, so the
// file is an orphan that the folder's disk safety net still counts.
func RemoveMediaFiles(files []MediaFile) {
	for _, f := range files {
		dir, err := MediaDir(f.Kind)
		if err != nil || !mediaFileOK(f.File) {
			log.Printf("cherry: media file of kind %s not removed: bad name or folder", f.Kind)
			continue
		}
		if err := os.Remove(filepath.Join(dir, f.File)); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("cherry: media file of kind %s not removed: %v", f.Kind, err)
		}
	}
}

// EraseDiaryPost is the owner's in-game delete of diary post no (web handler /v4/diary2/erase): the post with its comments and
// likes, and the pictures that only it named. A post that is not aid's is left alone, as the in-game delete always answers OK.
func EraseDiaryPost(aid, no string) error {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	SocialMu.Lock()
	defer SocialMu.Unlock()
	_, err := erasePostLocked(aid, no, nil)
	return err
}

// erasePostLocked removes diary post no of aid with its comments and likes, and the pictures only it named, in one save (with
// audit when set). found is false when the post is not aid's (nothing is saved). Caller holds AccountsMu and SocialMu.
func erasePostLocked(aid, no string, audit *staffAudit) (found bool, err error) {
	i := slices.IndexFunc(DiaryPosts, func(p DiaryPost) bool { return p.DiaryNo == no && p.AvatarID == aid })
	if i < 0 {
		return false, nil
	}
	prevPosts, prevComments, prevLikes := DiaryPosts, DiaryComments, DiaryLikes
	var comments, likes []string
	keptComments := []DiaryComment{}
	for _, c := range prevComments {
		if c.OwnerAvatarID == aid && c.DiaryNo == no {
			comments = append(comments, c.CommentNo)
		} else {
			keptComments = append(keptComments, c)
		}
	}
	keptLikes := []DiaryLike{}
	for _, l := range prevLikes {
		if l.OwnerAvatarID == aid && l.DiaryNo == no {
			likes = append(likes, l.LikeNo)
		} else {
			keptLikes = append(keptLikes, l)
		}
	}
	removed := []DiaryPost{prevPosts[i]}
	DiaryPosts, DiaryComments, DiaryLikes = slices.Delete(slices.Clone(prevPosts), i, i+1), keptComments, keptLikes
	media, err := unusedMediaLocked(aid, removed)
	if err != nil {
		DiaryPosts, DiaryComments, DiaryLikes = prevPosts, prevComments, prevLikes
		return true, err
	}
	if err := saveSocialMediaLocked(SocialChange{Posts: []string{no}, Comments: comments, Likes: likes}, media, audit); err != nil {
		DiaryPosts, DiaryComments, DiaryLikes = prevPosts, prevComments, prevLikes
		return true, err
	}
	RemoveMediaFiles(media)
	return true, nil
}

// RemovePlayerMedia removes one stored picture (media row id mediaID) of the player with row id: the row, its file and every
// reference to the file. A diary post loses that image and stays; a profile loses that photo or picture; a room preset loses its
// thumbnail (the stock default is none). The changed posts and accounts, the row removal and the audit row (the row id, the
// kind and the reason, never the picture) are one transaction; the file goes after the commit. ErrNoItem when the row is not
// the player's.
func RemovePlayerMedia(id, mediaID int64, reason, actor string) error {
	reason, err := cleanReason(reason)
	if err != nil {
		return err
	}
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	acc := playerLocked(id)
	if acc == nil {
		return ErrNoPlayer
	}
	SocialMu.Lock()
	defer SocialMu.Unlock()
	d := attachedDB()
	if d == nil {
		return ErrNoDatabase
	}
	var row MediaFile
	var found bool
	if err := d.View(func(q Querier) (err error) {
		row, found, err = mediaRow(q, "id", mediaID)
		return err
	}); err != nil {
		return err
	}
	if !found || row.Owner != acc.Aid || !mediaFileOK(row.File) {
		return ErrNoItem
	}
	return removeMediaLocked(d, row, reason, actor, playerTarget(id))
}

// removeMediaLocked is the body of a picture removal: the references to row's file go (posts, profiles, room thumbnails), the
// row goes, and the audit row (target, the owner's aid, the row id and kind, the reason) is written in the same transaction; the
// file goes after the commit. Caller holds AccountsMu and SocialMu, and d is the attached database.
func removeMediaLocked(d *DB, row MediaFile, reason, actor, target string) error {
	var err error
	site := callerSite()

	// The changes, each kept with its previous value for a rollback.
	prevPosts := DiaryPosts
	newPosts := slices.Clone(prevPosts)
	var changedPosts []string
	for i, p := range newPosts {
		kept := []DiaryImage{}
		for _, img := range p.Images {
			if !refersTo(img.ImageURL, row.File) {
				kept = append(kept, img)
			}
		}
		if len(kept) != len(p.Images) {
			p.Images = kept
			p.ImageLocationType = DiaryImageLocationType(kept)
			newPosts[i] = p
			changedPosts = append(changedPosts, p.DiaryNo)
		}
	}
	type accountBefore struct {
		acc     *Account
		image   ProfileImage
		profile *DiaryProfile
		presets map[string]RoomPreset
	}
	var before []accountBefore
	var changedAccs []*Account
	for _, a := range accountList {
		prev := accountBefore{a, a.ProfileImage, a.DiaryProfile, a.Presets}
		changed := false
		if refersTo(a.ProfileImage.Image, row.File) {
			a.ProfileImage.Image, changed = "", true
		}
		if refersTo(a.ProfileImage.WholeBody, row.File) {
			a.ProfileImage.WholeBody, changed = "", true
		}
		if a.DiaryProfile != nil {
			kept := []ProfilePhoto{}
			for _, ph := range a.DiaryProfile.Photos {
				if !refersTo(ph.ImageURL, row.File) {
					kept = append(kept, ph)
				}
			}
			if len(kept) != len(a.DiaryProfile.Photos) {
				next := *a.DiaryProfile
				next.Photos = kept
				a.DiaryProfile, changed = &next, true
			}
		}
		var hit []string
		for k, pr := range a.Presets {
			if refersTo(pr.Image, row.File) {
				hit = append(hit, k)
			}
		}
		if len(hit) > 0 {
			np := maps.Clone(a.Presets)
			for _, k := range hit {
				pr := np[k]
				pr.Image = ""
				np[k] = pr
			}
			a.Presets, changed = np, true
		}
		if changed {
			before = append(before, prev)
			changedAccs = append(changedAccs, a)
		}
	}
	DiaryPosts = newPosts
	restore := func() {
		DiaryPosts = prevPosts
		for _, b := range before {
			b.acc.ProfileImage, b.acc.DiaryProfile, b.acc.Presets = b.image, b.profile, b.presets
		}
	}

	sums := make([]string, len(changedAccs))
	err = d.Update(func(tx *sql.Tx) error {
		for i, a := range changedAccs {
			sum, err := updateAccountRowTx(tx, accountRows[a].id, a)
			if err != nil {
				return err
			}
			sums[i] = sum
		}
		if err := saveSocialRowsTx(tx, SocialChange{Posts: changedPosts}); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM media_files WHERE id = ?`, row.ID); err != nil {
			return err
		}
		return auditTx(tx, actor, "remove-media", target, map[string]any{
			"aid": row.Owner, "media": row.ID, "kind": row.Kind, "reason": reason,
		})
	})
	if err != nil {
		restore()
		return err
	}
	for i, a := range changedAccs {
		accountRows[a].sum = sums[i]
		accountRows[a].revoke = false
	}
	if testing.Testing() {
		checkNotMissed(changedAccs, site)
		netCheckSocial(d, site)
	}
	RemoveMediaFiles([]MediaFile{row})
	return nil
}

// deleteMediaRowsTx deletes the rows of stale from media_files inside an existing transaction.
func deleteMediaRowsTx(tx *sql.Tx, stale []MediaFile) error {
	for _, m := range stale {
		if _, err := tx.Exec(`DELETE FROM media_files WHERE id = ?`, m.ID); err != nil {
			return err
		}
	}
	return nil
}

// SaveProfileImageLocked updates acc.ProfileImage and removes every unreferenced MediaProfile row and file owned by
// acc.Aid (both the superseded profile picture and any abandoned profile uploads) in one transaction. Caller holds
// AccountsMu and does not hold SocialMu.
func SaveProfileImageLocked(acc *Account, next ProfileImage) error {
	SocialMu.Lock()
	defer SocialMu.Unlock()
	prev := acc.ProfileImage
	acc.ProfileImage = next
	var stale []MediaFile
	if d := attachedDB(); d != nil {
		refs := referencedFilesLocked()
		err := d.View(func(q Querier) error {
			r, err := q.Query(`SELECT id, kind, owner, file, size, created FROM media_files WHERE owner = ? AND kind = ?`, acc.Aid, MediaProfile)
			if err != nil {
				return err
			}
			defer r.Close()
			for r.Next() {
				var f MediaFile
				if err := r.Scan(&f.ID, &f.Kind, &f.Owner, &f.File, &f.Size, &f.Created); err != nil {
					return err
				}
				if !refs[f.File] {
					stale = append(stale, f)
				}
			}
			return r.Err()
		})
		if err != nil {
			acc.ProfileImage = prev
			return err
		}
	}
	var extra func(*sql.Tx) error
	if len(stale) > 0 {
		extra = func(tx *sql.Tx) error { return deleteMediaRowsTx(tx, stale) }
	}
	if err := commitLocked(nil, []*Account{acc}, 0, callerSite(), nil, extra); err != nil {
		acc.ProfileImage = prev
		return err
	}
	RemoveMediaFiles(stale)
	return nil
}

// unusedOwnerMediaByURLsLocked finds the media_files rows owned by owner (optionally filtered to kind when non-empty)
// named by urls that are no longer in refs. Caller holds AccountsMu and SocialMu.
func unusedOwnerMediaByURLsLocked(owner, kind string, urls []string, refs map[string]bool) ([]MediaFile, error) {
	d := attachedDB()
	if d == nil || len(urls) == 0 {
		return nil, nil
	}
	var candidates []string
	for _, raw := range urls {
		if f, ok := MediaRefFile(raw); ok && !refs[f] && !slices.Contains(candidates, f) {
			candidates = append(candidates, f)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	var out []MediaFile
	err := d.View(func(q Querier) error {
		for _, f := range candidates {
			row, found, err := mediaRow(q, "file", f)
			if err != nil {
				return err
			}
			if found && row.Owner == owner && (kind == "" || row.Kind == kind) {
				out = append(out, row)
			}
		}
		return nil
	})
	return out, err
}

// SaveRoomPresetsLocked updates acc.Presets and deletes any superseded MediaRoom rows/files owned by acc.Aid that are
// no longer referenced anywhere, in one transaction. Caller holds AccountsMu and does not hold SocialMu.
func SaveRoomPresetsLocked(acc *Account, next map[string]RoomPreset, supersededURLs ...string) error {
	SocialMu.Lock()
	defer SocialMu.Unlock()
	prev := acc.Presets
	acc.Presets = next
	var stale []MediaFile
	if len(supersededURLs) > 0 && attachedDB() != nil {
		var err error
		stale, err = unusedOwnerMediaByURLsLocked(acc.Aid, MediaRoom, supersededURLs, referencedFilesLocked())
		if err != nil {
			acc.Presets = prev
			return err
		}
	}
	var extra func(*sql.Tx) error
	if len(stale) > 0 {
		extra = func(tx *sql.Tx) error { return deleteMediaRowsTx(tx, stale) }
	}
	if err := commitLocked(nil, []*Account{acc}, 0, callerSite(), nil, extra); err != nil {
		acc.Presets = prev
		return err
	}
	RemoveMediaFiles(stale)
	return nil
}

// SaveProfilePhotosLocked updates acc.DiaryProfile and deletes any superseded profile-photo rows/files owned by acc.Aid
// that are no longer referenced anywhere, in one transaction. Caller holds AccountsMu and does not hold SocialMu.
func SaveProfilePhotosLocked(acc *Account, next *DiaryProfile, supersededURLs ...string) error {
	SocialMu.Lock()
	defer SocialMu.Unlock()
	prev := acc.DiaryProfile
	acc.DiaryProfile = next
	var stale []MediaFile
	if len(supersededURLs) > 0 && attachedDB() != nil {
		var err error
		stale, err = unusedOwnerMediaByURLsLocked(acc.Aid, "", supersededURLs, referencedFilesLocked())
		if err != nil {
			acc.DiaryProfile = prev
			return err
		}
	}
	var extra func(*sql.Tx) error
	if len(stale) > 0 {
		extra = func(tx *sql.Tx) error { return deleteMediaRowsTx(tx, stale) }
	}
	if err := commitLocked(nil, []*Account{acc}, 0, callerSite(), nil, extra); err != nil {
		acc.DiaryProfile = prev
		return err
	}
	RemoveMediaFiles(stale)
	return nil
}

// ModifyDiaryPost applies edit to aid's diary post diaryNo and deletes any diary media rows/files that the old post
// referenced and nothing references anymore, in one transaction. Returns the updated post and whether it was found.
// Caller holds neither AccountsMu nor SocialMu.
func ModifyDiaryPost(aid, diaryNo string, edit func(*DiaryPost)) (DiaryPost, bool, error) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	SocialMu.Lock()
	defer SocialMu.Unlock()
	idx := slices.IndexFunc(DiaryPosts, func(p DiaryPost) bool { return p.AvatarID == aid && p.DiaryNo == diaryNo })
	if idx < 0 {
		return DiaryPost{}, false, nil
	}
	old := DiaryPosts[idx]
	edit(&DiaryPosts[idx])
	stale, err := unusedMediaLocked(aid, []DiaryPost{old})
	if err != nil {
		DiaryPosts[idx] = old
		return DiaryPost{}, true, err
	}
	if err := saveSocialMediaLocked(SocialChange{Posts: []string{diaryNo}}, stale, nil); err != nil {
		DiaryPosts[idx] = old
		return DiaryPost{}, true, err
	}
	RemoveMediaFiles(stale)
	return DiaryPosts[idx], true, nil
}

// deletedAvatarMediaLocked returns the unrestored deleted avatar ids and every media file their records reference, so
// PruneStaleMedia never removes pictures preserved for a deleted avatar.
func deletedAvatarMediaLocked(q Querier) (aids map[string]bool, files map[string]bool, err error) {
	aids, files = map[string]bool{}, map[string]bool{}
	rows, err := q.Query(`SELECT aid, data FROM deleted_avatars WHERE restored_at = ''`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	addURL := func(raw string) {
		if f, ok := MediaRefFile(raw); ok {
			files[f] = true
		}
	}
	for rows.Next() {
		var aid, raw string
		if err := rows.Scan(&aid, &raw); err != nil {
			return nil, nil, err
		}
		if aid != "" {
			aids[aid] = true
		}
		var rec AvatarRecord
		if json.Unmarshal([]byte(raw), &rec) != nil {
			continue
		}
		for _, m := range rec.Media {
			if m.File != "" {
				files[m.File] = true
			}
		}
		if rec.Account.ProfileImage != nil {
			addURL(rec.Account.ProfileImage.Image)
			addURL(rec.Account.ProfileImage.WholeBody)
		}
		if rec.Account.DiaryProfile != nil {
			for _, ph := range rec.Account.DiaryProfile.Photos {
				addURL(ph.ImageURL)
			}
		}
		for _, pr := range rec.Account.Presets {
			addURL(pr.Image)
		}
		for _, p := range rec.Posts {
			for _, img := range p.Images {
				addURL(img.ImageURL)
			}
		}
	}
	return aids, files, rows.Err()
}

// PruneStaleMedia deletes media_files rows (and their .img files) that are neither referenced by live accounts/posts nor
// preserved for an unrestored deleted avatar, provided they are at least minAge old (protecting in-flight uploads). It
// also removes unreferenced orphan .img files on disk under {profile,room,diary}-media older than minAge.
func PruneStaleMedia(now time.Time, minAge time.Duration) (rowsDeleted int, bytesFreed int64, orphanFiles int, err error) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	SocialMu.Lock()
	defer SocialMu.Unlock()
	d := attachedDB()
	if d == nil {
		return 0, 0, 0, nil
	}
	refs := referencedFilesLocked()
	var stale []MediaFile
	keptFiles := map[string]bool{}
	var deletedFiles map[string]bool
	err = d.View(func(q Querier) error {
		deletedAids, df, err := deletedAvatarMediaLocked(q)
		if err != nil {
			return err
		}
		deletedFiles = df
		r, err := q.Query(`SELECT id, kind, owner, file, size, created FROM media_files`)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var f MediaFile
			if err := r.Scan(&f.ID, &f.Kind, &f.Owner, &f.File, &f.Size, &f.Created); err != nil {
				return err
			}
			protected := refs[f.File] || deletedFiles[f.File] || deletedAids[f.Owner]
			if protected {
				keptFiles[f.File] = true
				continue
			}
			if minAge > 0 {
				ts, perr := time.Parse(time.RFC3339, f.Created)
				if perr != nil || now.Sub(ts) < minAge {
					keptFiles[f.File] = true
					continue
				}
			}
			stale = append(stale, f)
		}
		return r.Err()
	})
	if err != nil {
		return 0, 0, 0, err
	}
	if len(stale) > 0 {
		if err := d.Update(func(tx *sql.Tx) error { return deleteMediaRowsTx(tx, stale) }); err != nil {
			return 0, 0, 0, err
		}
		for _, f := range stale {
			bytesFreed += f.Size
		}
		rowsDeleted = len(stale)
		RemoveMediaFiles(stale)
	}
	for _, kind := range []string{MediaProfile, MediaRoom, MediaDiary} {
		dir, derr := MediaDir(kind)
		if derr != nil {
			continue
		}
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !mediaFileOK(entry.Name()) {
				continue
			}
			name := entry.Name()
			if keptFiles[name] || refs[name] || deletedFiles[name] {
				continue
			}
			info, ierr := entry.Info()
			if ierr != nil || !info.Mode().IsRegular() {
				continue
			}
			if minAge > 0 && now.Sub(info.ModTime()) < minAge {
				continue
			}
			if rmErr := os.Remove(filepath.Join(dir, name)); rmErr == nil {
				orphanFiles++
			}
		}
	}
	return rowsDeleted, bytesFreed, orphanFiles, nil
}

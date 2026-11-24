package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// The staff photo page (PLAN Roadmap 8, staff "Moderation"): the stored pictures of media_files, newest first, with the
// uploader's account. The pictures are read from their folders by the staff route only; nothing here changes a row.

// PictureRow is one stored picture as the photo page shows it: its row, and the uploader's account row id (0 when the
// uploader has no account row now) and name.
type PictureRow struct {
	MediaFile
	PlayerID  int64
	OwnerName string
}

// PictureFilter narrows the photo page. Kind matches exactly; Player matches the uploader's avatar id exactly or the
// uploader's name as a substring (any case).
type PictureFilter struct {
	Kind, Player string
}

// MediaSearch returns up to limit pictures the filter keeps, newest first, with ids below before (0 = from the newest).
// ErrBadInput for a kind no picture has or a limit outside 1 to 200.
func MediaSearch(f PictureFilter, before int64, limit int) ([]PictureRow, error) {
	d := attachedDB()
	if d == nil {
		return nil, ErrNoDatabase
	}
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("%w: the photo page is 1 to 200 pictures", ErrBadInput)
	}
	var where []string
	var args []any
	if before > 0 {
		where = append(where, "m.id < ?")
		args = append(args, before)
	}
	if f.Kind != "" {
		if !slices.Contains([]string{MediaDiary, MediaRoom, MediaProfile}, f.Kind) {
			return nil, fmt.Errorf("%w: unknown picture kind", ErrBadInput)
		}
		where = append(where, "m.kind = ?")
		args = append(args, f.Kind)
	}
	if f.Player != "" {
		where = append(where, "(m.owner = ? OR instr(lower(a.name), lower(?)) > 0)")
		args = append(args, f.Player, f.Player)
	}
	args = append(args, limit)
	// a.aid <> '0' matches the partial unique index accounts_aid(aid) WHERE aid <> '0' so SQLite uses an index lookup
	// instead of scanning the accounts table.
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}
	query := `SELECT m.id, m.kind, m.owner, m.file, m.size, m.created, COALESCE(a.id, 0), COALESCE(a.name, '')
		FROM media_files m LEFT JOIN accounts a ON a.aid = m.owner AND a.aid <> '0'` +
		whereSQL + ` ORDER BY m.id DESC LIMIT ?`
	out := []PictureRow{}
	err := d.View(func(q Querier) error {
		rows, err := q.Query(query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r PictureRow
			if err := rows.Scan(&r.ID, &r.Kind, &r.Owner, &r.File, &r.Size, &r.Created, &r.PlayerID, &r.OwnerName); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// MediaRowByID returns the picture row with id; found is false when there is none.
func MediaRowByID(id int64) (MediaFile, bool, error) {
	d := attachedDB()
	if d == nil {
		return MediaFile{}, false, ErrNoDatabase
	}
	var f MediaFile
	var found bool
	err := d.View(func(q Querier) (err error) {
		f, found, err = mediaRow(q, "id", id)
		return err
	})
	return f, found, err
}

// MediaPath is the file of a stored picture inside its kind's folder. A row whose name is not the one MediaFileName makes,
// or whose path would leave the folder, has no path: the caller answers as for a missing file.
func MediaPath(f MediaFile) (string, error) {
	if !mediaFileOK(f.File) {
		return "", errors.New("picture row has a bad file name")
	}
	dir, err := MediaDir(f.Kind)
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, f.File)
	if filepath.Dir(p) != filepath.Clean(dir) {
		return "", errors.New("picture path is outside its folder")
	}
	return p, nil
}

// PlayerRowOfAid returns the row id of the account whose avatar id is aid. An avatar id of "0" names no account (several
// accounts have none yet), so it finds nothing.
func PlayerRowOfAid(aid string) (int64, bool) {
	AccountsMu.Lock()
	defer AccountsMu.Unlock()
	if aid == "0" {
		return 0, false
	}
	for _, acc := range accountList {
		if acc.Aid == aid {
			return accountRows[acc].id, true
		}
	}
	return 0, false
}

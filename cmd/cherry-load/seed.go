package main

import (
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"cherry/internal/store"
)

// The lab's guest header secret (internal/web guestSecret): the load players sign their guest logins with it.
const guestSecret = "vocky7dkd722i1u8ak84o"

// heavyEvery makes every 100th seeded account (1 %) heavy: heavyOwned owned codes, as the load target describes.
const (
	heavyEvery = 100
	heavyOwned = 3000
)

// ownedCats are the clothing categories of the synthetic item codes (CU + category + five digits).
var ownedCats = []string{"HA", "ON", "TO", "PA", "SH", "AH", "AE", "FE"}

// guestCode is the device uniqueCode of seeded account i; the load players log in with it.
func guestCode(i int) string { return fmt.Sprintf("lt%08x-0000-4000-8000-%012x", i, i) }

// guestHeader is the X-LINEPLAY-ACNT value for a device uniqueCode (lowercase hex MD5 of code + secret).
func guestHeader(code string) string {
	sum := md5.Sum([]byte(code + guestSecret))
	return hex.EncodeToString(sum[:])
}

// ownedCode is the j-th synthetic item code.
func ownedCode(j int) string {
	return "CU" + ownedCats[j%len(ownedCats)] + fmt.Sprintf("%05d", j/len(ownedCats)+1)
}

func isHeavy(i int) bool { return i%heavyEvery == 0 }

// seededAids are the avatar ids the seed gives accounts 0..n-1, by the creation rule the seed follows (aids start at
// MinAvatarID and skip FriendAID). The players name a diary's owner by it.
func seededAids(n int) []string {
	out := make([]string, n)
	var next uint64
	for i := range out {
		next = max(next, store.MinAvatarID-1) + 1
		if strconv.FormatUint(next, 10) == store.FriendAID {
			next++
		}
		out[i] = strconv.FormatUint(next, 10)
	}
	return out
}

// prepareDir makes dir and refuses a directory with files in it, unless force: then the cherry.db files of that
// directory are removed first and other files are left alone.
func prepareDir(dir string, force bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	if !force {
		return fmt.Errorf("%s is not empty; pass -force to replace the database in it", dir)
	}
	for _, name := range []string{store.DBFileName, store.DBFileName + "-wal", store.DBFileName + "-shm"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// runSeed writes a new database of synthetic accounts through the store's own write path (NewAccount, SaveLocked,
// SaveSocialLocked). Every name, guest code, item, mail and diary text it writes is synthetic.
func runSeed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	dir := fs.String("data-dir", "", "data directory to create the database in (empty, or use -force)")
	n := fs.Int("accounts", 10000, "synthetic accounts to write")
	force := fs.Bool("force", false, "replace the cherry.db files of a non-empty data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *n < 1 {
		return fmt.Errorf("seed: -data-dir and a positive -accounts are required")
	}
	if err := prepareDir(*dir, *force); err != nil {
		return err
	}
	start := time.Now()
	if err := store.OpenStore(filepath.Join(*dir, store.DBFileName)); err != nil {
		return err
	}
	now := time.Now()
	aids := make([]string, *n)
	names := make([]string, *n)
	heavy := 0
	for i := 0; i < *n; i++ {
		aid, err := seedAccount(i, now)
		if err != nil {
			store.CloseStore()
			return fmt.Errorf("account %d: %w", i, err)
		}
		aids[i], names[i] = aid, fmt.Sprintf("Load %05d", i)
		if isHeavy(i) {
			heavy++
		}
	}
	posts, friends, err := seedSocial(aids, names, now)
	if err != nil {
		store.CloseStore()
		return err
	}
	if err := store.CloseStore(); err != nil {
		return err
	}
	db, wal := fileSize(filepath.Join(*dir, store.DBFileName)), fileSize(filepath.Join(*dir, store.DBFileName+"-wal"))
	fmt.Println("SYNTHETIC DATA written by cherry-load seed (not a player record)")
	fmt.Printf("database=%s\n", filepath.Join(*dir, store.DBFileName))
	fmt.Printf("accounts=%d heavy=%d (%d owned codes each, %.1f%%) diary-posts=%d friendships=%d\n",
		*n, heavy, heavyOwned, 100*float64(heavy)/float64(*n), posts, friends)
	fmt.Printf("guest codes lt<hex i>-0000-4000-8000-<hex i>, first %s\n", guestCode(0))
	fmt.Printf("database bytes=%d wal bytes=%d seeded in %s\n", db, wal, time.Since(start).Round(time.Millisecond))
	return nil
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// seedAccount creates account i: a guest-bound avatar with items, a room, two mail rows and a balance; heavy accounts
// own heavyOwned codes. It returns the avatar id.
func seedAccount(i int, now time.Time) (string, error) {
	acc, err := store.NewAccount() // the creation save: a new record with no avatar yet
	if err != nil {
		return "", err
	}
	code := guestCode(i)
	store.AccountsMu.Lock()
	defer store.AccountsMu.Unlock()
	// The aid rule of the avatar creation (web handleCreateAvatar): aids start at MinAvatarID and skip FriendAID.
	store.NextAvatarID = max(store.NextAvatarID, store.MinAvatarID-1) + 1
	if strconv.FormatUint(store.NextAvatarID, 10) == store.FriendAID {
		store.NextAvatarID++
	}
	acc.Aid = strconv.FormatUint(store.NextAvatarID, 10)
	acc.Name = fmt.Sprintf("Load %05d", i)
	acc.Gender = map[bool]string{true: "F", false: "M"}[i%2 == 0]
	acc.Skin = "1"
	acc.Country = "JP"
	acc.GuestCode = code
	acc.GuestCodeHash = store.CodeHash(code)

	owned := 40
	if isHeavy(i) {
		owned = heavyOwned
	}
	acc.InventoryCodes = make([]string, owned)
	for j := range acc.InventoryCodes {
		acc.InventoryCodes[j] = ownedCode(j)
	}
	acc.ItemCodes = append([]string(nil), acc.InventoryCodes[:len(ownedCats)]...) // worn: one per category
	acc.Gems, acc.Welcomed = 5000, true

	// Heavy accounts also own 2,600 room items: with the 3,000 codes the row comes to about the 150 KB the load target
	// names (1001's real account is about 300 KB); the ordinary ones are a few KB.
	roomItems, placed := 25, 20
	if isHeavy(i) {
		roomItems, placed = 2600, 200
	}
	acc.RoomItems = make([]store.RoomItem, roomItems)
	for j := range acc.RoomItems {
		acc.RoomItems[j] = store.RoomItem{Seq: store.FirstRoomSeq + int64(j), Cd: fmt.Sprintf("RUWA%05d", j+1)}
	}
	acc.NextRoomSeq = store.FirstRoomSeq + int64(roomItems)
	layout := store.RoomLayout{
		Floor: store.RoomItem{Seq: store.FirstRoomSeq, Cd: "RUTI00001"},
		Wall:  store.RoomItem{Seq: store.FirstRoomSeq + 1, Cd: "RUWA00001"},
	}
	for j := 0; j < placed; j++ {
		layout.Placed = append(layout.Placed, store.RoomPlaced{Seq: store.FirstRoomSeq + int64(j), Cd: acc.RoomItems[j].Cd, X: j % 8, Y: j / 8, Dir: "S"})
	}
	acc.Rooms = map[string]store.RoomLayout{"LEVEL_1": layout}
	// Mail times are Unix milliseconds (economy mailbox.go compares them with UnixMilli; a row past its time is not listed).
	expires := now.AddDate(0, 1, 0).UnixMilli()
	acc.Mail = []store.MailRow{
		{Seq: 1, Type: "gems", Amount: 50, Message: "synthetic load-test gift", Sender: "cherry-load", Created: now.UnixMilli(), Expires: expires},
		{Seq: 2, Type: "item", Item: "CUAE00001", Message: "synthetic load-test item", Sender: "cherry-load", Created: now.UnixMilli(), Expires: expires},
	}
	acc.NextMailSeq = 3
	if err := store.SaveLocked(nil, acc); err != nil {
		return "", err
	}
	return acc.Aid, nil
}

// seedSocial writes an accepted friendship between accounts 2k and 2k+1, and one diary post for every fourth account.
// Each new row is put where the store keeps it (a friendship at the end of its list, a post at the front) and saved on
// its own, as the handlers save: the store checks that a new row sits at an end of its list at the save.
func seedSocial(aids, names []string, now time.Time) (posts, friends int, err error) {
	store.SocialMu.Lock()
	defer store.SocialMu.Unlock()
	for i := 0; i+1 < len(aids); i += 2 {
		store.Friendships = append(store.Friendships, store.Friendship{A: aids[i], B: aids[i+1], State: "accepted"})
		if err := store.SaveSocialLocked(store.SocialChange{Friends: [][2]string{{aids[i], aids[i+1]}}, Scalars: true}); err != nil {
			return posts, friends, err
		}
		friends++
	}
	for i := 0; i < len(aids); i += 4 {
		store.NextDiaryNo++
		no := strconv.FormatUint(store.NextDiaryNo, 10)
		post := store.DiaryPost{
			DiaryNo: no, AvatarID: aids[i], Nickname: names[i], RegDate: strconv.FormatInt(now.UnixMilli(), 10),
			Title: "synthetic", OpenState: "public", Content: "synthetic load-test diary post",
			ImageLocationType: store.DiaryImageLocationType(nil), Images: []store.DiaryImage{},
		}
		store.DiaryPosts = append([]store.DiaryPost{post}, store.DiaryPosts...) // newest first
		if err := store.SaveSocialLocked(store.SocialChange{Posts: []string{no}, Scalars: true}); err != nil {
			return posts, friends, err
		}
		posts++
	}
	return posts, friends, nil
}

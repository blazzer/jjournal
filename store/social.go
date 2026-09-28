package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"journal/lj"
)

// Group is a friend group owned by a user.
type Group struct {
	ID     int64
	UserID int64
	Name   string
	Bit    int
	Origin string
}

// Mask is 1<<(bit-1) for a 1-based bit index.
func Mask(bit int) uint32 {
	if bit < 1 || bit > 30 {
		return 0
	}
	return 1 << (bit - 1)
}

// LJFriendRow is a synced LiveJournal friend.
type LJFriendRow struct {
	Username  string
	GroupMask uint32
}

// NativeFriendRow is a friend who has an account here.
type NativeFriendRow struct {
	UserID    int64
	Username  string
	GroupMask uint32
}

// ReplaceLJFriends replaces the synced friend list.
func (s *Store) ReplaceLJFriends(ctx context.Context, userID int64, friends []lj.LJFriend, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var accountID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE user_id=? AND service='livejournal'`, userID).Scan(&accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM remote_friends WHERE account_id=?`, accountID); err != nil {
		return err
	}
	for _, f := range friends {
		name, err := lj.NormalizeUsername(f.Username)
		if err != nil {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO remote_friends(account_id, username, groupmask, synced_at) VALUES (?, ?, ?, ?)`,
			accountID, name, f.GroupMask, FormatTime(at)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListLJFriends returns synced friends.
func (s *Store) ListLJFriends(ctx context.Context, userID int64) ([]LJFriendRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.username, f.groupmask FROM remote_friends f
		JOIN accounts a ON a.id = f.account_id
		WHERE a.user_id=? AND a.service='livejournal' ORDER BY f.username`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LJFriendRow
	for rows.Next() {
		var f LJFriendRow
		if err := rows.Scan(&f.Username, &f.GroupMask); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// AddNativeFriend adds a local friend. Self-friend is rejected.
func (s *Store) AddNativeFriend(ctx context.Context, userID, friendID int64, mask uint32) error {
	if userID == friendID {
		return fmt.Errorf("store: cannot friend yourself")
	}
	if mask == 0 {
		mask = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO native_friends(user_id, friend_user_id, groupmask) VALUES (?, ?, ?)
		ON CONFLICT(user_id, friend_user_id) DO UPDATE SET groupmask=excluded.groupmask`, userID, friendID, mask)
	return err
}

// RemoveNativeFriend removes a native friend. LiveJournal friends are untouched.
func (s *Store) RemoveNativeFriend(ctx context.Context, userID, friendID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM native_friends WHERE user_id=? AND friend_user_id=?`, userID, friendID)
	return err
}

// ListNativeFriends returns native friends.
func (s *Store) ListNativeFriends(ctx context.Context, userID int64) ([]NativeFriendRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT n.friend_user_id, u.handle, n.groupmask
		FROM native_friends n JOIN users u ON u.id = n.friend_user_id
		WHERE n.user_id=? ORDER BY u.handle`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NativeFriendRow
	for rows.Next() {
		var f NativeFriendRow
		if err := rows.Scan(&f.UserID, &f.Username, &f.GroupMask); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListGroups returns friend groups.
func (s *Store) ListGroups(ctx context.Context, userID int64) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, name, bit, origin FROM friend_groups WHERE user_id=? ORDER BY bit`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.UserID, &g.Name, &g.Bit, &g.Origin); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GroupMask returns the bitmask for a group name, or ErrUnknownGroup.
func (s *Store) GroupMask(ctx context.Context, userID int64, name string) (uint32, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, nil
	}
	var bit int
	err := s.db.QueryRowContext(ctx, `SELECT bit FROM friend_groups WHERE user_id=? AND lower(name)=lower(?)`, userID, name).Scan(&bit)
	if errorsIsNoRows(err) {
		return 0, ErrUnknownGroup
	}
	if err != nil {
		return 0, err
	}
	return Mask(bit), nil
}

// ErrUnknownGroup means the filter name is not one of the viewer's groups.
var ErrUnknownGroup = fmt.Errorf("store: unknown group")

func errorsIsNoRows(err error) bool { return err == sql.ErrNoRows }

// CreateNativeGroup allocates the lowest free bit.
func (s *Store) CreateNativeGroup(ctx context.Context, userID int64, name string) (Group, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 40 {
		return Group{}, fmt.Errorf("store: invalid group name")
	}
	groups, err := s.ListGroups(ctx, userID)
	if err != nil {
		return Group{}, err
	}
	used := map[int]bool{}
	for _, g := range groups {
		used[g.Bit] = true
		if strings.EqualFold(g.Name, name) {
			return Group{}, fmt.Errorf("store: group already exists")
		}
	}
	bit := 0
	for i := 1; i <= 30; i++ {
		if !used[i] {
			bit = i
			break
		}
	}
	if bit == 0 {
		return Group{}, fmt.Errorf("store: group limit reached")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO friend_groups(user_id, name, bit, origin) VALUES (?, ?, ?, 'native')`, userID, name, bit)
	if err != nil {
		return Group{}, err
	}
	id, _ := res.LastInsertId()
	return Group{ID: id, UserID: userID, Name: name, Bit: bit, Origin: "native"}, nil
}

// DeleteNativeGroup removes a native group and clears its bit from native friends.
func (s *Store) DeleteNativeGroup(ctx context.Context, userID, groupID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var bit int
	var origin string
	err = tx.QueryRowContext(ctx, `SELECT bit, origin FROM friend_groups WHERE id=? AND user_id=?`, groupID, userID).Scan(&bit, &origin)
	if err != nil {
		return err
	}
	if origin != "native" {
		return fmt.Errorf("store: livejournal groups are read-only")
	}
	mask := Mask(bit)
	if _, err := tx.ExecContext(ctx, `UPDATE native_friends SET groupmask = groupmask & ~? WHERE user_id=?`, mask, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM friend_groups WHERE id=?`, groupID); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceLJGroups installs LiveJournal groups, moving native groups off claimed bits.
func (s *Store) ReplaceLJGroups(ctx context.Context, userID int64, groups []lj.LJGroup) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	existing, err := loadGroups(ctx, tx, userID)
	if err != nil {
		return err
	}
	desired := map[int]lj.LJGroup{}
	for _, g := range groups {
		if g.ID < 1 || g.ID > 30 {
			continue
		}
		name := strings.TrimSpace(g.Name)
		if name == "" {
			name = fmt.Sprintf("Group %d", g.ID)
		}
		if len(name) > 80 {
			name = name[:80]
		}
		g.Name = name
		desired[g.ID] = g
	}
	byBit := map[int]Group{}
	for _, g := range existing {
		byBit[g.Bit] = g
	}
	for bit := range desired {
		cur, ok := byBit[bit]
		if !ok {
			continue
		}
		if cur.Origin != "native" {
			continue
		}
		free := freeBit(byBit, desired)
		if free == 0 {
			return fmt.Errorf("store: no free bit for native group %s", cur.Name)
		}
		if err := moveNativeBit(ctx, tx, userID, cur, free); err != nil {
			return err
		}
		delete(byBit, bit)
		cur.Bit = free
		byBit[free] = cur
	}
	for bit, g := range desired {
		name := uniqueName(byBit, bit, g.Name)
		if cur, ok := byBit[bit]; ok {
			if _, err := tx.ExecContext(ctx, `UPDATE friend_groups SET name=?, origin='lj' WHERE id=?`, name, cur.ID); err != nil {
				return err
			}
			cur.Name = name
			cur.Origin = "lj"
			byBit[bit] = cur
			continue
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO friend_groups(user_id, name, bit, origin) VALUES (?, ?, ?, 'lj')`, userID, name, bit)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		byBit[bit] = Group{ID: id, UserID: userID, Name: name, Bit: bit, Origin: "lj"}
	}
	for bit, g := range byBit {
		if g.Origin == "lj" {
			if _, ok := desired[bit]; !ok {
				if _, err := tx.ExecContext(ctx, `DELETE FROM friend_groups WHERE id=?`, g.ID); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

func loadGroups(ctx context.Context, tx *sql.Tx, userID int64) ([]Group, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, user_id, name, bit, origin FROM friend_groups WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.UserID, &g.Name, &g.Bit, &g.Origin); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func freeBit(byBit map[int]Group, desired map[int]lj.LJGroup) int {
	for i := 1; i <= 30; i++ {
		if _, used := byBit[i]; used {
			continue
		}
		if _, want := desired[i]; want {
			continue
		}
		return i
	}
	return 0
}

func moveNativeBit(ctx context.Context, tx *sql.Tx, userID int64, g Group, newBit int) error {
	oldMask := Mask(g.Bit)
	newMask := Mask(newBit)
	if _, err := tx.ExecContext(ctx, `UPDATE friend_groups SET bit=? WHERE id=?`, newBit, g.ID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE native_friends SET groupmask = (groupmask & ~?) | ? WHERE user_id=? AND (groupmask & ?) != 0`,
		oldMask, newMask, userID, oldMask)
	return err
}

func uniqueName(byBit map[int]Group, keepBit int, name string) string {
	taken := func(n string) bool {
		for bit, g := range byBit {
			if bit == keepBit {
				continue
			}
			if strings.EqualFold(g.Name, n) {
				return true
			}
		}
		return false
	}
	if !taken(name) {
		return name
	}
	alt := name + " (LJ)"
	if !taken(alt) {
		return alt
	}
	return fmt.Sprintf("%s %d", name, keepBit)
}

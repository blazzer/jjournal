package app

import (
	"context"
	"strconv"

	"journal/lj"
	"journal/store"
)

// ListAction mutates the signed-in member's native list.
type ListAction struct {
	Action   string
	Username string
	Name     string
	FriendID int64
	GroupID  int64
	Groups   []int64
}

// ApplyList adds or removes a native friend or group.
func ApplyList(ctx context.Context, st *store.Store, v Viewer, in ListAction) error {
	switch in.Action {
	case "add_friend":
		name, err := lj.NormalizeUsername(in.Username)
		if err != nil {
			return Invalid{Field: "username", Msg: "Enter a valid username."}
		}
		friend, err := st.UserByUsername(ctx, name)
		if err != nil {
			return Invalid{Field: "username", Msg: "That person does not have an account here yet."}
		}
		groups, err := st.ListGroups(ctx, v.ID)
		if err != nil {
			return err
		}
		var mask uint32
		for _, id := range in.Groups {
			for _, g := range groups {
				if g.ID == id && g.Origin == "native" {
					mask |= store.Mask(g.Bit)
				}
			}
		}
		if mask == 0 {
			mask = 1
		}
		if err := st.AddNativeFriend(ctx, v.ID, friend.ID, mask); err != nil {
			return Invalid{Field: "username", Msg: "Could not add that friend."}
		}
	case "remove_friend":
		if err := st.RemoveNativeFriend(ctx, v.ID, in.FriendID); err != nil {
			return Invalid{Field: "friend_id", Msg: "Could not remove that friend."}
		}
	case "add_group":
		if _, err := st.CreateNativeGroup(ctx, v.ID, in.Name); err != nil {
			return Invalid{Field: "name", Msg: "Could not add that group."}
		}
	case "remove_group":
		if err := st.DeleteNativeGroup(ctx, v.ID, in.GroupID); err != nil {
			return Invalid{Field: "group_id", Msg: "LiveJournal groups are read-only."}
		}
	default:
		return Invalid{Field: "action", Msg: "Unknown action."}
	}
	return nil
}

// ParseIDs reads integer form values.
func ParseIDs(raw []string) []int64 {
	out := make([]int64, 0, len(raw))
	for _, s := range raw {
		n, _ := strconv.ParseInt(s, 10, 64)
		out = append(out, n)
	}
	return out
}

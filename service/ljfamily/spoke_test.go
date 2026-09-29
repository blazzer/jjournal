package ljfamily

import (
	"testing"

	"journal/lj"
	"journal/service"
)

func TestAdaptFriendsPage(t *testing.T) {
	fake := &lj.Fake{Entries: map[string][]lj.LJEntry{
		"ada": {{ItemID: 7, Journal: "bob", Author: "bob", EventHTML: "<p>hi</p>", Security: "public"}},
	}}
	svc := Adapt("livejournal", "LiveJournal", "livejournal.com", fake)
	sess := service.Session{Username: "ada"}
	page, err := svc.FriendsPage(t.Context(), sess, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !page.End || len(page.Entries) != 1 || page.Entries[0].RemoteID != "7" {
		t.Fatalf("%+v", page)
	}
	page, err = svc.FriendsPage(t.Context(), sess, 50)
	if err != nil || !page.End || len(page.Entries) != 0 {
		t.Fatalf("%+v %v", page, err)
	}
}

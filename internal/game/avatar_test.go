package game

import (
	"testing"

	"github.com/go-wyvern/undercover/internal/testsupport"
)

func avatarAt(v View, seat int) string {
	for _, p := range v.Players {
		if p.Seat == seat {
			return p.Avatar
		}
	}
	return ""
}

func TestAccountAvatarsPersistAndUpdateExistingSeats(t *testing.T) {
	store := testsupport.NewRooms(true)
	m, err := NewManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	ownerAvatar := "https://account.example/owner.png"
	guestAvatar := "https://account.example/friend.png"
	created, err := m.Create("owner", "房主", "头像测试局", "jade", 4, ownerAvatar)
	if err != nil {
		t.Fatal(err)
	}
	if avatarAt(created, 1) != ownerAvatar {
		t.Fatal("creator account avatar missing")
	}
	joined, err := m.Join(created.ID, "friend", "同桌", guestAvatar)
	if err != nil {
		t.Fatal(err)
	}
	if avatarAt(joined, 1) != ownerAvatar || avatarAt(joined, 2) != guestAvatar {
		t.Fatal("room members cannot see each other's avatar")
	}
	for _, room := range m.List() {
		if room.ID == created.ID && room.Players[1].Avatar != guestAvatar {
			t.Fatal("lobby avatar missing")
		}
	}
	before, err := m.Start(created.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	updated := "https://account.example/owner-new.png"
	if err = m.SyncAvatar("owner", updated); err != nil {
		t.Fatal(err)
	}
	after, _ := m.View(created.ID, "owner")
	if after.GameID != before.GameID || after.MyWord != before.MyWord || after.Phase != before.Phase || after.CurrentPlayerID != before.CurrentPlayerID {
		t.Fatal("profile sync changed game progress")
	}
	other, _ := m.View(created.ID, "friend")
	if avatarAt(other, 1) != updated {
		t.Fatal("updated avatar did not reach other members")
	}
	if m.rooms[created.ID].Players[0].Avatar != updated {
		t.Fatal("next-round roster still uses old avatar")
	}
	version := after.Version
	if err = m.SyncAvatar("owner", updated); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := m.View(created.ID, "owner")
	if unchanged.Version != version {
		t.Fatal("unchanged avatar caused unnecessary room events")
	}
	restored, err := NewManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	view, _ := restored.View(created.ID, "friend")
	if avatarAt(view, 1) != updated || avatarAt(view, 2) != guestAvatar {
		t.Fatal("account avatars lost on restart")
	}
	if err = restored.Leave(created.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	view, _ = restored.View(created.ID, "friend")
	for _, p := range view.Players {
		if p.Seat == 1 && (!p.Bot || p.Avatar != "") {
			t.Fatal("AI replacement retained the former human's avatar")
		}
	}
}

func TestLegacySeatGetsAvatarWithoutRejoining(t *testing.T) {
	m, _ := NewManager(testsupport.NewRooms(true), nil)
	before, err := m.Join("room-1", "owner", "房主", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.SyncAvatar("owner", "https://account.example/avatar.png"); err != nil {
		t.Fatal(err)
	}
	after, _ := m.View("room-1", "owner")
	if before.MyPlayerID != after.MyPlayerID || avatarAt(after, 1) == "" {
		t.Fatal("existing seat not upgraded in place")
	}
	joined, err := m.Join("room-1", "owner", "房主", "https://account.example/avatar-new.png")
	if err != nil {
		t.Fatal(err)
	}
	if joined.MyPlayerID != before.MyPlayerID || avatarAt(joined, 1) != "https://account.example/avatar-new.png" {
		t.Fatal("repeated join did not refresh the same seat")
	}
}

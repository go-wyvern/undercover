package game

import (
	"errors"
	"testing"

	"github.com/go-wyvern/undercover/internal/testsupport"
)

func TestCloseRoomAuthorizationDurabilityAndFailure(t *testing.T) {
	store := testsupport.NewRooms(false)
	m, err := NewManager(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := m.Create("owner", "房主", "关闭测试", "jade", 4, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Join(created.ID, "friend", "同桌", ""); err != nil {
		t.Fatal(err)
	}
	if err = m.CloseRoom(created.ID, "friend"); !errors.Is(err, ErrNotOwner) {
		t.Fatal("non-host deleted a room")
	}
	if _, err = m.Start(created.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	release, err := m.AcquireAgent(created.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	updates, unsubscribe := m.Subscribe(created.ID)
	defer unsubscribe()
	done := m.Done(created.ID)
	store.Fail = errors.New("database unavailable")
	if err = m.CloseRoom(created.ID, "owner"); err == nil {
		t.Fatal("reported success after failed DELETE")
	}
	if _, err = m.View(created.ID, "friend"); err != nil {
		t.Fatal("failed DELETE removed the live room")
	}
	select {
	case <-done:
		t.Fatal("failed DELETE cancelled the game")
	default:
	}
	select {
	case <-updates:
		t.Fatal("failed DELETE sent closure")
	default:
	}
	store.Fail = nil
	if err = m.CloseRoom(created.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	default:
		t.Fatal("room closure did not cancel voice")
	}
	select {
	case <-updates:
	default:
		t.Fatal("members were not notified")
	}
	release() // An in-flight host conversation cannot recreate the deleted row.
	if m.CurrentRoom("owner") != "" || m.CurrentRoom("friend") != "" || len(m.List()) != 0 {
		t.Fatal("closed room remains visible")
	}
	if _, err = m.View(created.ID, "friend"); !errors.Is(err, ErrRoomNotFound) {
		t.Fatal("closed room is still readable")
	}
	restarted, err := NewManager(store, nil)
	if err != nil || len(restarted.List()) != 0 {
		t.Fatal("deleted room or default rooms reappeared")
	}
}
func TestRoomWriteFailureRollsBackState(t *testing.T) {
	store := testsupport.NewRooms(false)
	m, _ := NewManager(store, nil)
	created, err := m.Create("owner", "房主", "写入测试", "jade", 4, "")
	if err != nil {
		t.Fatal(err)
	}
	store.Fail = errors.New("database unavailable")
	if _, err = m.Join(created.ID, "friend", "同桌", ""); err == nil {
		t.Fatal("join succeeded without persistence")
	}
	after, _ := m.View(created.ID, "owner")
	if len(after.Players) != 1 || after.Version != created.Version {
		t.Fatal("failed write changed room state")
	}
}

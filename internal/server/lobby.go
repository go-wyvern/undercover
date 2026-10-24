package server

import (
	"errors"
	"net/http"

	"github.com/go-wyvern/undercover/internal/auth"
)

func (a *App) lobbyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/rooms/{id}/ready", a.withUser(func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		var in struct {
			Ready *bool `json:"ready"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Ready == nil {
			fail(w, 400, errors.New("请指定准备状态"))
			return
		}
		v, err := a.rooms.SetReady(r.PathValue("id"), u.ID, *in.Ready)
		respond(w, v, err)
	}))
	mux.HandleFunc("POST /api/rooms/{id}/settings", a.withUser(func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		var in struct {
			AutoFillAI *bool `json:"autoFillAI"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.AutoFillAI == nil {
			fail(w, 400, errors.New("请指定 AI 自动补位设置"))
			return
		}
		v, err := a.rooms.SetAutoFillAI(r.PathValue("id"), u.ID, *in.AutoFillAI)
		respond(w, v, err)
	}))
	mux.HandleFunc("POST /api/rooms/{id}/ai-seat", a.withUser(func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		var in struct {
			Seat int   `json:"seat"`
			Bot  *bool `json:"bot"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Bot == nil {
			fail(w, 400, errors.New("请指定是否安排 AI 入座"))
			return
		}
		v, err := a.rooms.SetAISeat(r.PathValue("id"), u.ID, in.Seat, *in.Bot)
		respond(w, v, err)
	}))
	mux.HandleFunc("POST /api/rooms/{id}/members/{player}/remove", a.withUser(func(w http.ResponseWriter, r *http.Request, u *auth.User) {
		v, err := a.rooms.RemoveMember(r.PathValue("id"), u.ID, r.PathValue("player"))
		respond(w, v, err)
	}))
}

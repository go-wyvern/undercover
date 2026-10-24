package server

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/go-wyvern/undercover/internal/game"
)

var seatPrefix = regexp.MustCompile(`(?:^|[。！？；\n])\s*([一二三四五六1-6])号`)

func normalizedSpeech(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return r
	}, text)
}
func (a *App) sentenceCue(id, uid, text string, offset int64) game.AudioCue {
	cue := game.AudioCue{OffsetMS: offset, Text: text}
	v, err := a.rooms.View(id, uid)
	if err != nil {
		return cue
	}
	explicitSeat := 0
	if match := seatPrefix.FindStringSubmatch(text); len(match) > 0 && !strings.Contains(text, "投") {
		for i, r := range []rune("一二三四五六") {
			if string(r) == match[1] {
				explicitSeat = i + 1
			}
		}
		if match[1][0] >= '1' && match[1][0] <= '6' {
			explicitSeat = int(match[1][0] - '0')
		}
	}
	normalized := normalizedSpeech(text)
	for i := len(v.Logs) - 1; i >= 0; i-- {
		l := v.Logs[i]
		if l.Kind == "speech" && l.Round == v.Round && (explicitSeat == 0 || explicitSeat == l.Seat) && len([]rune(l.Text)) >= 4 && strings.Contains(normalized, normalizedSpeech(l.Text)) {
			cue.LogID = l.ID
			cue.PlayerID = l.PlayerID
			cue.Seat = l.Seat
			cue.Text = l.Text
			return cue
		}
	}
	// A short seat-labelled introduction can precede the description's first PCM.
	if explicitSeat > 0 && strings.ContainsAny(text, ":：") && !strings.ContainsAny(text, "请轮投") {
		for i := len(v.Logs) - 1; i >= 0; i-- {
			l := v.Logs[i]
			if l.Kind == "speech" && l.Seat == explicitSeat && l.Round == v.Round {
				cue.LogID = l.ID
				cue.PlayerID = l.PlayerID
				cue.Seat = l.Seat
				cue.Text = l.Text
				return cue
			}
		}
	}
	return cue
}

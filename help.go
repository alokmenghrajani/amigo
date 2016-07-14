package main

import (
	"log"

	"golang.org/x/net/websocket"
)

// Prints help message
func doHelp(config Config, ws *websocket.Conn, user string, channel string) {
	var m Message
	m.Type = "message"
	m.Channel = channel

	m.Text = `start _team name_: sets your team's name and PMs you a link to a puzzle. This starts your clock.
validate _level_ _flag_: tells you if a flag for a level is correct (message or invite me to a private channel first!).
scores: tells you the current top scores (beta)`
	log.Printf("posting: %v", m)
	postMessage(ws, m)
}

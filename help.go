package main

import (
  "golang.org/x/net/websocket"
  "log"
)

// Prints help message
func doHelp(config Config, ws *websocket.Conn, user string, channel string) {
  var m Message
	m.Type = "message"
	m.Channel = channel

  m.Text = `start _team name_: sets your team's name and PMs you a link to a puzzle. This starts your clock.
validate _flag_: tells you if a flag is correct (message or invite me to a private channel first!).`
  log.Printf("posting: %v", m)
	postMessage(ws, m)
}

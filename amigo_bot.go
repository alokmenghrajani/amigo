/**
 * Slack bot for a CTF.
 *
 * Functionality:
 * - hands out URL to a puzzle.
 * - validates flags.
 * - logs events to a database.
 * - posts messages to a public channel.
 */

package main

import (
	_ "github.com/go-sql-driver/mysql"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"golang.org/x/net/websocket"
	"sync"
	"github.com/nlopes/slack"
)

type user struct {
	username string
	privateChannel string
}

var userCache map[string]user
var userCacheLock sync.Mutex

func resolveUser(config Config, userToken string) (user, error) {
	userCacheLock.Lock()
	defer userCacheLock.Unlock()

	u, ok := userCache[userToken]
	if ok {
		return u, nil
	}

	log.Printf("resolving user: %s", userToken)
	api := slack.New(config.SlackApiToken)
	userInfo, err := api.GetUserInfo(userToken)
  if err != nil {
		log.Printf("api.GetUserInfo: %s", err)
    return user{}, err
  }
	_, _, imChannel, err := api.OpenIMChannel(userToken)
	if err != nil {
		log.Printf("api.OpenIMChannel: %s", err)
		return user{}, err
	}
	newUser := user{username: userInfo.Name, privateChannel: imChannel}
	userCache[userToken] = newUser
	return newUser, nil
}

func isPrivate(channel string) bool {
	return strings.HasPrefix(channel, "D")
}

func postError(ws *websocket.Conn, channel string, message string, userToken string) {
	var m Message
	m.Type = "message"
	m.Channel = channel
	if isPrivate(channel) {
		m.Text = message
	} else {
		m.Text = fmt.Sprintf("<@%s>: %s", userToken, message)
	}
	log.Printf("error: %s", message)
	postMessage(ws, m)
}

func main() {
	userCache = make(map[string]user)
	userCacheLock = sync.Mutex{}

	config := configRead()
	fmt.Print("[OK] Config\n")

	// Connect to database
	db, err := sql.Open("mysql", config.MysqlConn)
	if err != nil {
		log.Panicf("Failed to connect to database: %s", err)
	}
	fmt.Print("[OK] Database\n")

	// Connect to Slack using Websocket Real Time API
	ws, bot_id := slackConnect(config.SlackApiToken)
	fmt.Print("[OK] Slack\n")

	for {
		// read each incoming message
		m, err := getMessage(ws)
		if err != nil {
			log.Printf("getMessage failed: %s", err)
			continue
		}

		if m.Type == "message" {
			if m.Subtype == "" {
				if strings.HasPrefix(m.Text, fmt.Sprintf("<@%s>", bot_id)) {
					parts := strings.Fields(m.Text)
					if len(parts) >= 2 && parts[1] == "help" {
						go func(m Message) {
						  doHelp(config, ws, m.User, m.Channel)
						}(m)
					} else if len(parts) >= 3 && parts[1] == "start" {
						go func(m Message) {
							doStart(config, db, ws, m.User, m.Channel, strings.Join(parts[2:], " "))
						}(m)
					} else if len(parts) >= 3 && parts[1] == "validate" {
						go func(m Message) {
							doValidate(config, db, ws, m.User, m.Channel, strings.Join(parts[2:], " "))
						}(m)
					} else {
						go func(m Message) {
							postError(ws, m.Channel, "sorry, I didn't understand that.", m.User)
						}(m)
					}
				} else if strings.HasPrefix(m.Channel, "D") && m.User != bot_id {
					parts := strings.Fields(m.Text)
					if len(parts) >= 1 && parts[0] == "help" {
						go func(m Message) {
						  doHelp(config, ws, m.User, m.Channel)
						}(m)
					} else if len(parts) >= 2 && parts[0] == "start" {
						go func(m Message) {
							doStart(config, db, ws, m.User, m.Channel, strings.Join(parts[1:], " "))
						}(m)
					} else if len(parts) >= 2 && parts[0] == "validate" {
						go func(m Message) {
							doValidate(config, db, ws, m.User, m.Channel, strings.Join(parts[1:], " "))
						}(m)
					} else {
						go func(m Message) {
							postError(ws, m.Channel, "sorry, I didn't understand that.", m.User)
						}(m)
					}
				}
			}
		}
	}
}

func doStart(config Config, db *sql.DB, ws *websocket.Conn, userToken string, channel string, teamName string) {
	// Map userToken to user
	u, err := resolveUser(config, userToken)
	if err != nil {
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
		return
	}

	// Check user exists in users table
	log.Printf("doStart: %s as %s", u.username, teamName)
	var team int
	err = db.QueryRow("SELECT team FROM users WHERE user=?", u.username).Scan(&team)
	switch {
	case err == sql.ErrNoRows:
		postError(ws, channel, "sorry, I don't know which team you are on.", userToken)
		return
	case err != nil:
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
		return
	default:
	}

	// Update the team name
	_, err = db.Exec("INSERT INTO teams SET id=?, name=? ON DUPLICATE KEY UPDATE name=?",
		team, teamName, teamName)
	if err != nil {
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
		return
	}

	// Record log event
	_, err = db.Exec("INSERT INTO logs SET user=?, event='start'", u.username)
	if err != nil {
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
		return
	}

	// Post to public channel

	// Return link
	var m Message
	m.Type = "message"
	m.Text = "link to puzzle: "
	if isPrivate(channel) {
		m.Channel = channel
	} else {
		m.Channel = u.privateChannel
	}
	postMessage(ws, m)
	log.Printf("doStart: done (%s)", u.username)
}

func doValidate(config Config, db *sql.DB, ws *websocket.Conn, userToken string, channel string, flag string) {
	// Map userToken to user
	u, err := resolveUser(config, userToken)
	if err != nil {
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
		return
	}

	// Check user exists in users table
	log.Printf("doValidate: %s for %s", u.username, flag)
	var team int
	err = db.QueryRow("SELECT team FROM users WHERE user=?", u.username).Scan(&team)
	switch {
	case err == sql.ErrNoRows:
		postError(ws, channel, "sorry, I don't know which team you are on.", userToken)
		return
	case err != nil:
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
		return
	default:
	}

	event := "incorrect flag"
	if flag == config.Flag1 {
		event = "got flag 1"
	} else if flag == config.Flag2 {
		event = "got flag 2"
	}

	// Record log event
	_, err = db.Exec("INSERT INTO logs SET user=?, event=?", u.username, event)
	if err != nil {
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
		return
	}

	// Post to public channel
	// TODO

	// Return link
	var m Message
	m.Type = "message"
	m.Text = event
	if isPrivate(channel) {
		m.Channel = channel
	} else {
		m.Channel = u.privateChannel
	}
	postMessage(ws, m)
	log.Printf("doValidate: done (%s)", u.username)
}

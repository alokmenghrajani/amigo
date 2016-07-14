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
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	_ "github.com/go-sql-driver/mysql"
	"github.com/nlopes/slack"
	"golang.org/x/net/websocket"
)

type user struct {
	username       string
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

func resolveChannel(config Config) string {
	log.Printf("resolving channel: %s", config.PublicChannel)
	api := slack.New(config.SlackApiToken)
	groups, err := api.GetGroups(true)
	if err != nil {
		log.Printf("api.GetGroups: %s", err)
	} else {
		for _, group := range groups {
			if group.Name == config.PublicChannel {
				return group.ID
			}
		}
	}

	channels, err := api.GetChannels(true)
	if err != nil {
		log.Printf("api.GetChannels: %s", err)
	} else {
		for _, channel := range channels {
			if channel.Name == config.PublicChannel {
				return channel.ID
			}
		}
	}

	return ""
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

var publicChannel string

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

	publicChannel = resolveChannel(config)

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
					} else if len(parts) >= 4 && parts[1] == "validate" {
						go func(m Message) {
							doValidate(config, db, ws, m.User, m.Channel, parts[2], strings.Join(parts[3:], " "))
						}(m)
					} else if len(parts) >= 2 && parts[1] == "scores" {
						go func(m Message) {
							doTopScores(config, db, ws, m.User, m.Channel)
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
					} else if len(parts) >= 3 && parts[0] == "validate" {
						go func(m Message) {
							doValidate(config, db, ws, m.User, m.Channel, parts[1], strings.Join(parts[2:], " "))
						}(m)
					} else if len(parts) >= 1 && parts[0] == "scores" {
						go func(m Message) {
							doTopScores(config, db, ws, m.User, m.Channel)
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

	var aUser string
	err = db.QueryRow("SELECT user FROM logs WHERE team_id=?", team).Scan(&aUser)
	switch {
	case err != nil && err != sql.ErrNoRows:
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
		return
	case err == nil:
		postError(ws, channel, fmt.Sprintf("sorry, %s of your team already started the ctf!", aUser), userToken)
		return
	default:
	}

	// Update the team name, can only happen once.
	_, err = db.Exec("INSERT INTO teams SET id=?, name=?", team, teamName)
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
	var m Message
	m.Type = "message"
	m.Channel = publicChannel
	m.Text = fmt.Sprintf("Team %s has entered the competition!", teamName)
	postMessage(ws, m)

	// Return link
	m.Type = "message"
	m.Text = fmt.Sprintf("Here is a link to the puzzle: %s", config.PuzzleLink)
	if isPrivate(channel) {
		m.Channel = channel
	} else {
		m.Channel = u.privateChannel
	}
	postMessage(ws, m)
	log.Printf("doStart: done (%s)", u.username)
}

func doValidate(config Config, db *sql.DB, ws *websocket.Conn, userToken string, channel string, sLevel string, flag string) {
	// Map userToken to user
	u, err := resolveUser(config, userToken)
	if err != nil {
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
		return
	}

	// Check user exists in users table
	log.Printf("doValidate: %s solving puzzle %s: %s", u.username, flag)
	var team string
	var teamId int
	err = db.QueryRow("SELECT teams.name,teams.id FROM teams JOIN users ON teams.id = users.team WHERE users.user=?", u.username).Scan(&team, &teamId)
	switch {
	case err == sql.ErrNoRows:
		postError(ws, channel, "sorry, I don't know which team you are on.", userToken)
		return
	case err != nil:
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
		return
	default:
	}

	// Disallow validation on public channel
	if channel == publicChannel {
		postError(ws, channel, fmt.Sprintf("shush!"), userToken)
		return
	}

	level, err = strconv.Atoi(sLevel)
	switch {
	case err != nil:
		postError(ws, channel, fmt.Sprintf("%s is not a valid puzzle number", sLevel), userToken)
		return
	case level < 1:
		postError(ws, channel, fmt.Sprintf("you give us too much credit for starting puzzle enumeration from 0; humans designed this, not chat bots"), userToken)
		return
	case level > 2:
		postError(ws, channel, fmt.Sprintf("woaaaaah nelly! puzzle 3 hasn't started yet!"), userToken)
		return
	default:
	}

	event := "incorrect:" + flag
	event_ok := false

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM logs WHERE team_id=? AND level=?", teamId, level).Scan(&count)
	if err == nil {
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
	}

	switch {
	case level == 1:
		if flag == config.Flag1 {
			event = "flag 1"
			event_ok = true
		} else if flag == config.Flag2 {
			event = "flag 2"
			event_ok = true
		}
	case level == 2:
		// Make sure they haven't done > 10 tries
		switch {
		case count >= 10:
			postError(ws, channel, fmt.Sprintf("you've exhausted your 10 tries! no points 4 u"), userToken)
			return
		default:
			if flag == config.Flag3 {
				event = "flag 3"
				event_ok = true
			}
		}
	}

	// Record log event
	_, err = db.Exec("INSERT INTO logs SET user=?, event=?", u.username, event)
	if err != nil {
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
		return
	}

	// Post to public channel
	var m Message
	m.Type = "message"
	if event_ok {
		m.Channel = publicChannel
		m.Text = fmt.Sprintf("Team %s found %s!", team, event)
		postMessage(ws, m)
	}

	// Return result
	if event_ok {
		m.Text = fmt.Sprintf("Congrats, you found %s!", event)
	} else {
		m.Text = fmt.Sprintf("Sorry, that's not right.")
		if level == 2 {
			m.Text += fmt.Sprintf(" You have %d tries left.", 10-(count+1))
		}
	}
	m.Channel = channel
	postMessage(ws, m)
	log.Printf("doValidate: done (%s)", u.username)
}

func doTopScores(config Config, db *sql.DB, ws *websocket.Conn, userToken string, channel string) {
	// I'm sorry
	rows, err := db.Query("select distinct teams.id as id, teams.name as team_name, unix_timestamp(t1.ts)-unix_timestamp(t0.ts) as flag1_time, unix_timestamp(t2.ts)-unix_timestamp(t0.ts) as flag2_time, FLOOR(unix_timestamp(t3.ts)/unix_timestamp(t3.ts)) AS flag3_time, (select count(*) from logs as a where a.team_id=teams.id and level=2 and (event like 'incorrect:%' or event like 'flag 3')) AS flag3_tries from logs left join teams on (teams.id=logs.team_id) left join logs as t0 on (t0.team_id=logs.team_id and t0.event='start') left join logs as t1 on (t1.team_id=logs.team_id and t1.event='flag 1') left join logs as t2 on (t2.team_id=logs.team_id and t2.event='flag 2') left join logs as t3 on (t3.team_id=logs.team_id and t3.event='flag 3') order by (flag3_time * -flag3_tries) desc, -flag2_time desc, flag1_time")
	// rows, err := db.Query("select id, name, unix_timestamp(flag1)-unix_timestamp(start) as flag1_time, unix_timestamp(flag2)-unix_timestamp(start) as flag2_time from teams_start_flag1_flag2 where id < 666 and flag1 is not null order by -flag2_time desc, flag1_time")
	if err != nil {
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
		return
	}
	defer rows.Close()

	i := 0
	text := ""
	teamsShown := map[int]bool{}
	for rows.Next() {
		var id int
		var name string
		var flag1Time, flag2Time, flag3Time, flag3Tries sql.NullInt64
		err := rows.Scan(&id, &name, &flag1Time, &flag2Time, &flag3Time, &flag3Tries)
		if err != nil {
			postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
			return
		}
		if _, ok := teamsShown[id]; ok {
			// Skip team if they have already been output (if teams "find" a flag multiple times,
			// they'll end up with multiple entries, but we just want to print the fastest time).
			continue
		}
		text += fmt.Sprintf("#%d : Team %s has found the following flags: ", i, name)

		if flag1Time.Valid {
			text += fmt.Sprintf("flag 1 (%d min) ", flag1Time.Int64/60)
		}
		if flag2Time.Valid {
			text += fmt.Sprintf("flag 2 (%d min) ", flag2Time.Int64/60)
		}
		if flag3Time.Valid {
			text += fmt.Sprintf("flag 3 (%d tries) ", flag3Tries.Int64)
		}
		text += "\n"
		teamsShown[id] = true
		i++
	}

	if text == "" {
		text = "It appears nobody has found any flags yet"
	}

	// Post to public channel
	var m Message
	m.Type = "message"
	m.Text = text
	m.Channel = channel
	postMessage(ws, m)
}

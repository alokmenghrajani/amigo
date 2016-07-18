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
	"sort"
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
	ws, botID := slackConnect(config.SlackApiToken)
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
				if strings.HasPrefix(m.Text, fmt.Sprintf("<@%s>", botID)) {
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
				} else if strings.HasPrefix(m.Channel, "D") && m.User != botID {
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
	log.Printf("doValidate: %s solving puzzle %s: %s", u.username, sLevel, flag)
	var team string
	var teamID int
	err = db.QueryRow("SELECT teams.name,teams.id FROM teams JOIN users ON teams.id = users.team WHERE users.user=?", u.username).Scan(&team, &teamID)
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

	level := -1
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
	eventOk := false

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM logs WHERE team_id=? AND level=?", teamID, level).Scan(&count)
	if err != nil {
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
		return
	}

	switch {
	case level == 1:
		if flag == config.Flag1 {
			event = "flag 1"
			eventOk = true
		} else if flag == config.Flag2 {
			event = "flag 2"
			eventOk = true
		}
	case level == 2:
		// Make sure they haven't done > 10 tries
		switch {
		case count >= 10:
			postError(ws, channel, fmt.Sprintf("you've exhausted your 10 tries! no points 4 u"), userToken)
			return
		default:
			var dupCount int
			err = db.QueryRow("SELECT COUNT(*) FROM logs WHERE team_id=? AND level=? AND event=?", teamID, level, event).Scan(&dupCount)
			if err != nil {
				postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
				return
			}
			if dupCount > 0 {
				postError(ws, channel, fmt.Sprintf("you (or a teammate) already tried that guess"), userToken)
				return
			}
			if flag == config.Flag3 {
				event = "flag 3"
				eventOk = true
			}
		}
	}

	// Record log event
	_, err = db.Exec("INSERT INTO logs SET user=?, event=?, level=?, team_id=?", u.username, event, level, teamID)
	if err != nil {
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
		return
	}

	// Post to public channel
	var m Message
	m.Type = "message"
	if eventOk {
		m.Channel = publicChannel
		m.Text = fmt.Sprintf("Team %s found %s!", team, event)
		postMessage(ws, m)
	}
	if level == 2 && (count+1) == 10 && !eventOk {
		m.Channel = publicChannel
		m.Text = fmt.Sprintf("Team %s ran out of tries! :(", team)
		postMessage(ws, m)
	}

	// Return result
	if eventOk {
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

type teamScores struct {
	teamID                                                               int
	hasFlag1, hasFlag2, hasFlag3, hasFlag4, hasFlag5, hasFlag6, hasFlag7 bool
}

// ScoreList is things
type ScoreList []teamScores

func (s ScoreList) Len() int {
	return len(s)
}

func (s ScoreList) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s teamScores) numFlags() int {
	numFlags := 0
	if s.hasFlag1 {
		numFlags++
	}
	if s.hasFlag2 {
		numFlags++
	}
	if s.hasFlag3 {
		numFlags++
	}
	if s.hasFlag4 {
		numFlags++
	}
	if s.hasFlag5 {
		numFlags++
	}
	if s.hasFlag6 {
		numFlags++
	}
	if s.hasFlag7 {
		numFlags++
	}
	return numFlags
}

func (s ScoreList) Less(i, j int) bool {
	numFlagsLeft := s[i].numFlags()
	numFlagsRight := s[j].numFlags()
	return numFlagsLeft < numFlagsRight
}

func doTopScores(config Config, db *sql.DB, ws *websocket.Conn, userToken string, channel string) {
	// Fetch data
	rows, err := db.Query("select id, event, team_id from logs where team_id < 666")
	if err != nil {
		postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
		return
	}
	defer rows.Close()

	// Extract data from rows
	teams := map[int]bool{}
	eventCounts := map[int]map[string]int{}

	for rows.Next() {
		var id, teamID int
		var event string

		err := rows.Scan(&id, &event, &teamID)
		if err != nil {
			postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
			return
		}

		teams[teamID] = true

		eventCountsForTeam, ok := eventCounts[teamID]
		if !ok {
			eventCounts[teamID] = map[string]int{}
			eventCountsForTeam = eventCounts[teamID]
		}

		switch event {
		case "start", "flag 1", "flag 2", "flag 3", "flag 4", "flag 5", "flag 6", "flag 7":
			prevCount, ok := eventCountsForTeam[event]
			if ok {
				eventCountsForTeam[event] = prevCount + 1
			} else {
				eventCountsForTeam[event] = 1
			}
		}
	}

	// Flag 1: compute start/end time as score
	scores := []teamScores{}
	for team := range teams {
		_, hasFlag1 := eventCounts[team]["flag 1"]
		_, hasFlag2 := eventCounts[team]["flag 2"]
		_, hasFlag3 := eventCounts[team]["flag 3"]
		_, hasFlag4 := eventCounts[team]["flag 4"]
		_, hasFlag5 := eventCounts[team]["flag 5"]
		_, hasFlag6 := eventCounts[team]["flag 6"]
		_, hasFlag7 := eventCounts[team]["flag 7"]

		s := teamScores{}
		s.teamID = team
		s.hasFlag1 = hasFlag1
		s.hasFlag2 = hasFlag2
		s.hasFlag3 = hasFlag3
		s.hasFlag4 = hasFlag4
		s.hasFlag5 = hasFlag5
		s.hasFlag6 = hasFlag6
		s.hasFlag7 = hasFlag7

		scores = append(scores, s)
	}

	sort.Sort(sort.Reverse(ScoreList(scores)))

	i := 0
	text := ""
	for _, team := range scores {
		rows, err := db.Query(fmt.Sprintf("select name from teams where id = %d", team.teamID))
		if err != nil {
			postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
			return
		}
		defer rows.Close()

		var teamName string
		rows.Next()
		err = rows.Scan(&teamName)
		if err != nil {
			postError(ws, channel, fmt.Sprintf("sorry, something went wrong (%s)", err), userToken)
			return
		}

		text += fmt.Sprintf("# %d: Team '%s' found %d flags\n", i, teamName, team.numFlags())
		i++
	}

	// Post to public channel
	var m Message
	m.Type = "message"
	m.Text = text
	m.Channel = channel
	postMessage(ws, m)
}

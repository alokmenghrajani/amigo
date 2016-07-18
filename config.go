package main

import (
	"encoding/json"
	"log"
	"os"
)

type Config struct {
	BotName       string `json:"bot_name"`
	SlackApiToken string `json:"slack_api_token"`
	MysqlConn     string `json:"mysql_conn_string"`
	PuzzleLink    string `json:"puzzle_link"`
	PublicChannel string `json:"public_channel"`
	Flag1         string `json:"flag1"`
	Flag2         string `json:"flag2"`
	Flag3         string `json:"flag3"`
	Flag4         string `json:"flag4"`
	Flag5         string `json:"flag5"`
	Flag6         string `json:"flag6"`
	Flag7         string `json:"flag7"`
	Flag8         string `json:"flag8"`
}

func configRead() Config {
	config_file, err := os.Open("config.json")
	if err != nil {
		log.Panicf("failed to open config.json: %s\n", err)
	}
	decoder := json.NewDecoder(config_file)
	config := Config{}
	err = decoder.Decode(&config)
	if err != nil {
		log.Panicf("json decoding failed: %s\n", err)
	}
	return config
}

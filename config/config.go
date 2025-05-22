package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	SlackAppToken string `yaml:"slack_app_token"`
	SlackBotToken string `yaml:"slack_bot_token"`
	Channel       string `yaml:"channel"`
	ReportUser    string `yaml:"report_user"`
	Baseline      int    `yaml:"baseline"`
	DBPath        string `yaml:"db_path"`
}

func Load(path string) (Config, error) {
	var config Config
	data, err := os.ReadFile(path)
	if err != nil {
		return config, err
	}
	err = yaml.Unmarshal(data, &config)
	return config, err
}

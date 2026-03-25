package main

import (
	"encoding/json"
	"os"
)

// 接apikey的请求体
type Config struct {
	ApiKeys []string `json:"apikeys"`
}

// 读取config文件
func LoadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var config Config
	err = json.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

package main

import (
	"encoding/json"
	"os"
)

// 接apikey的请求体
type Config struct {
	Providers []Providers         `json:"providers"`
	Fallbacks map[string][]string `json:"fallbacks"`
	DSN       string              `json:"DSN"`
}
type Providers struct {
	Name      string   `json:"name"`
	Url       string   `json:"url"`
	Model     string   `json:"model"`
	Keys      []string `json:"keys"`
	PricePerK int64    `json:"price_per_k"`
	Pool      *KeyPool `json:"-"`
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

// 寻找特定的大模型供应商
func (c *Config) findProvider(targetName string) *Providers {
	for i := range c.Providers {
		if c.Providers[i].Name == targetName {
			return &c.Providers[i]
		}
	}
	return nil
}

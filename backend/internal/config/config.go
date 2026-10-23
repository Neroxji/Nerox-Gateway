package config

import (
	"encoding/json"
	"os"

	"aigateway/internal/provider"
)

// 接apikey的请求体
type Config struct {
	Providers []Provider          `json:"providers"`
	Fallbacks map[string][]string `json:"fallbacks"`
	DSN       string              `json:"DSN"`
}
type Provider struct {
	Name      string            `json:"name"`
	Url       string            `json:"url"`
	Model     string            `json:"model"`
	Keys      []string          `json:"keys"`
	PricePerK int64             `json:"price_per_k"`
	Pool      *provider.KeyPool `json:"-"`
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
func (c *Config) FindProvider(targetName string) *Provider {
	for i := range c.Providers {
		if c.Providers[i].Name == targetName {
			return &c.Providers[i]
		}
	}
	return nil
}

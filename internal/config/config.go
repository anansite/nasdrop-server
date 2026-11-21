package config

import (
	"flag"
	"os"
	"strconv"
)

type Config struct {
	Port     int
	DataDir  string
	DBPath   string
	Username string
	Password string
}

func Load() *Config {
	cfg := &Config{
		Port:     8090, // 默认使用 8090，避开 8888 端口
		DataDir:  "./data",
		DBPath:   "./nasdrop.db",
		Username: "",
		Password: "",
	}

	if envPort := os.Getenv("PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			cfg.Port = p
		}
	}
	if envData := os.Getenv("DATA_DIR"); envData != "" {
		cfg.DataDir = envData
	}
	if envDB := os.Getenv("DB_PATH"); envDB != "" {
		cfg.DBPath = envDB
	}
	if envUser := os.Getenv("NASDROP_USER"); envUser != "" {
		cfg.Username = envUser
	}
	if envPass := os.Getenv("NASDROP_PASS"); envPass != "" {
		cfg.Password = envPass
	}

	flag.IntVar(&cfg.Port, "port", cfg.Port, "HTTP/WebDAV service port")
	flag.StringVar(&cfg.DataDir, "data", cfg.DataDir, "Root storage directory for WebDAV")
	flag.StringVar(&cfg.DBPath, "db", cfg.DBPath, "SQLite database file path")
	flag.StringVar(&cfg.Username, "user", cfg.Username, "WebDAV / API authentication username (optional)")
	flag.StringVar(&cfg.Password, "pass", cfg.Password, "WebDAV / API authentication password (optional)")
	flag.Parse()

	return cfg
}

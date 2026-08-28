package config

import (
	"os"
	"strconv"
)

type Config struct {
	Port       int    // 管理后台端口
	ProxyPort  int    // 302代理端口
	PortalPort int    // 观影门户端口（6688；6665-6669 在浏览器不安全端口黑名单内不可用）
	DataDir    string // 数据目录
	ConfigDir  string // 配置目录
	JWTSecret  string // JWT密钥
}

func Load() *Config {
	port := getEnvInt("PORT", 6060)
	proxyPort := getEnvInt("PROXY_PORT", 6086)
	portalPort := getEnvInt("PORTAL_PORT", 6688)
	return &Config{
		Port:       port,
		ProxyPort:  proxyPort,
		PortalPort: portalPort,
		DataDir:    getEnv("DATA_DIR", "/data"),
		ConfigDir:  getEnv("CONFIG_DIR", "/config"),
		JWTSecret:  getEnv("JWT_SECRET", "strmhub-secret-key"),
	}
}

func (c *Config) PortStr() string {
	return strconv.Itoa(c.Port)
}

func (c *Config) ProxyPortStr() string {
	return strconv.Itoa(c.ProxyPort)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

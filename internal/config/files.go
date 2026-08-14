package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// ===== 路径辅助 =====

func (c *Config) AuthFile() string {
	return filepath.Join(c.ConfigDir, "auth.yaml")
}

func (c *Config) CookieFile() string {
	return filepath.Join(c.ConfigDir, "115-cookie.txt")
}

func (c *Config) SettingFile() string {
	return filepath.Join(c.ConfigDir, "setting.yaml")
}

// ===== 管理员账号（auth.yaml）=====

type AuthConfig struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
}

// IsAuthExists 检查是否已有管理员
func (c *Config) IsAuthExists() bool {
	_, err := os.Stat(c.AuthFile())
	return err == nil
}

// LoadAuth 读取管理员配置
func (c *Config) LoadAuth() (*AuthConfig, error) {
	data, err := os.ReadFile(c.AuthFile())
	if err != nil {
		return nil, err
	}
	var auth AuthConfig
	if err := yaml.Unmarshal(data, &auth); err != nil {
		return nil, err
	}
	return &auth, nil
}

// SaveAuth 保存管理员配置（密码会被 bcrypt 哈希）
func (c *Config) SaveAuth(username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	auth := AuthConfig{
		Username:     username,
		PasswordHash: string(hash),
	}
	data, err := yaml.Marshal(&auth)
	if err != nil {
		return err
	}
	return os.WriteFile(c.AuthFile(), data, 0600)
}

// VerifyAuth 验证账号密码
func (c *Config) VerifyAuth(username, password string) bool {
	auth, err := c.LoadAuth()
	if err != nil {
		return false
	}
	if auth.Username != username {
		return false
	}
	// 兼容明文密码（旧版本迁移）
	if auth.PasswordHash == "" || !strings.HasPrefix(auth.PasswordHash, "$2") {
		return auth.PasswordHash == password
	}
	return bcrypt.CompareHashAndPassword([]byte(auth.PasswordHash), []byte(password)) == nil
}

// ResetAuth 删除管理员文件（重置密码，不丢其他数据）
func (c *Config) ResetAuth() error {
	return os.Remove(c.AuthFile())
}

// ===== 115 Cookie（115-cookie.txt）=====

// LoadCookie 读取 115 Cookie
func (c *Config) LoadCookie() (string, error) {
	data, err := os.ReadFile(c.CookieFile())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// SaveCookie 写入 115 Cookie
func (c *Config) SaveCookie(cookie string) error {
	return os.WriteFile(c.CookieFile(), []byte(cookie), 0600)
}

// ===== 基础配置（setting.yaml）=====

// SettingFile 通用键值配置（value 为 JSON 字符串，与前端兼容）
type SettingMap map[string]string

// LoadSettings 读取所有配置
func (c *Config) LoadSettings() (SettingMap, error) {
	data, err := os.ReadFile(c.SettingFile())
	if err != nil {
		if os.IsNotExist(err) {
			return SettingMap{}, nil
		}
		return nil, err
	}
	var s SettingMap
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s == nil {
		s = SettingMap{}
	}
	return s, nil
}

// GetSetting 读取单个配置
func (c *Config) GetSetting(key string) string {
	s, err := c.LoadSettings()
	if err != nil {
		return ""
	}
	return s[key]
}

// SaveSetting 保存单个配置（保留其他配置）
func (c *Config) SaveSetting(key, value string) error {
	s, err := c.LoadSettings()
	if err != nil {
		s = SettingMap{}
	}
	s[key] = value
	data, err := yaml.Marshal(&s)
	if err != nil {
		return err
	}
	return os.WriteFile(c.SettingFile(), data, 0644)
}

// EnsureConfigDir 确保配置目录存在
func (c *Config) EnsureConfigDir() error {
	return os.MkdirAll(c.ConfigDir, 0755)
}

// ConfigSummary 返回配置目录用于日志
func (c *Config) ConfigSummary() string {
	return fmt.Sprintf("配置目录: %s, 数据目录: %s", c.ConfigDir, c.DataDir)
}

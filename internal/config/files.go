package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

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

// UpdateAuthUsername 只改用户名，保留原密码哈希
func (c *Config) UpdateAuthUsername(newUsername string) error {
	auth, err := c.LoadAuth()
	if err != nil {
		return err
	}
	auth.Username = newUsername
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

// Load115Device 读取 115 登录设备类型（115-device.txt）
func (c *Config) Load115Device() string {
	data, err := os.ReadFile(filepath.Join(c.ConfigDir, "115-device.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Save115Device 写入 115 登录设备类型
func (c *Config) Save115Device(device string) error {
	return os.WriteFile(filepath.Join(c.ConfigDir, "115-device.txt"), []byte(device), 0644)
}

// ===== 115 开放平台 Token（115-open-token.yaml）=====

// OpenToken 115 开放平台 OAuth 凭证
type OpenToken struct {
	AppID        string `yaml:"app_id"`
	AccessToken  string `yaml:"access_token"`
	RefreshToken string `yaml:"refresh_token"`
	ExpiresAt    int64  `yaml:"expires_at"` // unix 秒
}

// OpenTokenFile token 文件路径
func (c *Config) OpenTokenFile() string {
	return filepath.Join(c.ConfigDir, "115-open-token.yaml")
}

// LoadOpenToken 读取开放平台 token
func (c *Config) LoadOpenToken() (*OpenToken, error) {
	data, err := os.ReadFile(c.OpenTokenFile())
	if err != nil {
		return nil, err
	}
	var t OpenToken
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// SaveOpenToken 写入开放平台 token
func (c *Config) SaveOpenToken(t *OpenToken) error {
	data, err := yaml.Marshal(t)
	if err != nil {
		return err
	}
	return os.WriteFile(c.OpenTokenFile(), data, 0600)
}

// ClearOpenToken 清除开放平台 token（重新授权时用）
func (c *Config) ClearOpenToken() error {
	if err := os.Remove(c.OpenTokenFile()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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

// settingsMu 配置文件读改写互斥：并发的 POST /config/setting 同时
// LoadSettings→改→WriteFile 会互相覆盖丢 key
var settingsMu sync.Mutex

// GetSetting 读取单个配置
func (c *Config) GetSetting(key string) string {
	s, err := c.LoadSettings()
	if err != nil {
		return ""
	}
	return s[key]
}

// SaveSetting 保存单个配置（保留其他配置）。临时文件+rename 原子落盘：
// 进程中途被杀/断电不会留下截断的 setting.yaml（截断文件会让全部配置
// 读取回退到 DB 旧值，损坏被掩盖）
func (c *Config) SaveSetting(key, value string) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	s, err := c.LoadSettings()
	if err != nil {
		s = SettingMap{}
	}
	s[key] = value
	data, err := yaml.Marshal(&s)
	if err != nil {
		return err
	}
	tmp := c.SettingFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, c.SettingFile())
}

// EnsureConfigDir 确保配置目录存在
func (c *Config) EnsureConfigDir() error {
	return os.MkdirAll(c.ConfigDir, 0755)
}

// ConfigSummary 返回配置目录用于日志
func (c *Config) ConfigSummary() string {
	return fmt.Sprintf("配置目录: %s, 数据目录: %s", c.ConfigDir, c.DataDir)
}

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Settings Web 模式全局设置（默认文件 ./settings.yaml，可用 -web-settings 指定）
type Settings struct {
	Auth        AuthCfg `yaml:"auth"`        // 登录账号
	ConfigsDir  string  `yaml:"configsDir"`  // 多配置文件目录（默认 ./configs）
	HistorySize int     `yaml:"historySize"` // 运行历史保留条数（默认 10，重启即清空）
}

// AuthCfg 登录账号（密码以 盐+哈希 存储，不存明文）
type AuthCfg struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"passwordHash"`
	Salt         string `yaml:"salt"`
}

// defaultSettings 首次启动的默认设置：admin/admin + 默认配置目录
func defaultSettings() *Settings {
	return &Settings{
		Auth:        AuthCfg{Username: "admin"},
		ConfigsDir:  "configs",
		HistorySize: 10,
	}
}

// applyDefaults 填充默认值（文件缺失字段时）
func (s *Settings) applyDefaults() {
	if s.Auth.Username == "" {
		s.Auth.Username = "admin"
	}
	if s.ConfigsDir == "" {
		s.ConfigsDir = "configs"
	}
	if s.HistorySize <= 0 {
		s.HistorySize = 10
	}
}

// loadSettings 读取设置文件；不存在则创建默认设置（admin/admin）并写盘。
// 密码以随机盐哈希存储：首次启动时对 admin/admin 做哈希，文件里不出现明文。
func loadSettings(path string) (*Settings, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		s := defaultSettings()
		s.setPassword("admin", "admin")
		if err := saveSettings(path, s); err != nil {
			return nil, err
		}
		return s, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := &Settings{}
	if err := yaml.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	s.applyDefaults()
	// 兼容旧文件：无密码哈希时（异常），重置为 admin/admin
	if s.Auth.PasswordHash == "" || s.Auth.Salt == "" {
		s.setPassword("admin", "admin")
		if err := saveSettings(path, s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func saveSettings(path string, s *Settings) error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// setPassword 设置账号密码（生成随机盐 + sha256 哈希）
func (s *Settings) setPassword(username, password string) {
	s.Auth.Username = username
	salt := make([]byte, 16)
	rand.Read(salt)
	s.Auth.Salt = hex.EncodeToString(salt)
	s.Auth.PasswordHash = hashPassword(password, s.Auth.Salt)
}

// checkPassword 校验账号密码
func (s *Settings) checkPassword(username, password string) bool {
	if username != s.Auth.Username {
		return false
	}
	return hashPassword(password, s.Auth.Salt) == s.Auth.PasswordHash
}

func hashPassword(password, salt string) string {
	h := sha256.New()
	h.Write([]byte(salt))
	h.Write([]byte(password))
	return hex.EncodeToString(h.Sum(nil))
}

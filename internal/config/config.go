package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"beancount-autoupdate/internal/logger"

	"github.com/BurntSushi/toml"
	"github.com/joho/godotenv"
)

// Config 主配置结构
type Config struct {
	Telegram   TelegramConfig   `toml:"telegram"`
	LLM        LLMConfig        `toml:"llm"`
	Beancount  BeancountConfig  `toml:"beancount"`
	Git        GitConfig        `toml:"git"`
	Logging    LoggingConfig    `toml:"logging"`
	WebDAV     WebDAVConfig     `toml:"webdav"`
	HTTPServer HTTPServerConfig `toml:"http_server"`
	projectDir string
}

// TelegramConfig Telegram Bot 配置
type TelegramConfig struct {
	Token             string `toml:"token"`
	AllowedUserIDs    []int  `toml:"allowed_user_ids"`
	DeleteUserMessage bool   `toml:"delete_user_message"` // 是否在确认提交后删除用户发送的图片消息
}

// LLMConfig LLM 配置
type LLMConfig struct {
	BaseURL      string `toml:"base_url"`
	Model        string `toml:"model"`
	APIKey       string `toml:"api_key"`
	Timeout      int    `toml:"timeout"`
	ExtendPrompt string `toml:"extend_prompt"`  // 额外的 prompt，可以是 "append" 或 "replace"
	MaxImageSize int    `toml:"max_image_size"` // 图片最大尺寸限制（宽度或高度），0 表示不限制
}

// BeancountConfig Beancount 配置
type BeancountConfig struct {
	DataDir           string `toml:"data_dir"`
	Title             string `toml:"title"`
	OperatingCurrency string `toml:"operating_currency"`
}

// GitConfig Git 配置
type GitConfig struct {
	RepoURL             string `toml:"repo_url"`
	AutoCommit          bool   `toml:"auto_commit"`
	CommitMessagePrefix string `toml:"commit_message_prefix"`
	AutoPush            bool   `toml:"auto_push"`
}

// LoggingConfig 日志配置
type LoggingConfig struct {
	Level       string `toml:"level"`
	LogDir      string `toml:"log_dir"`
	LogFile     string `toml:"log_file"`
	MaxBytes    int64  `toml:"max_bytes"`
	BackupCount int    `toml:"backup_count"`
}

// WebDAVConfig WebDAV 配置
type WebDAVConfig struct {
	Enabled   bool   `toml:"enabled"`
	URL       string `toml:"url"`
	PublicURL string `toml:"public_url"`
	Username  string `toml:"username"`
	Password  string `toml:"password"`
	Path      string `toml:"path"`
	VerifySSL bool   `toml:"verify_ssl"`
}

// HTTPServerConfig HTTP 上传服务配置
type HTTPServerConfig struct {
	Enabled         bool   `toml:"enabled"`
	ListenAddr      string `toml:"listen_addr"`
	TargetUserID    int    `toml:"target_user_id"`
	MaxUploadSizeMB int64  `toml:"max_upload_size_mb"`
	ReadTimeoutSec  int    `toml:"read_timeout_sec"`
	WriteTimeoutSec int    `toml:"write_timeout_sec"`
}

// LoadConfig 加载配置文件
func LoadConfig(configPath string) (*Config, error) {
	// 加载环境变量
	if err := godotenv.Load(); err != nil {
		// .env 文件不存在不是错误，继续执行
		logger.Debugf(".env 文件加载失败（可能不存在）: %v", err)
	}

	// 确定配置文件路径
	if configPath == "" {
		configPath = "config.toml"
	}

	// 读取 TOML 配置文件
	var config Config
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		return nil, fmt.Errorf("failed to decode config file: %w", err)
	}

	config.setDefaults()

	// 设置项目根目录
	config.projectDir = filepath.Dir(configPath)

	// 从环境变量覆盖配置
	config.loadFromEnv()

	return &config, nil
}

func (c *Config) setDefaults() {
	if c.HTTPServer.ListenAddr == "" {
		c.HTTPServer.ListenAddr = "127.0.0.1:8080"
	}
	if c.HTTPServer.MaxUploadSizeMB <= 0 {
		c.HTTPServer.MaxUploadSizeMB = 20
	}
	if c.HTTPServer.ReadTimeoutSec <= 0 {
		c.HTTPServer.ReadTimeoutSec = 15
	}
	if c.HTTPServer.WriteTimeoutSec <= 0 {
		c.HTTPServer.WriteTimeoutSec = 30
	}
}

// loadFromEnv 从环境变量加载配置
func (c *Config) loadFromEnv() {
	// Telegram
	if token := os.Getenv("TELEGRAM_BOT_TOKEN"); token != "" {
		c.Telegram.Token = token
	}

	// LLM
	if apiKey := os.Getenv("LLM_API_KEY"); apiKey != "" {
		c.LLM.APIKey = apiKey
	}

	// WebDAV
	if username := os.Getenv("WEBDAV_USERNAME"); username != "" {
		c.WebDAV.Username = username
	}
	if password := os.Getenv("WEBDAV_PASSWORD"); password != "" {
		c.WebDAV.Password = password
	}
}

// GetProjectDir 获取项目根目录
func (c *Config) GetProjectDir() string {
	return c.projectDir
}

// GetAbsPath 获取相对于项目根目录的绝对路径
func (c *Config) GetAbsPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(c.projectDir, path)
}

// Validate 验证配置
func (c *Config) Validate() []string {
	var errors []string

	// 验证 Telegram 配置
	if c.Telegram.Token == "" {
		errors = append(errors, "Telegram Bot Token 未设置，请设置环境变量 TELEGRAM_BOT_TOKEN 或在 config.toml 中配置")
	}

	// 验证 LLM 配置
	if c.LLM.APIKey == "" {
		errors = append(errors, "LLM API Key 未设置，请设置环境变量 LLM_API_KEY 或在 config.toml 中配置")
	}

	// 验证 Git 配置
	if c.Git.RepoURL == "" {
		errors = append(errors, "Git 远程仓库地址未设置，请在 config.toml 中配置 git.repo_url")
	}

	if c.HTTPServer.Enabled {
		if c.HTTPServer.TargetUserID <= 0 {
			errors = append(errors, "HTTP 服务已启用，但 target_user_id 未设置，请在 config.toml 中配置 http_server.target_user_id")
		}
		if c.HTTPServer.ListenAddr == "" {
			errors = append(errors, "HTTP 服务已启用，但 listen_addr 为空，请在 config.toml 中配置 http_server.listen_addr")
		}
	}

	return errors
}

// IsUserAllowed 检查用户是否有权限
func (c *Config) IsUserAllowed(userID int) bool {
	if len(c.Telegram.AllowedUserIDs) == 0 {
		return true // 允许所有用户
	}
	for _, id := range c.Telegram.AllowedUserIDs {
		if id == userID {
			return true
		}
	}
	return false
}

// ParseAllowedUserIDs 解析允许的用户 ID 列表
func (c *Config) ParseAllowedUserIDs(envVar string) {
	if envValue := os.Getenv(envVar); envValue != "" {
		parts := strings.Split(envValue, ",")
		ids := make([]int, 0, len(parts))
		for _, part := range parts {
			if id, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			c.Telegram.AllowedUserIDs = ids
		}
	}
}

package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sirupsen/logrus"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/config"
	"beancount-autoupdate/internal/embed"
	"beancount-autoupdate/internal/git"
	"beancount-autoupdate/internal/llm"
	"beancount-autoupdate/internal/logger"
	"beancount-autoupdate/internal/telegram"
	"beancount-autoupdate/internal/webdav"
)

func main() {
	// 初始化嵌入的模板
	if err := embed.InitTemplates(); err != nil {
		logrus.Fatalf("Failed to initialize embedded templates: %v", err)
	}

	// 加载配置
	cfg, err := config.LoadConfig("")
	if err != nil {
		fmt.Printf("❌ 加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// 验证配置
	errors := cfg.Validate()
	if len(errors) > 0 {
		fmt.Println("❌ 配置验证失败:")
		for _, e := range errors {
			fmt.Printf("  - %s\n", e)
		}
		os.Exit(1)
	}

	// 初始化日志
	if err := logger.Init(
		cfg.GetAbsPath(cfg.Logging.LogDir),
		cfg.Logging.LogFile,
		cfg.Logging.Level,
		cfg.Logging.MaxBytes,
		cfg.Logging.BackupCount,
	); err != nil {
		fmt.Printf("❌ 初始化日志失败: %v\n", err)
		os.Exit(1)
	}

	logger.Info("========================================")
	logger.Info("  Beancount AutoUpdate 启动中...")
	logger.Info("========================================")

	// 初始化 Beancount 管理器
	logger.Info("初始化 Beancount 管理器...")
	beancountMgr, err := beancount.NewManager(
		cfg.GetAbsPath(cfg.Beancount.DataDir),
		cfg.Beancount.Title,
		cfg.Beancount.OperatingCurrency,
	)
	if err != nil {
		logger.Fatalf("初始化 Beancount 管理器失败: %v", err)
	}

	// 如果是新初始化的仓库，提交并推送初始文件
	if beancountMgr.IsNewRepo() {
		logger.Info("检测到新仓库，正在提交初始文件...")
		// 初始化 Git 管理器后处理
	}

	// 初始化 Git 管理器
	logger.Info("初始化 Git 管理器...")
	gitMgr, err := git.NewManager(
		cfg.GetAbsPath(cfg.Beancount.DataDir),
		cfg.Git.RepoURL,
		cfg.Git.AutoCommit,
		cfg.Git.CommitMessagePrefix,
		cfg.Git.AutoPush,
		cfg.Git.PushTimeout,
		cfg.Git.ConflictStrategy,
	)
	if err != nil {
		logger.Fatalf("初始化 Git 管理器失败: %v", err)
	}

	// 如果是新仓库，提交初始文件
	if beancountMgr.IsNewRepo() {
		if _, err := gitMgr.CommitChanges("Initial commit: Initialize beancount repository structure"); err != nil {
			logger.Warnf("提交初始文件失败: %v", err)
		}
		if cfg.Git.AutoPush {
			if _, err := gitMgr.PushChanges(); err != nil {
				logger.Warnf("推送初始文件失败: %v", err)
			}
		}
		logger.Info("初始文件已提交并推送")
	}

	// 初始化 LLM 解析器
	logger.Info("初始化 LLM 解析器...")
	llmParser := llm.NewParser(
		cfg.LLM.BaseURL,
		cfg.LLM.Model,
		cfg.LLM.APIKey,
		cfg.LLM.Timeout,
	)

	// 初始化 WebDAV 管理器（如果启用）
	var webdavMgr *webdav.Manager
	if cfg.WebDAV.Enabled {
		logger.Info("初始化 WebDAV 管理器...")
		webdavMgr, err = webdav.NewManager(
			cfg.WebDAV.URL,
			cfg.WebDAV.Username,
			cfg.WebDAV.Password,
			cfg.WebDAV.VerifySSL,
		)
		if err != nil {
			logger.Warnf("初始化 WebDAV 管理器失败: %v", err)
			webdavMgr = nil
		} else {
			// 测试连接
			if success, err := webdavMgr.TestConnection(); err != nil || !success {
				logger.Warnf("WebDAV 连接测试失败: %v", err)
			} else {
				logger.Info("WebDAV 连接测试成功")
			}
		}
	}

	// 初始化 Telegram Bot
	logger.Info("初始化 Telegram Bot...")
	bot, err := telegram.NewBot(
		cfg,
		beancountMgr,
		llmParser,
		gitMgr,
		webdavMgr,
	)
	if err != nil {
		logger.Fatalf("初始化 Telegram Bot 失败: %v", err)
	}

	logger.Info("所有模块初始化完成！")
	logger.Info("========================================")
	logger.Info("Bot 正在运行...")
	logger.Info("按 Ctrl+C 退出")

	// 运行 Bot
	go func() {
		if err := bot.Run(); err != nil {
			logger.Errorf("Bot 运行出错: %v", err)
		}
	}()

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	logger.Info("收到中断信号，正在关闭...")
	logger.Info("Beancount AutoUpdate 已停止")
}

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"beancount-autoupdate/internal/analysis"
	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/config"
	"beancount-autoupdate/internal/embed"
	"beancount-autoupdate/internal/git"
	"beancount-autoupdate/internal/httpingest"
	"beancount-autoupdate/internal/llm"
	"beancount-autoupdate/internal/logger"
	"beancount-autoupdate/internal/telegram"
	"beancount-autoupdate/internal/webdav"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	// 定义命令行参数
	configPath := flag.String("config", "", "配置文件路径 (默认: ./config.toml)")
	version := flag.Bool("version", false, "显示版本信息")
	debug := flag.Bool("d", false, "启用 debug 模式")
	flag.Parse()

	// 显示版本信息
	if *version {
		fmt.Printf("Beancount AutoUpdate\n")
		fmt.Printf("Version: %s\n", Version)
		fmt.Printf("Build Time: %s\n", BuildTime)
		os.Exit(0)
	}

	// 初始化嵌入的模板
	if err := embed.InitTemplates(); err != nil {
		logger.Fatalf("Failed to initialize embedded templates: %v", err)
	}

	// 加载配置
	cfg, err := config.LoadConfig(*configPath)
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

	// 如果指定了 -d 参数，强制使用 debug 级别
	if *debug {
		logger.SetLevel("debug")
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
		cfg.LLM.ExtendPrompt,
		cfg.LLM.MaxImageSize,
	)

	// 初始化分析服务（如果启用）
	var analysisSvc *analysis.Service
	if cfg.Analysis.Enabled {
		logger.Info("初始化账单分析服务...")
		analysisModel := cfg.Analysis.LLMModel
		if analysisModel == "" {
			analysisModel = cfg.LLM.Model
		}
		ledgerFile := cfg.Analysis.LedgerFile
		if ledgerFile == "" {
			ledgerFile = filepath.Join(cfg.GetAbsPath(cfg.Beancount.DataDir), "main.bean")
		} else {
			ledgerFile = cfg.GetAbsPath(ledgerFile)
		}

		analysisSvc = analysis.NewService(analysis.Options{
			Enabled:        true,
			BeanQueryBin:   cfg.Analysis.BeanQueryBin,
			BeanReportBin:  cfg.Analysis.BeanReportBin,
			LedgerFile:     ledgerFile,
			Timeout:        time.Duration(cfg.Analysis.TimeoutSec) * time.Second,
			MaxOutputLines: cfg.Analysis.MaxOutputLines,
			Summarizer: analysis.NewOpenAISummarizer(analysis.OpenAIOptions{
				BaseURL: cfg.LLM.BaseURL,
				APIKey:  cfg.LLM.APIKey,
				Model:   analysisModel,
				Timeout: time.Duration(cfg.Analysis.TimeoutSec) * time.Second,
			}),
		})
	}

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
		analysisSvc,
	)
	if err != nil {
		logger.Fatalf("初始化 Telegram Bot 失败: %v", err)
	}

	logger.Info("所有模块初始化完成！")

	// 初始化 HTTP 上传服务（如果启用）
	var ingestServer *httpingest.Server
	if cfg.HTTPServer.Enabled {
		ingestServer = httpingest.NewServer(cfg.HTTPServer, bot)
	}

	logger.Info("========================================")
	logger.Info("Bot 正在运行...")
	if ingestServer != nil {
		logger.Infof("HTTP 上传服务已启用: %s", cfg.HTTPServer.ListenAddr)
	}
	logger.Info("按 Ctrl+C 退出")

	// 运行 Bot
	go func() {
		if err := bot.Run(); err != nil {
			logger.Errorf("Bot 运行出错: %v", err)
		}
	}()

	if ingestServer != nil {
		go func() {
			if err := ingestServer.Run(); err != nil {
				logger.Errorf("HTTP 上传服务运行出错: %v", err)
			}
		}()
	}

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	logger.Info("收到中断信号，正在关闭...")
	if ingestServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ingestServer.Shutdown(shutdownCtx); err != nil {
			logger.Warnf("关闭 HTTP 上传服务失败: %v", err)
		}
	}
	logger.Info("Beancount AutoUpdate 已停止")
}

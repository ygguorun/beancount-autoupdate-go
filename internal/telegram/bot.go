package telegram

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/config"
	"beancount-autoupdate/internal/git"
	"beancount-autoupdate/internal/llm"
	"beancount-autoupdate/internal/logger"
	"beancount-autoupdate/internal/webdav"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Bot Telegram Bot
type Bot struct {
	config          *config.Config
	beancountMgr    *beancount.Manager
	llmParser       *llm.Parser
	gitMgr          *git.Manager
	webdavMgr       *webdav.Manager
	botAPI          *tgbotapi.BotAPI
	pendingTx       map[int]map[string]*beancount.PendingTransaction // userID -> transactionID -> PendingTransaction
	confirmingTx    map[int]map[string]struct{}
	waitingForInput map[int]map[string]string // userID -> transactionID -> inputType
	mu              sync.RWMutex
	stateMu         sync.Mutex
	stateFilePath   string
	llmSemaphore    chan struct{} // LLM 信号量，控制并发调用
	runDone         chan struct{}
}

// NewBot 创建 Telegram Bot
func NewBot(
	cfg *config.Config,
	beancountMgr *beancount.Manager,
	llmParser *llm.Parser,
	gitMgr *git.Manager,
	webdavMgr *webdav.Manager,
) (*Bot, error) {
	botAPI, err := tgbotapi.NewBotAPI(cfg.Telegram.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot API: %w", err)
	}

	botAPI.Debug = false

	b := &Bot{
		config:          cfg,
		beancountMgr:    beancountMgr,
		llmParser:       llmParser,
		gitMgr:          gitMgr,
		webdavMgr:       webdavMgr,
		botAPI:          botAPI,
		pendingTx:       make(map[int]map[string]*beancount.PendingTransaction),
		waitingForInput: make(map[int]map[string]string),
		stateFilePath:   cfg.GetAbsPath(filepath.Join("tmp", "telegram_sessions.json")),
		llmSemaphore:    make(chan struct{}, 1), // 限制 LLM 并发为1
		runDone:         make(chan struct{}),
	}

	if err := b.loadSessionState(); err != nil {
		logger.Warnf("加载会话状态失败，将使用空状态启动: %v", err)
	}

	return b, nil
}

func (b *Bot) Shutdown(ctx context.Context) error {
	b.botAPI.StopReceivingUpdates()
	select {
	case <-b.runDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

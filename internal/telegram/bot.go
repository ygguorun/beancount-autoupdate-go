package telegram

import (
	"fmt"
	"sync"

	"beancount-autoupdate/internal/analysis"
	"beancount-autoupdate/internal/beancount"
	"beancount-autoupdate/internal/config"
	"beancount-autoupdate/internal/git"
	"beancount-autoupdate/internal/llm"
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
	analysisSvc     *analysis.Service
	botAPI          *tgbotapi.BotAPI
	pendingTx       map[int]map[string]*beancount.PendingTransaction // userID -> transactionID -> PendingTransaction
	waitingForInput map[int]map[string]string                        // userID -> transactionID -> inputType
	mu              sync.RWMutex
	llmSemaphore    chan struct{} // LLM 信号量，控制并发调用
}

// NewBot 创建 Telegram Bot
func NewBot(
	cfg *config.Config,
	beancountMgr *beancount.Manager,
	llmParser *llm.Parser,
	gitMgr *git.Manager,
	webdavMgr *webdav.Manager,
	analysisSvc *analysis.Service,
) (*Bot, error) {
	botAPI, err := tgbotapi.NewBotAPI(cfg.Telegram.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot API: %w", err)
	}

	botAPI.Debug = false

	return &Bot{
		config:          cfg,
		beancountMgr:    beancountMgr,
		llmParser:       llmParser,
		gitMgr:          gitMgr,
		webdavMgr:       webdavMgr,
		analysisSvc:     analysisSvc,
		botAPI:          botAPI,
		pendingTx:       make(map[int]map[string]*beancount.PendingTransaction),
		waitingForInput: make(map[int]map[string]string),
		llmSemaphore:    make(chan struct{}, 1), // 限制 LLM 并发为1
	}, nil
}

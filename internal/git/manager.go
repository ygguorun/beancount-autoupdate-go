package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"

	"github.com/sirupsen/logrus"
	gossh "golang.org/x/crypto/ssh"
)

var logger = logrus.StandardLogger()

// Manager Git 管理器
type Manager struct {
	repoPath            string
	repoURL             string
	autoCommit          bool
	commitMessagePrefix string
	autoPush            bool
	pushTimeout         time.Duration
	conflictStrategy    string
	repo                *git.Repository
	mu                  sync.Mutex
}

// NewManager 创建 Git 管理器
func NewManager(repoPath, repoURL string, autoCommit bool, commitMessagePrefix string, autoPush bool, pushTimeout int, conflictStrategy string) (*Manager, error) {
	m := &Manager{
		repoPath:            repoPath,
		repoURL:             repoURL,
		autoCommit:          autoCommit,
		commitMessagePrefix: commitMessagePrefix,
		autoPush:            autoPush,
		pushTimeout:         time.Duration(pushTimeout) * time.Second,
		conflictStrategy:    conflictStrategy,
	}

	if err := m.initRepo(); err != nil {
		return nil, err
	}

	return m, nil
}

// initRepo 初始化 Git 仓库
func (m *Manager) initRepo() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 检查本地仓库是否存在
	if _, err := os.Stat(m.repoPath); os.IsNotExist(err) {
		// 本地仓库不存在
		if m.repoURL != "" {
			// 尝试克隆
			if err := m.cloneRepo(); err != nil {
				// 克隆失败，初始化空仓库
				return m.initEmptyRepo()
			}
			return nil
		}
		// 初始化空仓库
		return m.initEmptyRepo()
	}

	// 本地仓库存在，尝试加载
	repo, err := git.PlainOpen(m.repoPath)
	if err != nil {
		// 不是 Git 仓库，初始化一个
		return m.initEmptyRepo()
	}

	m.repo = repo
	return nil
}

// initEmptyRepo 初始化空仓库
func (m *Manager) initEmptyRepo() error {
	if err := os.MkdirAll(m.repoPath, 0755); err != nil {
		return fmt.Errorf("failed to create repo directory: %w", err)
	}

	repo, err := git.PlainInit(m.repoPath, false)
	if err != nil {
		return fmt.Errorf("failed to init repo: %w", err)
	}

	m.repo = repo
	return nil
}

// cloneRepo 克隆仓库
func (m *Manager) cloneRepo() error {
	// 解析 URL
	auth := m.getAuth()
	cloneOptions := &git.CloneOptions{
		URL:      m.repoURL,
		Auth:     auth,
		Progress: os.Stdout,
	}

	repo, err := git.PlainClone(m.repoPath, false, cloneOptions)
	if err != nil {
		return fmt.Errorf("failed to clone repo: %w", err)
	}

	m.repo = repo
	return nil
}

// getAuth 获取认证信息
func (m *Manager) getAuth() transport.AuthMethod {
	// 检查是否是 SSH URL
	if strings.HasPrefix(m.repoURL, "git@") || strings.HasPrefix(m.repoURL, "ssh://") {
		return m.getSSHAuth()
	}

	// HTTPS 认证
	return m.getHTTPAuth()
}

// getHTTPAuth 获取 HTTP 认证
func (m *Manager) getHTTPAuth() transport.AuthMethod {
	username := os.Getenv("GIT_USERNAME")
	password := os.Getenv("GIT_PASSWORD")

	if username != "" && password != "" {
		return &http.BasicAuth{
			Username: username,
			Password: password,
		}
	}

	return nil
}

// getSSHAuth 获取 SSH 认证
func (m *Manager) getSSHAuth() transport.AuthMethod {
	logger.Infof("尝试配置 SSH 认证...")

	homeDir, err := os.UserHomeDir()
	if err != nil {
		logger.Errorf("获取用户主目录失败: %v", err)
		return nil
	}

	// 1. 尝试从 ~/.ssh/config 读取配置
	sshConfigPath := filepath.Join(homeDir, ".ssh", "config")
	logger.Infof("尝试读取 SSH 配置文件: %s", sshConfigPath)

	if configData, err := os.ReadFile(sshConfigPath); err == nil {
		configStr := string(configData)
		logger.Infof("SSH 配置文件读取成功，尝试解析...")

		// 解析配置文件，查找 github.com 的 IdentityFile
		lines := strings.Split(configStr, "\n")
		var identityFile string
		inGitHubHost := false

		for _, line := range lines {
			line = strings.TrimSpace(line)

			// 检查是否是 github.com 的 Host 块
			if strings.HasPrefix(line, "Host ") {
				host := strings.TrimSpace(strings.TrimPrefix(line, "Host "))
				if host == "github.com" || host == "*.github.com" {
					inGitHubHost = true
					logger.Infof("找到 github.com 配置块")
				} else {
					inGitHubHost = false
				}
			} else if inGitHubHost && strings.HasPrefix(line, "IdentityFile") {
				identityFile = strings.TrimSpace(strings.TrimPrefix(line, "IdentityFile"))
				// 展开波浪号
				if strings.HasPrefix(identityFile, "~/") {
					identityFile = filepath.Join(homeDir, identityFile[2:])
				}
				logger.Infof("找到 IdentityFile: %s", identityFile)
			}
		}

		// 如果找到了 IdentityFile，使用它
		if identityFile != "" {
			if auth := m.loadSSHKey(identityFile); auth != nil {
				return auth
			}
		}
	} else {
		logger.Infof("无法读取 SSH 配置文件: %v", err)
	}

	// 2. 如果没有找到配置，尝试常见的 SSH 密钥路径
	logger.Infof("尝试查找常见的 SSH 密钥文件...")
	keyPaths := []string{
		filepath.Join(homeDir, ".ssh", "id_rsa"),
		filepath.Join(homeDir, ".ssh", "id_ed25519"),
		filepath.Join(homeDir, ".ssh", "id_ecdsa"),
	}

	for _, keyPath := range keyPaths {
		logger.Infof("检查密钥文件: %s", keyPath)
		if _, err := os.Stat(keyPath); err == nil {
			logger.Infof("✓ 找到密钥文件: %s", keyPath)
			if auth := m.loadSSHKey(keyPath); auth != nil {
				return auth
			}
		}
	}

	// 3. 如果没有找到密钥文件，尝试使用 SSH agent
	logger.Infof("未找到密钥文件，尝试使用 SSH agent...")
	if auth, err := ssh.NewSSHAgentAuth("git"); err == nil {
		logger.Infof("✓ 使用 SSH agent 认证成功")
		return auth
	} else {
		logger.Infof("✗ SSH agent 认证失败: %v", err)
	}

	logger.Errorf("✗ 未找到任何 SSH 认证方式")
	return nil
}

// loadSSHKey 加载 SSH 密钥文件
func (m *Manager) loadSSHKey(keyPath string) transport.AuthMethod {
	logger.Infof("尝试加载 SSH 密钥: %s", keyPath)

	// 读取密钥文件
	sshKey, err := os.ReadFile(keyPath)
	if err != nil {
		logger.Errorf("✗ 读取 SSH 密钥失败: %v", err)
		return nil
	}

	// 解析密钥
	logger.Infof("尝试解析 SSH 密钥...")
	signer, err := gossh.ParsePrivateKey(sshKey)
	if err != nil {
		// 如果密钥有密码，尝试从环境变量获取
		if strings.Contains(err.Error(), "password protected") {
			logger.Infof("密钥需要密码保护")
			passphrase := os.Getenv("GIT_SSH_PASSPHRASE")
			if passphrase != "" {
				logger.Infof("使用环境变量中的密码解析密钥...")
				signer, err = gossh.ParsePrivateKeyWithPassphrase(sshKey, []byte(passphrase))
				if err != nil {
					logger.Errorf("✗ 使用密码解析 SSH 密钥失败: %v", err)
					return nil
				}
				logger.Infof("✓ 使用密码解析密钥成功")
			} else {
				logger.Errorf("✗ SSH 密钥需要密码，请设置 GIT_SSH_PASSPHRASE 环境变量")
				return nil
			}
		} else {
			logger.Errorf("✗ 解析 SSH 密钥失败: %v", err)
			return nil
		}
	} else {
		logger.Infof("✓ 解析 SSH 密钥成功")
	}

	// 创建认证对象
	auth := &ssh.PublicKeys{User: "git", Signer: signer}

	// 配置 known_hosts
	homeDir, err := os.UserHomeDir()
	if err != nil {
		logger.Warnf("获取用户主目录失败: %v", err)
		auth.HostKeyCallback = gossh.InsecureIgnoreHostKey()
	} else {
		knownHostsPath := filepath.Join(homeDir, ".ssh", "known_hosts")
		knownHostsCallback, err := ssh.NewKnownHostsCallback(knownHostsPath)
		if err != nil {
			logger.Warnf("✗ 无法读取 known_hosts 文件: %v", err)
			logger.Warnf("使用不安全的 HostKeyCallback（仅用于测试）")
			auth.HostKeyCallback = gossh.InsecureIgnoreHostKey()
		} else {
			logger.Infof("✓ 使用 known_hosts 文件: %s", knownHostsPath)
			auth.HostKeyCallback = knownHostsCallback
		}
	}

	logger.Infof("✓ SSH 认证配置完成，使用密钥: %s", keyPath)
	return auth
}

// HasChanges 检查是否有未提交的更改
func (m *Manager) HasChanges() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	logger.Infof("检查仓库状态...")

	if m.repo == nil {
		logger.Errorf("仓库未初始化")
		return false, fmt.Errorf("repository not initialized")
	}

	logger.Infof("获取 Worktree...")
	worktree, err := m.repo.Worktree()
	if err != nil {
		logger.Errorf("获取 Worktree 失败: %v", err)
		return false, err
	}

	logger.Infof("获取仓库状态...")
	status, err := worktree.Status()
	if err != nil {
		logger.Errorf("获取仓库状态失败: %v", err)
		return false, err
	}

	logger.Infof("仓库是否干净: %v", status.IsClean())

	if !status.IsClean() {
		logger.Infof("检测到未提交的更改:")
		for file, fileStatus := range status {
			staging := fileStatus.Staging
			worktree := fileStatus.Worktree
			logger.Infof("  %s: Staging=%v, Worktree=%v", file, staging, worktree)
		}
		return true, nil
	}

	logger.Infof("仓库干净，没有未提交的更改")
	return false, nil
}

// CommitChanges 提交更改
func (m *Manager) CommitChanges(message string) (bool, error) {
	logger.Infof("开始执行 Git 提交...")
	logger.Infof("提交信息: %s", message)
	logger.Infof("仓库路径: %s", m.repoPath)

	// 检查是否有远程仓库
	if !m.autoCommit {
		logger.Infof("自动提交已禁用")
		return false, nil
	}

	// 检查仓库是否初始化
	if m.repo == nil {
		logger.Errorf("仓库未初始化")
		return false, fmt.Errorf("repository not initialized")
	}

	// 获取 Worktree
	logger.Infof("获取 Worktree...")
	worktree, err := m.repo.Worktree()
	if err != nil {
		logger.Errorf("获取 Worktree 失败: %v", err)
		return false, err
	}

	// 检查是否有更改
	logger.Infof("检查是否有更改...")
	status, err := worktree.Status()
	if err != nil {
		logger.Errorf("获取仓库状态失败: %v", err)
		return false, err
	}

	logger.Infof("仓库是否干净: %v", status.IsClean())

	if status.IsClean() {
		logger.Infof("没有需要提交的更改")
		return false, nil
	}

	logger.Infof("检测到更改:")
	for file, fileStatus := range status {
		staging := fileStatus.Staging
		worktree := fileStatus.Worktree
		logger.Infof("  %s: Staging=%v, Worktree=%v", file, staging, worktree)
	}

	// 添加所有更改
	logger.Infof("添加所有更改到暂存区...")
	if _, err := worktree.Add("."); err != nil {
		logger.Errorf("添加文件到暂存区失败: %v", err)
		return false, fmt.Errorf("failed to add files: %w", err)
	}
	logger.Infof("文件已添加到暂存区")

	// 构建提交信息
	commitMessage := message
	if commitMessage == "" {
		commitMessage = fmt.Sprintf("%s %s", m.commitMessagePrefix, "Update")
	}

	logger.Infof("提交信息: %s", commitMessage)

	// 提交
	logger.Infof("执行 Git 提交...")
	commitHash, err := worktree.Commit(commitMessage, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Beancount AutoUpdate",
			Email: "autoupdate@beancount.local",
			When:  time.Now(),
		},
	})

	if err != nil {
		logger.Errorf("Git 提交失败: %v", err)
		return false, fmt.Errorf("failed to commit: %w", err)
	}

	logger.Infof("Git 提交成功: %s - %s", commitHash.String()[:7], commitMessage)
	return true, nil
}

// setupRemoteAndPush 设置远程仓库并推送
func (m *Manager) setupRemoteAndPush() error {
	// 检查是否已有远程仓库
	remote, err := m.repo.Remote("origin")
	if err != nil {
		// 添加远程仓库
		_, err = m.repo.CreateRemote(&config.RemoteConfig{
			Name: "origin",
			URLs: []string{m.repoURL},
		})
		if err != nil {
			return fmt.Errorf("failed to create remote: %w", err)
		}
		remote, err = m.repo.Remote("origin")
		if err != nil {
			return err
		}
	}

	// 获取当前分支
	ref, err := m.repo.Head()
	if err != nil {
		return err
	}

	branchName := ref.Name().Short()

	// 设置上游分支并推送
	pushOptions := &git.PushOptions{
		RemoteName: "origin",
		Auth:       m.getAuth(),
		RefSpecs: []config.RefSpec{
			config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", branchName, branchName)),
		},
	}

	if err := remote.Push(pushOptions); err != nil {
		return fmt.Errorf("failed to push: %w", err)
	}

	return nil
}

// PushChanges 推送更改到远程仓库
func (m *Manager) PushChanges() (bool, error) {
	logger.Infof("开始执行 Git 推送...")
	logger.Infof("仓库路径: %s", m.repoPath)

	// 检查是否自动推送
	if !m.autoPush {
		logger.Infof("自动推送已禁用")
		return false, nil
	}

	// 检查是否有远程仓库
	if m.repo == nil {
		logger.Errorf("仓库未初始化")
		return false, fmt.Errorf("repository not initialized")
	}

	remotes, err := m.repo.Remotes()
	if err != nil {
		logger.Errorf("获取远程仓库失败: %v", err)
		return false, err
	}

	if len(remotes) == 0 {
		logger.Infof("没有配置远程仓库")
		return false, nil
	}

	// 获取远程仓库
	remote := remotes[0]
	logger.Infof("远程仓库: %s - %s", remote.Config().Name, remote.Config().URLs)

	// 先拉取以避免冲突
	logger.Infof("先拉取远程更改以避免冲突...")
	pullSuccess, pullErr := m.PullChanges()
	if pullErr != nil {
		logger.Errorf("拉取远程更改失败: %v", pullErr)
		// 继续尝试推送
	} else if pullSuccess {
		logger.Infof("拉取远程更改成功")
	} else {
		logger.Infof("没有远程更改需要拉取")
	}

	// 获取当前分支
	head, err := m.repo.Head()
	if err != nil {
		logger.Errorf("获取当前分支失败: %v", err)
		return false, err
	}

	branchName := strings.TrimPrefix(head.Name().String(), "refs/heads/")
	logger.Infof("当前分支: %s", branchName)

	// 推送更改
	logger.Infof("执行 Git 推送...")
	pushOptions := &git.PushOptions{
		RemoteName: "origin",
		Auth:       m.getAuth(),
		RefSpecs: []config.RefSpec{
			config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", branchName, branchName)),
		},
	}

	logger.Infof("推送配置: RemoteName=%s, RefSpec=%v", pushOptions.RemoteName, pushOptions.RefSpecs)

	if err := remote.Push(pushOptions); err != nil {
		logger.Errorf("Git 推送失败: %v", err)
		return false, fmt.Errorf("failed to push: %w", err)
	}

	logger.Infof("Git 推送成功")
	return true, nil
}

// PullChanges 拉取远程更改
func (m *Manager) PullChanges() (bool, error) {
	if m.repo == nil {
		return false, nil
	}

	// 检查是否有远程仓库
	if _, err := m.repo.Remote("origin"); err != nil {
		return false, nil
	}

	// 获取当前分支
	ref, err := m.repo.Head()
	if err != nil {
		return false, err
	}

	branchName := ref.Name().Short()

	// 拉取更改
	pullOptions := &git.PullOptions{
		RemoteName:    "origin",
		ReferenceName: plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", branchName)),
		Auth:          m.getAuth(),
	}

	worktree, err := m.repo.Worktree()
	if err != nil {
		return false, err
	}

	if err := worktree.Pull(pullOptions); err != nil {
		// 没有更新或错误
		if err == git.NoErrAlreadyUpToDate {
			logger.Infof("远程仓库已是最新")
			return false, nil
		}
		logger.Errorf("拉取失败: %v", err)
		return false, err
	}

	logger.Infof("拉取成功")
	return true, nil
}

// GetStatus 获取仓库状态
func (m *Manager) GetStatus() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.repo == nil {
		return "No repository", nil
	}

	worktree, err := m.repo.Worktree()
	if err != nil {
		return "", err
	}

	status, err := worktree.Status()
	if err != nil {
		return "", err
	}

	// 获取当前分支
	ref, err := m.repo.Head()
	if err != nil {
		return "", err
	}

	branchName := ref.Name().Short()

	result := fmt.Sprintf("Branch: %s\n", branchName)
	result += fmt.Sprintf("Clean: %v\n", status.IsClean())

	if !status.IsClean() {
		result += "Changed files:\n"
		for file, fileStatus := range status {
			result += fmt.Sprintf("  %s: %s\n", file, fileStatus.Staging)
		}
	}

	return result, nil
}

// AddRemote 添加远程仓库
func (m *Manager) AddRemote(name, url string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.repo == nil {
		return fmt.Errorf("no repository")
	}

	_, err := m.repo.CreateRemote(&config.RemoteConfig{
		Name: name,
		URLs: []string{url},
	})

	return err
}

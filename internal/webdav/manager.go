package webdav

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Manager WebDAV 管理器
type Manager struct {
	client    *http.Client
	url       string
	username  string
	password  string
	verifySSL bool
	mu        sync.Mutex
}

// NewManager 创建 WebDAV 管理器
func NewManager(url, username, password string, verifySSL bool) (*Manager, error) {
	// 创建自定义 HTTP 客户端
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: !verifySSL,
		},
	}

	client := &http.Client{
		Timeout:   60 * time.Second,
		Transport: transport,
	}

	m := &Manager{
		client:    client,
		url:       strings.TrimRight(url, "/"),
		username:  username,
		password:  password,
		verifySSL: verifySSL,
	}

	return m, nil
}

// TestConnection 测试连接
func (m *Manager) TestConnection() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 尝试读取根目录
	req, err := m.createRequest("PROPFIND", "/", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Depth", "0")

	resp, err := m.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 207 || resp.StatusCode == 200 {
		return true, nil
	}

	return false, fmt.Errorf("connection test failed with status %d", resp.StatusCode)
}

// UploadFile 上传文件
func (m *Manager) UploadFile(localPath, remotePath, filenameTemplate string, date time.Time, orderID string) (string, error) {
	// 确保目录存在
	if err := m.ensureDirectory(remotePath); err != nil {
		return "", fmt.Errorf("failed to ensure directory: %w", err)
	}

	// 生成文件名
	filename := m.GenerateFilename(filenameTemplate, date, orderID)

	// 构建完整的远程路径
	remoteFilePath := path.Join(remotePath, filename)

	// 读取本地文件
	data, err := os.ReadFile(localPath)
	if err != nil {
		return "", fmt.Errorf("failed to read local file: %w", err)
	}

	// 上传文件
	if err := m.uploadBytes(data, remoteFilePath); err != nil {
		return "", fmt.Errorf("failed to upload file: %w", err)
	}

	// 构建完整的 URL
	remoteURL := m.buildURL(remoteFilePath)

	return remoteURL, nil
}

// UploadFileFromBytes 从字节数组上传文件
func (m *Manager) UploadFileFromBytes(data []byte, remotePath, filenameTemplate string, date time.Time, orderID string) (string, error) {
	// 确保目录存在
	if err := m.ensureDirectory(remotePath); err != nil {
		return "", fmt.Errorf("failed to ensure directory: %w", err)
	}

	// 生成文件名
	filename := m.GenerateFilename(filenameTemplate, date, orderID)

	// 构建完整的远程路径
	remoteFilePath := path.Join(remotePath, filename)

	// 上传文件
	if err := m.uploadBytes(data, remoteFilePath); err != nil {
		return "", fmt.Errorf("failed to upload file: %w", err)
	}

	// 构建完整的 URL
	remoteURL := m.buildURL(remoteFilePath)

	return remoteURL, nil
}

// uploadBytes 上传字节数组
func (m *Manager) uploadBytes(data []byte, remotePath string) error {
	req, err := m.createRequest("PUT", remotePath, bytes.NewReader(data))
	if err != nil {
		return err
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 201 && resp.StatusCode != 204 {
		return fmt.Errorf("upload failed with status %d", resp.StatusCode)
	}

	return nil
}

// GenerateFilename 根据模板生成文件名
func (m *Manager) GenerateFilename(template string, date time.Time, orderID string) string {
	// 使用交易时间，如果没有则使用当前时间
	transactionTime := date
	if transactionTime.IsZero() {
		transactionTime = time.Now()
	}

	// 准备替换变量
	variables := map[string]string{
		"date":     transactionTime.Format("2006-01-02"),
		"datetime": transactionTime.Format("20060102_150405"),
		"uuid":     uuid.New().String()[:8],
		"order_id": orderID,
	}

	// 替换模板中的变量
	filename := template
	for key, value := range variables {
		filename = strings.ReplaceAll(filename, "{"+key+"}", value)
	}

	return filename
}

// buildURL 构建完整的 WebDAV URL
func (m *Manager) buildURL(remotePath string) string {
	remotePath = strings.TrimLeft(remotePath, "/")
	if remotePath == "" {
		return m.url
	}
	return m.url + "/" + remotePath
}

// ensureDirectory 确保目录存在
func (m *Manager) ensureDirectory(directoryPath string) error {
	if directoryPath == "" || directoryPath == "/" {
		return nil
	}

	// 逐级创建目录
	parts := strings.Split(strings.Trim(directoryPath, "/"), "/")
	currentPath := ""

	for _, part := range parts {
		if currentPath == "" {
			currentPath = part
		} else {
			currentPath = currentPath + "/" + part
		}

		// 尝试创建目录
		if err := m.createDirectory(currentPath); err != nil {
			// 目录可能已存在，忽略错误
			if !strings.Contains(err.Error(), "405") && !strings.Contains(err.Error(), "exists") {
				return fmt.Errorf("failed to create directory %s: %w", currentPath, err)
			}
		}
	}

	return nil
}

// createDirectory 创建目录
func (m *Manager) createDirectory(directoryPath string) error {
	req, err := m.createRequest("MKCOL", directoryPath, nil)
	if err != nil {
		return err
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 && resp.StatusCode != 405 {
		return fmt.Errorf("failed to create directory with status %d", resp.StatusCode)
	}

	return nil
}

// MoveFile 移动/重命名文件
func (m *Manager) MoveFile(sourcePath, destinationPath string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sourcePath = strings.TrimLeft(sourcePath, "/")
	destinationPath = strings.TrimLeft(destinationPath, "/")

	// 检查源文件是否存在
	if exists, err := m.fileExists(sourcePath); err != nil || !exists {
		return false, fmt.Errorf("source file not found")
	}

	// 移动文件
	req, err := m.createRequest("MOVE", sourcePath, nil)
	if err != nil {
		return false, err
	}

	req.Header.Set("Destination", m.buildURL(destinationPath))
	req.Header.Set("Overwrite", "F")

	resp, err := m.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 && resp.StatusCode != 204 {
		return false, fmt.Errorf("failed to move file with status %d", resp.StatusCode)
	}

	return true, nil
}

// DeleteFile 删除文件
func (m *Manager) DeleteFile(filePath string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	filePath = strings.TrimLeft(filePath, "/")

	// 删除文件
	req, err := m.createRequest("DELETE", filePath, nil)
	if err != nil {
		return false, err
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		return false, fmt.Errorf("failed to delete file with status %d", resp.StatusCode)
	}

	return true, nil
}

// fileExists 检查文件是否存在
func (m *Manager) fileExists(filePath string) (bool, error) {
	req, err := m.createRequest("HEAD", filePath, nil)
	if err != nil {
		return false, err
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	return resp.StatusCode == 200, nil
}

// createRequest 创建 HTTP 请求
func (m *Manager) createRequest(method, path string, body io.Reader) (*http.Request, error) {
	url := m.buildURL(path)

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}

	// 设置基本认证
	if m.username != "" && m.password != "" {
		req.SetBasicAuth(m.username, m.password)
	}

	return req, nil
}

// generateUUID 生成 UUID
func generateUUID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		// 如果随机数生成失败，使用时间戳作为后备
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}

	b[6] = (b[6] & 0x0F) | 0x40 // Version 4
	b[8] = (b[8] & 0x3F) | 0x80 // Variant 10

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// WebDAVMultiStatus WebDAV 响应结构
type WebDAVMultiStatus struct {
	XMLName   xml.Name         `xml:"multistatus"`
	Responses []WebDAVResponse `xml:"response"`
}

// WebDAVResponse WebDAV 响应项
type WebDAVResponse struct {
	Href     string `xml:"href"`
	PropStat struct {
		Prop struct {
			ResourceType struct {
				Collection []string `xml:"collection"`
			} `xml:"resourcetype"`
		} `xml:"prop"`
		Status string `xml:"status"`
	} `xml:"propstat"`
}

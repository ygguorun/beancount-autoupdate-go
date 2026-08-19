package httpingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"beancount-autoupdate/internal/config"
	"beancount-autoupdate/internal/logger"
	"beancount-autoupdate/internal/telegram"
)

type Server struct {
	cfg    config.HTTPServerConfig
	bot    *telegram.Bot
	server *http.Server
}

func NewServer(cfg config.HTTPServerConfig, bot *telegram.Bot) *Server {
	s := &Server{cfg: cfg, bot: bot}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/receipts", s.handleUploadReceipt)

	s.server = &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  time.Duration(cfg.ReadTimeoutSec) * time.Second,
		WriteTimeout: time.Duration(cfg.WriteTimeoutSec) * time.Second,
	}

	return s
}

func (s *Server) Run() error {
	logger.Infof("HTTP 上传服务启动: listen=%s", s.cfg.ListenAddr)
	err := s.server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server listen failed: %w", err)
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	logger.Info("HTTP 上传服务关闭中...")
	if err := s.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("http server shutdown failed: %w", err)
	}
	return nil
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleUploadReceipt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	maxBytes := s.cfg.MaxUploadSizeMB * 1024 * 1024
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		logger.Warnf("HTTP 上传解析失败: %v", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid multipart form"})
		return
	}
	defer func() {
		if err := r.MultipartForm.RemoveAll(); err != nil {
			logger.Warnf("清理 multipart 临时文件失败: %v", err)
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing file field"})
		return
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			logger.Errorf("关闭上传文件失败: %v", closeErr)
		}
	}()

	sniff := make([]byte, 512)
	n, readErr := io.ReadFull(file, sniff)
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read upload"})
		return
	}

	contentType := http.DetectContentType(sniff[:n])
	ext, ok := imageExtensionByContentType(contentType)
	if !ok {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "only image files are supported"})
		return
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to prepare upload"})
		return
	}

	output, err := os.CreateTemp(os.TempDir(), "beancount-http-*"+ext)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save upload"})
		return
	}
	tempFilePath := output.Name()

	if _, err := io.Copy(output, file); err != nil {
		if closeErr := output.Close(); closeErr != nil {
			logger.Errorf("关闭临时文件失败: %v", closeErr)
		}
		if removeErr := os.Remove(tempFilePath); removeErr != nil {
			logger.Errorf("清理临时文件失败: %v", removeErr)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist upload"})
		return
	}

	if err := output.Close(); err != nil {
		if removeErr := os.Remove(tempFilePath); removeErr != nil {
			logger.Errorf("清理临时文件失败: %v", removeErr)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to finalize upload"})
		return
	}

	requestID := fmt.Sprintf("req_%d", time.Now().UnixNano())
	logger.Infof("收到 HTTP 上传: requestID=%s, filename=%s, contentType=%s, targetUserID=%d",
		requestID, sanitizeFilename(header.Filename), contentType, s.cfg.TargetUserID)

	go func() {
		s.bot.ProcessExternalImage(s.cfg.TargetUserID, tempFilePath, ext)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"request_id": requestID,
		"status":     "accepted",
	})
}

func imageExtensionByContentType(contentType string) (string, bool) {
	switch strings.ToLower(contentType) {
	case "image/jpeg":
		return ".jpg", true
	case "image/png":
		return ".png", true
	case "image/gif":
		return ".gif", true
	case "image/webp":
		return ".webp", true
	default:
		return "", false
	}
}

func sanitizeFilename(name string) string {
	if name == "" {
		return "unknown"
	}
	return strings.ReplaceAll(name, "\n", "")
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logger.Errorf("写入 JSON 响应失败: %v", err)
	}
}

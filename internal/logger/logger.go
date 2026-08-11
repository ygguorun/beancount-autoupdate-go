package logger

import (
	"io"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

var log *logrus.Logger

// Init 初始化日志系统
func Init(logDir, logFile, level string, maxBytes int64, backupCount int) error {
	log = logrus.New()

	// 设置日志级别
	logLevel, err := logrus.ParseLevel(level)
	if err != nil {
		logLevel = logrus.InfoLevel
	}
	log.SetLevel(logLevel)

	// 设置日志格式
	log.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})

	// 确保日志目录存在
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}

	// 配置日志轮转
	logPath := filepath.Join(logDir, logFile)
	fileWriter := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    int(maxBytes / (1024 * 1024)), // 转换为 MB
		MaxBackups: backupCount,
		MaxAge:     30, // 保留 30 天
		Compress:   true,
	}

	// 同时输出到文件和标准输出
	multiWriter := io.MultiWriter(os.Stdout, fileWriter)
	log.SetOutput(multiWriter)

	return nil
}

// GetLogger 获取日志实例
func GetLogger() *logrus.Logger {
	if log == nil {
		log = logrus.New()
		log.SetLevel(logrus.InfoLevel)
		log.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02 15:04:05",
		})
	}
	return log
}

// WithField 创建带字段的日志条目
func WithField(key string, value any) *logrus.Entry {
	return GetLogger().WithField(key, value)
}

// WithFields 创建带多个字段的日志条目
func WithFields(fields logrus.Fields) *logrus.Entry {
	return GetLogger().WithFields(fields)
}

// Info 记录信息日志
func Info(args ...any) {
	GetLogger().Info(args...)
}

// Infof 记录格式化信息日志
func Infof(format string, args ...any) {
	GetLogger().Infof(format, args...)
}

// Debug 记录调试日志
func Debug(args ...any) {
	GetLogger().Debug(args...)
}

// Debugf 记录格式化调试日志
func Debugf(format string, args ...any) {
	GetLogger().Debugf(format, args...)
}

// Warn 记录警告日志
func Warn(args ...any) {
	GetLogger().Warn(args...)
}

// Warnf 记录格式化警告日志
func Warnf(format string, args ...any) {
	GetLogger().Warnf(format, args...)
}

// Error 记录错误日志
func Error(args ...any) {
	GetLogger().Error(args...)
}

// Errorf 记录格式化错误日志
func Errorf(format string, args ...any) {
	GetLogger().Errorf(format, args...)
}

// Fatal 记录致命错误日志并退出
func Fatal(args ...any) {
	GetLogger().Fatal(args...)
}

// Fatalf 记录格式化致命错误日志并退出
func Fatalf(format string, args ...any) {
	GetLogger().Fatalf(format, args...)
}

// WithError 创建带错误的日志条目
func WithError(err error) *logrus.Entry {
	return GetLogger().WithError(err)
}

// SetLevel 设置日志级别
func SetLevel(level string) {
	logLevel, err := logrus.ParseLevel(level)
	if err != nil {
		logLevel = logrus.InfoLevel
	}
	GetLogger().SetLevel(logLevel)
}

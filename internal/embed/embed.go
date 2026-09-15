// Package embed 提供嵌入的模板文件和默认配置
package embed

import (
	"embed"
	"fmt"
)

//go:embed templates/*
var templatesFS embed.FS

// GetTemplate 获取模板文件内容
func GetTemplate(name string) (string, error) {
	data, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", name, err)
	}
	return string(data), nil
}

// InitTemplates 启动时校验所有嵌入模板可读取（embed 数据本就在内存，无需缓存）
func InitTemplates() error {
	entries, err := templatesFS.ReadDir("templates")
	if err != nil {
		return fmt.Errorf("failed to read embedded templates: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, err := GetTemplate(entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

// Package embed 提供嵌入的模板文件和默认配置
package embed

import (
	"embed"
	"fmt"
)

//go:embed templates/*
var templatesFS embed.FS

// TemplateFiles 嵌入的模板文件
var TemplateFiles = map[string]string{
	"init.bean":                        "",
	"assets.bean":                      "",
	"equity.bean":                      "",
	"expenses.bean":                    "",
	"income.bean":                      "",
	"liabilities.bean":                 "",
	"receipt_image_recognition.txt":    "",
	"analysis_agent_system_prompt.txt": "",
}

// GetTemplate 获取模板文件内容
func GetTemplate(name string) (string, error) {
	if content, ok := TemplateFiles[name]; ok && content != "" {
		return content, nil
	}

	// 从 embed.FS 读取
	data, err := templatesFS.ReadFile(fmt.Sprintf("templates/%s", name))
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", name, err)
	}

	content := string(data)
	TemplateFiles[name] = content // 缓存
	return content, nil
}

// GetTemplateOrPanic 获取模板文件内容，如果失败则 panic
func GetTemplateOrPanic(name string) string {
	content, err := GetTemplate(name)
	if err != nil {
		panic(err)
	}
	return content
}

// InitTemplates 初始化所有模板（在程序启动时调用）
func InitTemplates() error {
	templateNames := []string{
		"init.bean",
		"assets.bean",
		"equity.bean",
		"expenses.bean",
		"income.bean",
		"liabilities.bean",
		"receipt_image_recognition.txt",
		"analysis_agent_system_prompt.txt",
	}

	for _, name := range templateNames {
		if _, err := GetTemplate(name); err != nil {
			return fmt.Errorf("failed to load template %s: %w", name, err)
		}
	}

	return nil
}

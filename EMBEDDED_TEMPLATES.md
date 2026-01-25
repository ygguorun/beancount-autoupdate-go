# 嵌入模板说明

## 概述

本项目使用 Go 的 `embed` 功能将所有模板文件和默认配置直接嵌入到编译后的可执行文件中，无需外部模板文件。

## 嵌入的文件

以下文件已嵌入到程序中：

### Beancount 账户模板
- `assets.bean` - 资产账户模板
- `equity.bean` - 权益账户模板
- `expenses.bean` - 支出账户模板
- `income.bean` - 收入账户模板
- `liabilities.bean` - 负债账户模板

### LLM 提示词模板
- `receipt_image_recognition.txt` - 账单图片识别提示词模板

## 使用方式

### 1. 开发环境

在开发环境中，模板文件位于 `internal/embed/templates/` 目录：

```
go/internal/embed/templates/
├── assets.bean
├── equity.bean
├── expenses.bean
├── income.bean
├── liabilities.bean
└── receipt_image_recognition.txt
```

修改这些文件后，重新编译即可更新嵌入的模板。

### 2. 生产环境

编译后的可执行文件已经包含了所有模板，无需额外的模板文件。

```bash
# 编译
make build

# 运行（无需模板文件）
./build/beancount-autoupdate
```

### 3. 自定义模板

如果需要使用自定义模板，可以：

#### 方法 1: 修改源代码中的模板文件
1. 编辑 `internal/embed/templates/` 目录下的模板文件
2. 重新编译程序

#### 方法 2: 使用外部模板文件（需要修改代码）
修改 `internal/embed/embed.go`，添加从外部文件读取的逻辑。

## 测试嵌入的模板

运行测试命令验证嵌入的模板：

```bash
make test-embed
```

输出示例：
```
测试嵌入的模板...
✅ 模板初始化成功
✅ assets.bean: 4880 字符
✅ equity.bean: 244 字符
✅ expenses.bean: 2616 字符
✅ income.bean: 2454 字符
✅ liabilities.bean: 5458 字符
✅ receipt_image_recognition.txt: 6100 字符

所有模板测试完成！
```

## 优势

1. **单一可执行文件** - 无需携带额外的模板文件
2. **简化部署** - 只需复制一个可执行文件
3. **版本控制** - 模板版本与程序版本一致
4. **防止丢失** - 模板不会丢失或被误删

## 技术细节

使用 Go 1.16+ 的 `embed` 包：

```go
//go:embed templates/*
var templatesFS embed.FS
```

编译时，所有模板文件被打包到可执行文件中，运行时通过 `embed.FS` 读取。
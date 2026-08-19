# Makefile for Beancount AutoUpdate - Go Version

# 变量定义
APP_NAME=beancount-autoupdate
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u '+%Y-%m-%d_%H:%M:%S')
NEXT_VERSION=$(shell svu next 2>/dev/null || echo "0.0.1")
LDFLAGS=-ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)"

# 平台检测 - Windows 需要 .exe 后缀
ifeq ($(OS),Windows_NT)
    BINARY=$(APP_NAME).exe
else
    BINARY=$(APP_NAME)
endif

# Go 相关变量
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GORUN=$(GOCMD) run

# 目录变量
CMD_DIR=./cmd
BUILD_DIR=./build
DIST_DIR=./dist

# 平台相关
PLATFORMS=darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64

# 默认目标
.PHONY: all
all: clean deps build

# 下载依赖
.PHONY: deps
deps:
	@echo "下载依赖..."
	$(GOMOD) download
	$(GOMOD) tidy

# 运行
.PHONY: run
run:
	@echo "运行程序..."
	$(GORUN) $(CMD_DIR)/main.go

# 构建
.PHONY: build
build:
	@echo "构建程序..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) $(CMD_DIR)/main.go

# 构建所有平台
.PHONY: build-all
build-all:
	@echo "构建所有平台..."
	@mkdir -p $(DIST_DIR)
	@$(foreach platform,$(PLATFORMS), \
		echo "构建 $(platform)..."; \
		GOOS=$(word 1,$(subst /, ,$(platform))) \
		GOARCH=$(word 2,$(subst /, ,$(platform))) \
		$(GOBUILD) $(LDFLAGS) -o $(DIST_DIR)/$(APP_NAME)-$(subst /,-,$(platform))$(if $(findstring windows,$(platform)),.exe,) $(CMD_DIR)/main.go || exit $$?; \
	)

# 测试
.PHONY: test
test:
	@echo "运行测试..."
	$(GOTEST) -v ./...

# 测试覆盖率
.PHONY: test-coverage
test-coverage:
	@echo "运行测试覆盖率..."
	$(GOTEST) -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@echo "覆盖率报告已生成: coverage.html"

# 代码检查
.PHONY: lint
lint:
	@echo "运行代码检查..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint 未安装，跳过代码检查"; \
	fi

# 格式化代码
.PHONY: fmt
fmt:
	@echo "格式化代码..."
	$(GOCMD) fmt ./...

# 清理
.PHONY: clean
clean:
	@echo "清理构建文件..."
	$(GOCLEAN)
	@rm -rf $(BUILD_DIR)
	@rm -rf $(DIST_DIR)
	@rm -f coverage.out coverage.html

# 安装
.PHONY: install
install: build
	@echo "安装程序..."
	@cp $(BUILD_DIR)/$(APP_NAME) /usr/local/bin/
	@echo "程序已安装到 /usr/local/bin/$(APP_NAME)"

# 卸载
.PHONY: uninstall
uninstall:
	@echo "卸载程序..."
	@rm -f /usr/local/bin/$(APP_NAME)
	@echo "程序已卸载"

# Docker 构建
.PHONY: docker-build
docker-build:
	@echo "构建 Docker 镜像..."
	docker build -t $(APP_NAME):$(VERSION) .
	docker tag $(APP_NAME):$(VERSION) $(APP_NAME):latest

# Docker 运行
.PHONY: docker-run
docker-run:
	@echo "运行 Docker 容器..."
	docker run -d --name $(APP_NAME) --restart unless-stopped \
		-v $(PWD)/config.toml:/app/config.toml \
		-v $(PWD)/.env:/app/.env \
		-v $(PWD)/beancount-data:/app/beancount-data \
		-v $(PWD)/logs:/app/logs \
		$(APP_NAME):latest

# Docker 停止
.PHONY: docker-stop
docker-stop:
	@echo "停止 Docker 容器..."
	docker stop $(APP_NAME) || true
	docker rm $(APP_NAME) || true

# 部署（示例）
.PHONY: deploy
deploy: build
	@echo "部署程序..."
	@echo "请根据实际情况修改部署脚本"
	# scp $(BUILD_DIR)/$(APP_NAME) user@server:/path/to/deploy/

# 帮助
.PHONY: help
help:
	@echo "可用命令:"
	@echo "  make all          - 清理、下载依赖并构建"
	@echo "  make deps         - 下载依赖"
	@echo "  make run          - 运行程序"
	@echo "  make build        - 构建程序"
	@echo "  make build-all    - 构建所有平台"
	@echo "  make test         - 运行测试"
	@echo "  make test-coverage - 运行测试覆盖率"
	@echo "  make lint         - 运行代码检查"
	@echo "  make fmt          - 格式化代码"
	@echo "  make clean        - 清理构建文件"
	@echo "  make install      - 安装程序"
	@echo "  make uninstall    - 卸载程序"
	@echo "  make docker-build - 构建 Docker 镜像"
	@echo "  make docker-run   - 运行 Docker 容器"
	@echo "  make docker-stop  - 停止 Docker 容器"
	@echo "  make deploy       - 部署程序"
	@echo "  make help         - 显示帮助信息"
	@echo ""
	@echo "版本管理:"
	@echo "  make version      - 显示当前版本"
	@echo "  make version-next - 显示下一个版本号"
	@echo "  make tag-major    - 创建主版本标签 (x.0.0)"
	@echo "  make tag-minor    - 创建次版本标签 (x.y.0)"
	@echo "  make tag-patch    - 创建修订版本标签 (x.y.z)"
	@echo "  make tag          - 创建标签并推送到远程"
	@echo "  make release      - 执行完整发布流程"

# 显示当前版本
.PHONY: version
version:
	@echo "当前版本: $(VERSION)"

# 显示下一个版本号
.PHONY: version-next
version-next:
	@echo "下一个版本: $(NEXT_VERSION)"

# 创建主版本标签
.PHONY: tag-major
tag-major:
	@echo "创建主版本标签..."
	@NEW_VERSION=$$(svu major) && \
	git tag -a "$$NEW_VERSION" -m "Release $$NEW_VERSION" && \
	echo "已创建标签 $$NEW_VERSION"

# 创建次版本标签
.PHONY: tag-minor
tag-minor:
	@echo "创建次版本标签..."
	@NEW_VERSION=$$(svu minor) && \
	git tag -a "$$NEW_VERSION" -m "Release $$NEW_VERSION" && \
	echo "已创建标签 $$NEW_VERSION"

# 创建修订版本标签
.PHONY: tag-patch
tag-patch:
	@echo "创建修订版本标签..."
	@NEW_VERSION=$$(svu patch) && \
	git tag -a "$$NEW_VERSION" -m "Release $$NEW_VERSION" && \
	echo "已创建标签 $$NEW_VERSION"

# 创建标签并推送
.PHONY: tag
tag:
	@echo "创建标签并推送..."
	@NEW_VERSION=$$(svu next) && \
	git tag -a "$$NEW_VERSION" -m "Release $$NEW_VERSION" && \
	git push origin "$$NEW_VERSION" && \
	echo "已创建并推送标签 $$NEW_VERSION"

# 执行完整发布流程
.PHONY: release
release:
	@echo "执行发布流程..."
	@echo "1. 构建所有平台..."
	@$(MAKE) build-all
	@echo "2. 创建标签并推送..."
	@$(MAKE) tag
	@echo "发布完成！"

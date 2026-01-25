# 多阶段构建 Dockerfile
# 第一阶段：构建
FROM golang:1.23-alpine AS builder

# 安装必要的工具
RUN apk add --no-cache git ca-certificates tzdata

# 设置工作目录
WORKDIR /build

# 复制 go.mod 和 go.sum
COPY go.mod go.sum ./

# 下载依赖
RUN go mod download

# 复制源代码
COPY . .

# 构建应用
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o beancount-autoupdate ./cmd/main.go

# 第二阶段：运行
FROM alpine:latest

# 安装 ca-certificates 和 tzdata
RUN apk --no-cache add ca-certificates tzdata git

# 设置时区
ENV TZ=Asia/Shanghai

# 创建非 root 用户
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser

# 设置工作目录
WORKDIR /app

# 从构建阶段复制二进制文件
COPY --from=builder /build/beancount-autoupdate .

# 复制配置文件模板
COPY config.toml.example config.toml.example
COPY .env.example .env.example

# 创建必要的目录
RUN mkdir -p beancount-data logs && \
    chown -R appuser:appuser /app

# 切换到非 root 用户
USER appuser

# 暴露端口（如果需要）
# EXPOSE 8080

# 运行应用
CMD ["./beancount-autoupdate"]
# 多阶段构建：编译 Go 二进制
FROM golang:1.22-alpine AS builder

WORKDIR /build

# 先复制依赖文件，利用 Docker 缓存
COPY go.mod go.sum ./
RUN go mod download

# 复制源码
COPY . .

# 编译（CGO 禁用，交叉编译方便）
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o strmhub .

# 运行阶段：最小镜像
FROM alpine:latest

# 安装 ca-certificates（TLS 证书）和 时区数据
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# 复制编译好的二进制
COPY --from=builder /build/strmhub .
# 复制前端静态资源
COPY --from=builder /build/web ./web

EXPOSE 6060 6086

VOLUME ["/config", "/data", "/media", "/logs"]

ENTRYPOINT ["./strmhub"]

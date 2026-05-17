# 1
# 使用官方的 Go 镜像作为编译环境
FROM golang:1.26-alpine AS builder

# 设置环境变量
ENV GO111MODULE=on \
    GOPROXY=http://goproxy.cn,direct

# 设置工作目录
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0: 禁用 CGO，确保编译出静态链接的二进制文件，方便在 alpine 中运行
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o aigateway ./05-high-availability-cluster


# 2
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

ENV TZ=Asia/Shanghai

WORKDIR /app

COPY --from=builder /app/aigateway .

EXPOSE 8080

CMD [ "./aigateway" ]

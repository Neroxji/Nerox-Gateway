FROM golang:1.26-alpine AS builder

ENV GO111MODULE=on \
    GOPROXY=http://goproxy.cn,direct

WORKDIR /src

COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/fakeprovider .

FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

ENV TZ=Asia/Shanghai

WORKDIR /app
COPY --from=builder /out/fakeprovider ./fakeprovider

EXPOSE 8081
CMD ["./fakeprovider"]

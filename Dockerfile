FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY go.mod ./
RUN go mod download || true

COPY . .

# 纯 Go 编译，完全不需要 CGO，跨平台编译极度稳定
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o nasdrop-server ./cmd/server

FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app

COPY --from=builder /app/nasdrop-server .

EXPOSE 8881
VOLUME ["/data"]

ENV PORT=8881
ENV DATA_DIR=/data
ENV DB_PATH=/data/nasdrop.db

ENTRYPOINT ["./nasdrop-server"]

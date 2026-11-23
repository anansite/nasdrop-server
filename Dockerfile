FROM golang:1.22-alpine AS builder

WORKDIR /app
ENV GOPROXY=https://goproxy.cn,direct

COPY go.mod ./
RUN go mod download || true

COPY . .

RUN go mod tidy
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o nasdrop-server ./cmd/server

FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app

COPY --from=builder /app/nasdrop-server .

EXPOSE 8881
VOLUME ["/data", "/config"]

ENV TZ=Asia/Shanghai
ENV PORT=8881
ENV DATA_DIR=/data
ENV DB_PATH=/config/nasdrop.db
ENV THUMB_DIR=/config/thumbnails

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8881/api/ping || exit 1

ENTRYPOINT ["./nasdrop-server"]

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

ENV PORT=8881
ENV DATA_DIR=/data
ENV DB_PATH=/config/nasdrop.db
ENV THUMB_DIR=/config/thumbnails

ENTRYPOINT ["./nasdrop-server"]

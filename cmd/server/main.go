package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anansite/nasdrop-server/internal/api"
	"github.com/anansite/nasdrop-server/internal/config"
	"github.com/anansite/nasdrop-server/internal/db"
	"github.com/anansite/nasdrop-server/internal/webdav"
)

func main() {
	cfg := config.Load()

	// 确保存储根目录存在
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		log.Fatalf("Create data dir %s failed: %v", cfg.DataDir, err)
	}

	// 初始化嵌入式 SQLite 权威数据库
	store, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("Init SQLite database failed: %v", err)
	}
	defer store.Close()

	mux := http.NewServeMux()

	// 1. 注册商业级相册 REST API (/api/...)
	apiRouter := api.NewRouter(store, cfg.DataDir, cfg.ThumbDir)
	apiRouter.Register(mux)

	// 2. 注册标准 RFC 4918 WebDAV 处理器（挂载在 / 与 /dav，联动缩略图缓存清理）
	davHandler := webdav.NewHookedHandler("", cfg.DataDir, cfg.ThumbDir, store)
	mux.Handle("/", davHandler)

	// 3. 全局鉴权与 CORS 中间件包装
	rootHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 允许跨域
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, MOVE, COPY, PROPFIND, PROPPATCH, MKCOL, LOCK, UNLOCK, OPTIONS, HEAD")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Depth, Destination, If, Overwrite, Range")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, ETag")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		// 基础认证检查（若配置了用户名密码）
		if cfg.Username != "" && cfg.Password != "" {
			u, p, ok := r.BasicAuth()
			if !ok || u != cfg.Username || p != cfg.Password {
				w.Header().Set("WWW-Authenticate", `Basic realm="NasDrop"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}

		mux.ServeHTTP(w, r)
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("==================================================")
	log.Printf("★ NasDrop Server 已启动 (生产加固版)")
	log.Printf("  - 服务监听端口: %d", cfg.Port)
	log.Printf("  - WebDAV 存储路径: %s", cfg.DataDir)
	log.Printf("  - 数据库文件: %s", cfg.DBPath)
	log.Printf("  - 标准 WebDAV 地址: http://127.0.0.1:%d/", cfg.Port)
	log.Printf("  - 商业级相册 API: http://127.0.0.1:%d/api/timeline", cfg.Port)
	if cfg.Username == "" || cfg.Password == "" {
		log.Printf("  ⚠️  [安全警告] 未配置 NASDROP_USER / NASDROP_PASS，WebDAV 与 API 处于免密状态！")
	} else {
		log.Printf("  🔒 [安全防护] 已启用 Basic Auth 鉴权防护 (用户: %s)", cfg.Username)
	}
	log.Printf("==================================================")

	srv := &http.Server{
		Addr:              addr,
		Handler:           rootHandler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server exited with error: %v", err)
		}
	}()

	<-stopChan
	log.Printf("收到停止信号，正在优雅关闭 NasDrop 服务...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("服务优雅关闭超时: %v", err)
	} else {
		log.Printf("NasDrop Server 已安全退出。")
	}
}

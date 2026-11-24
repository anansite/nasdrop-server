# nasdrop-server

极轻量级专属 NAS 服务端：**内置标准 RFC 4918 WebDAV 协议 + 商业级相册时间轴元数据引擎**。

> **核心设计理念**：
> 1. **服务不解析图片**：完全不在路由器上进行沉重的图片解码或生成缩略图，避免消耗路由器宝贵的 CPU 与内存。
> 2. **手机端算力负责**：手机上传时提取拍摄时间（EXIF）并生成微缩图，服务端通过 SQLite 进行权威持久化。
> 3. **单二进制 / 15MB 内存**：采用纯 Go 编写（无 CGO 依赖），内嵌 SQLite 数据库，一键运行。
> 4. **全生命周期跟随**：内置 WebDAV 钩子，文件重命名、移动、删除时自动与 SQLite 同步，不产生脏数据。

---

## 快速开始与部署

> **重要原则**：**严禁在路由器等边缘小主机设备上执行 `docker build` 或源码编译**。
> 本仓库已接入 GitHub Actions 自动化 CI，每次提交均会自动多架构交叉编译（linux/amd64, linux/arm64）并发布最新 Docker 镜像与独立运行二进制。路由器仅拉取预构建镜像或下载 Release 单文件运行，零性能损耗。

### 方式一：Docker / Docker Compose（推荐）

直接拉取 GitHub 官方预构建镜像运行：

```bash
docker run -d \
  --name nasdrop-server \
  --restart unless-stopped \
  -p 8881:8881 \
  -v /path/to/your/nas:/data \
  -v /path/to/config:/config \
  -e PORT=8881 \
  -e DATA_DIR=/data \
  -e DB_PATH=/config/nasdrop.db \
  -e THUMB_DIR=/config/thumbnails \
  -e NASDROP_USER=admin \
  -e NASDROP_PASS=changeme123 \
  ghcr.io/anansite/nasdrop-server:latest
```

或使用 `docker-compose.yml`：

```bash
docker compose pull
docker compose up -d
```

### 方式二：直接运行预编译独立二进制程序

从 GitHub Releases / Actions Artifacts 直接下载对应架构的二进制文件（如 `nasdrop-server-linux-amd64`）：

```bash
./nasdrop-server -port 8881 -data /path/to/your/nas -db /path/to/config/nasdrop.db -user admin -pass changeme123
```

### 运行参数与环境变量

| 参数 | 环境变量 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `-port` | `PORT` | `8881` | HTTP 与 WebDAV 服务监听端口 |
| `-data` | `DATA_DIR` | `./data` | WebDAV 挂载的本地硬盘存储目录 |
| `-db` | `DB_PATH` | `./nasdrop.db` | SQLite 数据库文件存储路径 |
| `-user` | `NASDROP_USER` | 空 | WebDAV 与 API 基础鉴权账号（可选） |
| `-pass` | `NASDROP_PASS` | 空 | WebDAV 与 API 基础鉴权密码（可选） |

---

## 核心接口说明

- **标准 WebDAV**：`http://ip:8881/` 或 `http://ip:8881/dav`
- **相册时间轴**：`GET /api/timeline?limit=100&offset=0`（按拍摄时间降序）
- **元数据同步**：`POST /api/metadata`（手机端上报拍摄时间与缩略图映射）
- **差量对齐**：`POST /api/reconcile`（清理外部删除的孤儿记录）
- **健康检测**：`GET /api/ping`

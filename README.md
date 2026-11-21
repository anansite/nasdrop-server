# nasdrop-server

极轻量级专属 NAS 服务端：**内置标准 RFC 4918 WebDAV 协议 + 商业级相册时间轴元数据引擎**。

> **核心设计理念**：
> 1. **服务不解析图片**：完全不在路由器上进行沉重的图片解码或生成缩略图，避免消耗路由器宝贵的 CPU 与内存。
> 2. **手机端算力负责**：手机上传时提取拍摄时间（EXIF）并生成微缩图，服务端通过 SQLite 进行权威持久化。
> 3. **单二进制 / 15MB 内存**：采用纯 Go 编写（无 CGO 依赖），内嵌 SQLite 数据库，一键运行。
> 4. **全生命周期跟随**：内置 WebDAV 钩子，文件重命名、移动、删除时自动与 SQLite 同步，不产生脏数据。

---

## 快速开始

### 方式一：Docker Compose 运行（推荐）

```bash
docker compose up -d
```

### 方式二：直接运行二进制程序

```bash
./nasdrop-server -port 8090 -data /mnt/storage -db /mnt/storage/nasdrop.db
```

### 运行参数与环境变量

| 参数 | 环境变量 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `-port` | `PORT` | `8090` | HTTP 与 WebDAV 服务监听端口 |
| `-data` | `DATA_DIR` | `./data` | WebDAV 挂载的本地硬盘存储目录 |
| `-db` | `DB_PATH` | `./nasdrop.db` | SQLite 数据库文件存储路径 |
| `-user` | `NASDROP_USER` | 空 | WebDAV 与 API 基础鉴权账号（可选） |
| `-pass` | `NASDROP_PASS` | 空 | WebDAV 与 API 基础鉴权密码（可选） |

---

## 核心接口说明

- **标准 WebDAV**：`http://ip:8090/` 或 `http://ip:8090/dav`
- **相册时间轴**：`GET /api/timeline?limit=100&offset=0`（按拍摄时间降序）
- **元数据同步**：`POST /api/metadata`（手机端上报拍摄时间与缩略图映射）
- **差量对齐**：`POST /api/reconcile`（清理外部删除的孤儿记录）
- **健康检测**：`GET /api/ping`

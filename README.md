# STRMhub

网盘媒体库 STRM 工具(全栈一体化):从网盘批量生成 `.strm` 文件,配合 Emby/Jellyfin 把网盘当本地媒体库,并自带 **302 直链播放**、**TMDB 刮削**、**目录整理**、**Webhook 联动**。

## ✨ 功能

- **多网盘驱动**:本地文件系统 / 115 网盘 / 123 云盘 / WebDAV(统一驱动抽象,可扩展)
- **STRM 生成**:增量补缺 / 增量更新 / 全量覆写三种模式;SafeName 路径映射;**防误删三层保护**(远端空保护 + 删除比例阈值 + 人工确认);增量 diff 快照(未变化文件直接跳过)
- **302 直链播放**:`/api/redirect` 端点 + 直链缓存 + 同目录预缓存(起播秒开);内嵌 **Emby/Jellyfin 302 反代**(PlaybackInfo 强制直链,失败自动回源)
- **刮削**:TMDB 匹配 → 电影同名 nfo / 剧集 tvshow.nfo + 海报下载 → 海报墙索引(含追更集数)
- **目录整理**:文件名解析(年份/季集/质量词)→ 计划-预览-执行三段式
- **Webhook 联动**:规则 CRUD + 动作链(`strm_scan` / `scrape` / `emby_refresh`),兼容 QAS/CloudSaver 回调与 `delayTime`
- **转存工具**:123 秒传串解析(123FSLinkV1/V2、123FLCPV1/V2、base62/md5 归一化)→ 导入规划 → 驱动秒传执行
- **安全基线**:凭据 AES-GCM 加密落盘、JWT 登录 + 失败限速、任务持久化(SQLite,重启不丢)

## 🚀 Docker Compose 部署(推荐)

### 前置条件

- 已安装 Docker 与 Docker Compose v2
- 服务器开放端口 **6060**(管理台)与 **6086**(Emby 302 反代)

### 步骤

```bash
# 方式 A(最简): 一行拉取部署 —— 不 clone 仓库, compose 文件直接引用远程
mkdir -p strmhub && cd strmhub && mkdir -p data strm
docker compose -f https://raw.githubusercontent.com/DaisyYijin/STRMhub/main/docker-compose.yml up -d
# 国内网络若 raw 不可达, 用 jsDelivr CDN 源:
# docker compose -f https://cdn.jsdelivr.net/gh/DaisyYijin/STRMhub@main/docker-compose.yml up -d

# 方式 B: 下载 compose 文件后本地运行
curl -sSL -o docker-compose.yml https://raw.githubusercontent.com/DaisyYijin/STRMhub/main/docker-compose.yml
# (国内备用: https://cdn.jsdelivr.net/gh/DaisyYijin/STRMhub@main/docker-compose.yml)
mkdir -p data strm
docker compose up -d          # 自动从 ghcr.io 拉取镜像

# 方式 C: 本地构建(修改代码/无 ghcr 镜像时)
git clone https://github.com/DaisyYijin/STRMhub.git
cd STRMhub && mkdir -p data strm
docker compose up -d --build
```

> 三种方式启动前都建议先编辑 compose 文件, 把 `STRMHUB_ADMIN_PASSWORD` 改为自己的密码
> (方式 A 可先 `curl` 下载到本地改好再 `up`)。

### 使用

| 入口 | 地址 | 说明 |
|---|---|---|
| 管理台 | `http://<服务器IP>:6060` | 登录后管理账户/任务/刮削/整理/Webhook |
| API 文档 | `http://<服务器IP>:6060/docs` | Swagger 交互式文档 |
| Emby 302 反代 | `http://<服务器IP>:6086` | **Emby 客户端改连此端口**(替代原 8096) |

### 镜像发布(自动)

仓库内置 GitHub Actions(`.github/workflows/docker-build.yml`):
- **推送 `main` 分支** → 自动构建并发布 `ghcr.io/daisyyijin/strmhub:latest`
- **推送 `v*` tag**(如 `v0.2.0`)→ 额外发布对应版本标签
- 服务器升级只需 `git pull && docker compose pull && docker compose up -d`

> 镜像由 GitHub 云端构建(无需本地 Docker),首次推送 main 后约 5 分钟镜像可用。

### 目录挂载

| 宿主机目录 | 容器内 | 说明 |
|---|---|---|
| `./data` | `/app/data` | 密钥、SQLite 数据库、凭据加密数据(**必须持久化, 备份**) |
| `./strm` | `/strm` | STRM 输出目录(本地驱动生成 + 反代读取, 可选) |

### 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `STRMHUB_ADMIN_PASSWORD` | `admin` | 管理台密码(**生产必改**) |
| `STRMHUB_DATA` | `/app/data` | 数据目录(一般无需改) |
| `EMBY_HOST` | `http://127.0.0.1:8096` | 上游 Emby/Jellyfin 地址 |
| `EMBY_API_KEY` | 空 | Emby API Key(302 反代必需) |
| `TMDB_API_KEY` | 空 | TMDB Key(刮削必需, 未配置时刮削降级为仅建索引) |

### 升级

```bash
git pull
docker compose up -d --build
```

### 备份

```bash
# 数据全部在 ./data, 直接备份该目录即可(含密钥, 丢失后凭据无法解密)
tar -czf strmhub-data-$(date +%F).tar.gz data/
```

## 🔧 本地开发

```bash
# 后端(Windows 下用 Python 3.12)
pip install -r requirements.txt
uvicorn app.main:app --port 6060        # 管理服务
# 或双服务: python -m app.launcher     # 6060 管理 + 6086 反代

# 前端(开发热更新, 代理到 6060)
cd frontend
npm install
npm run dev                              # http://localhost:5173
npm test                                 # vitest 单测
npm run build                            # 构建到 dist(后端自动托管)
```

## 🧪 测试

```bash
pytest tests/                            # 后端 115 项测试
cd frontend && npm test                  # 前端 7 项测试
```

## 🔌 真实网盘联调

| 网盘 | 凭据 | 说明 |
|---|---|---|
| 115 | Cookie | 浏览器登录 115 后复制 Cookie, 填入账户凭据 |
| 123 | 手机号:密码 | 填 `手机号:密码` |
| WebDAV | 账号:密码 | 如坚果云 `user:pass`; 需在账户配置 JSON 填 `base_url` |

> 网盘 API 为逆向协议, 随时可能失效; 驱动已内置限流(默认 2QPS)与封控冷却, 请勿调高 QPS。

## 📄 License

MIT(请按需修改)

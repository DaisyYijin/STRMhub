# STRMhub 镜像: 多阶段构建(前端 node -> 后端 python), 管理 6060 + Emby 302 反代 6086

# ---- 阶段 1: 前端构建 ----
FROM node:20-alpine AS frontend
WORKDIR /frontend
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# ---- 阶段 2: 运行时 ----
FROM python:3.12-slim
WORKDIR /app

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    STRMHUB_DATA=/app/data

# 依赖层(利用构建缓存)
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# 应用代码 + 前端产物
COPY app ./app
COPY --from=frontend /frontend/dist ./frontend/dist

# 管理端口 / Emby 302 反代端口
EXPOSE 6060 6086

# 数据目录由 docker-compose 卷挂载(./data:/app/data)
VOLUME ["/app/data"]

CMD ["python", "-m", "app.launcher"]

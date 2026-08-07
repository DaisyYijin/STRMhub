"""核心数据模型: 账户 / 任务 / 设置。

对齐蓝图数据模型:
- accounts: 凭据加密存储(credential_enc)
- tasks: 任务定义持久化 + 执行状态
- settings: 全局键值
"""
from __future__ import annotations

import datetime as dt
from datetime import UTC

from sqlalchemy import DateTime, Float, Integer, String, Text, UniqueConstraint
from sqlalchemy.orm import Mapped, mapped_column

from .session import Base


def _utcnow():
    return dt.datetime.now(UTC)


class Account(Base):
    __tablename__ = "accounts"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    driver_type: Mapped[str] = mapped_column(String(32), nullable=False)
    name: Mapped[str] = mapped_column(String(128), nullable=False, unique=True)
    credential_enc: Mapped[str] = mapped_column(Text, default="")   # AES-GCM hex
    config_json: Mapped[str] = mapped_column(Text, default="{}")    # 驱动配置(如 root)
    info_json: Mapped[str] = mapped_column(Text, default="{}")      # 账号信息(昵称/容量/头像等)
    status: Mapped[str] = mapped_column(String(16), default="ok")   # ok | error
    created_at: Mapped[dt.datetime] = mapped_column(DateTime, default=_utcnow)


class Task(Base):
    __tablename__ = "tasks"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    account_id: Mapped[int] = mapped_column(Integer, nullable=False, index=True)
    name: Mapped[str] = mapped_column(String(128), nullable=False)
    remote_path: Mapped[str] = mapped_column(Text, nullable=False)   # 源目录(file_id)
    local_output: Mapped[str] = mapped_column(Text, nullable=False)  # strm 输出根
    scan_mode: Mapped[str] = mapped_column(String(24), default="incremental_missing")
    extensions_json: Mapped[str] = mapped_column(Text, default="[]")
    base_url: Mapped[str] = mapped_column(Text, default="")
    token: Mapped[str] = mapped_column(String(64), default="")
    status: Mapped[str] = mapped_column(String(16), default="idle")  # idle|running|done|error
    last_run_at: Mapped[dt.datetime | None] = mapped_column(DateTime, nullable=True)
    last_error: Mapped[str] = mapped_column(Text, default="")
    created_at: Mapped[dt.datetime] = mapped_column(DateTime, default=_utcnow)


class Setting(Base):
    __tablename__ = "settings"

    key: Mapped[str] = mapped_column(String(64), primary_key=True)
    value: Mapped[str] = mapped_column(Text, default="")


class FileIndex(Base):
    """远端文件索引: 增量 diff 快照(参考 p115strmhelper files 表)。

    path 为相对任务 remote_path 的路径链(目录/子目录/文件名, 以 / 连接),
    每账户唯一; (size, mtime) 用于判断文件是否变化。
    """
    __tablename__ = "file_index"
    __table_args__ = (UniqueConstraint("account_id", "path", name="uq_account_path"),)

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    account_id: Mapped[int] = mapped_column(Integer, nullable=False, index=True)
    path: Mapped[str] = mapped_column(Text, nullable=False)
    file_key: Mapped[str] = mapped_column(Text, default="")
    size: Mapped[int] = mapped_column(Integer, default=0)
    mtime: Mapped[float] = mapped_column(Float, default=0.0)


class ScrapeItem(Base):
    """海报墙索引条目(每任务一条记录, 可追更)。"""
    __tablename__ = "scrape_items"
    __table_args__ = (UniqueConstraint("task_id", "title", name="uq_task_title"),)

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    task_id: Mapped[str] = mapped_column(String(64), nullable=False, index=True)
    title: Mapped[str] = mapped_column(String(256), nullable=False)
    year: Mapped[int | None] = mapped_column(Integer, nullable=True)
    media_type: Mapped[str] = mapped_column(String(16), default="movie")
    status: Mapped[str] = mapped_column(String(16), default="matched")  # matched|doubt|none
    tmdb_id: Mapped[int | None] = mapped_column(Integer, nullable=True)
    poster_rel: Mapped[str] = mapped_column(Text, default="")   # 相对 strm 目录的海报路径
    root_rel: Mapped[str] = mapped_column(Text, default="")     # 相对 strm 目录的作品根
    ep_local: Mapped[int] = mapped_column(Integer, default=0)   # 本地已有集数
    ep_tmdb: Mapped[int] = mapped_column(Integer, default=0)    # TMDB 总集数
    updated_at: Mapped[dt.datetime] = mapped_column(DateTime, default=_utcnow)


class WebhookRule(Base):
    """Webhook 联动规则: 触发 token -> 动作链。"""
    __tablename__ = "webhook_rules"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    name: Mapped[str] = mapped_column(String(128), nullable=False, unique=True)
    trigger: Mapped[str] = mapped_column(String(32), default="webhook")  # webhook|qas_strm|cs_strm
    action_chain_json: Mapped[str] = mapped_column(Text, default="[]")
    token: Mapped[str] = mapped_column(String(64), default="", index=True)
    enabled: Mapped[bool] = mapped_column(default=True)
    created_at: Mapped[dt.datetime] = mapped_column(DateTime, default=_utcnow)


class DriverConfig(Base):
    """驱动级配置(规则/目录等, 不随账户): 一个驱动类型一套。"""
    __tablename__ = "driver_configs"

    driver_type: Mapped[str] = mapped_column(String(32), primary_key=True)
    config_json: Mapped[str] = mapped_column(Text, default="{}")
    updated_at: Mapped[dt.datetime] = mapped_column(DateTime, default=_utcnow)

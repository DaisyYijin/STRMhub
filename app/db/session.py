"""数据库会话: SQLite + WAL, 单写连接(参考 LitePan store 设计)。"""
from __future__ import annotations

from sqlalchemy import create_engine, event
from sqlalchemy.orm import DeclarativeBase, sessionmaker

from .. import config


class Base(DeclarativeBase):
    pass


def _make_engine(db_path=None):
    path = db_path or (config.data_dir() / "db" / "strmhub.db")
    path.parent.mkdir(parents=True, exist_ok=True)  # 直接使用 DB 时确保目录存在
    engine = create_engine(
        f"sqlite:///{path}",
        connect_args={"check_same_thread": False, "timeout": 30},
    )

    @event.listens_for(engine, "connect")
    def _set_pragma(dbapi_conn, _record):
        cur = dbapi_conn.cursor()
        cur.execute("PRAGMA journal_mode=WAL")
        cur.execute("PRAGMA busy_timeout=30000")
        cur.execute("PRAGMA foreign_keys=ON")
        cur.close()

    return engine


_engine = None
_SessionLocal = None


def _migrate_columns(engine) -> None:
    """轻量迁移: 旧库补充新列(create_all 不会加列)。"""
    import sqlalchemy as sa
    from sqlalchemy import inspect
    insp = inspect(engine)
    if "accounts" not in insp.get_table_names():
        return
    acols = {c["name"] for c in insp.get_columns("accounts")}
    with engine.begin() as conn:
        if "info_json" not in acols:
            conn.execute(sa.text(
                "ALTER TABLE accounts ADD COLUMN info_json TEXT DEFAULT '{}'"))
    if "tasks" in insp.get_table_names():
        tcols = {c["name"] for c in insp.get_columns("tasks")}
        with engine.begin() as conn:
            if "monitor_life" not in tcols:
                conn.execute(sa.text(
                    "ALTER TABLE tasks ADD COLUMN monitor_life INTEGER DEFAULT 0"))
            if "life_cursor_json" not in tcols:
                conn.execute(sa.text(
                    "ALTER TABLE tasks ADD COLUMN life_cursor_json TEXT DEFAULT '{}'"))
            if "extra_json" not in tcols:
                conn.execute(sa.text(
                    "ALTER TABLE tasks ADD COLUMN extra_json TEXT DEFAULT '{}'"))


def init_db(db_path=None) -> None:
    """初始化引擎与表结构(幂等)。"""
    global _engine, _SessionLocal
    if _engine is None:
        _engine = _make_engine(db_path)
        _SessionLocal = sessionmaker(bind=_engine, expire_on_commit=False)
        Base.metadata.create_all(_engine)
        _migrate_columns(_engine)


def session_scope():
    """上下文管理器: 自动提交/回滚/关闭。"""
    from contextlib import contextmanager

    @contextmanager
    def _scope():
        if _SessionLocal is None:
            init_db()
        session = _SessionLocal()
        try:
            yield session
            session.commit()
        except Exception:
            session.rollback()
            raise
        finally:
            session.close()

    return _scope()


def get_session():
    if _SessionLocal is None:
        init_db()
    return _SessionLocal()

"""115 网盘管理页全部配置的保存链路与 data 目录持久化验证。

模拟前端每个 tab 的保存操作(saveRules: 先 GET 合并 -> PUT 子集),
验证: ①各字段保存成功 ②GET 读回一致 ③重启(新实例)后仍在 ④数据落在 data 目录。
"""
from __future__ import annotations

import json
import sqlite3
import uuid

import pytest
from fastapi.testclient import TestClient

from app import config
from app.main import app

# 前端各 tab 保存的字段子集(与 Accounts.vue saveRules 调用一致)
TAB_FIELDS = {
    "identify": ["min_video_size_mb", "blacklist", "custom_words",
                 "custom_matches", "release_groups"],
    "ai": ["ai"],
    "rename": ["rename_movie_folder", "rename_movie_file", "rename_tv_folder",
               "rename_season_folder", "rename_episode_file"],
    "category": ["category_yaml"],
    "organize": ["organize_dirs"],
}

CATEGORY_YAML = """movie:
  动画电影:
    genre_ids: '16'
  外语电影:
"""

FULL_RULES = {
    "min_video_size_mb": 100,
    "blacklist": ["trailer", "sample"],
    "custom_words": ["SW|Star Wars"],
    "custom_matches": ["星际穿越|157336|movie"],
    "release_groups": ["FRDS", "NEWCINE"],
    "ai": {"mode": "assist", "api_base": "https://api.x/v1",
           "api_key": "sk-test", "model": "gpt-4o-mini"},
    "rename_movie_folder": "{first_letter}-{title}-{year}-[tmdb=[[tmdb_id]]]",
    "rename_movie_file": "{title}.{year}<.{resource_pix}><-{resource_team}>",
    "rename_tv_folder": "{title}-{year}",
    "rename_season_folder": "Season {season_num:02d}",
    "rename_episode_file": "{title}.{season_episode}",
    "category_yaml": CATEGORY_YAML,
    "organize_dirs": {"pending": {"id": "342", "name": "待整理"},
                      "existing": {"id": "343", "name": "已存在"},
                      "redundant": {"id": "344", "name": "冗余"}},
}

DB_FILE = config.data_dir() / "db" / "strmhub.db"


def _login(c):
    return c.post("/api/auth/login",
                  json={"password": "testpass"}).json()["token"]


@pytest.fixture(scope="module")
def client():
    with TestClient(app) as c:
        yield c


@pytest.fixture()
def configured_account(client):
    """创建账户并模拟前端各 tab 保存全部规则(合并方式与前端相同)。"""
    h = {"Authorization": f"Bearer {_login(client)}"}
    acc = client.post("/api/accounts", headers=h, json={
        "name": f"持久化账户-{uuid.uuid4().hex[:8]}", "driver_type": "p115",
        "credential": "UID=1; CID=2"}).json()
    aid = acc["id"]
    for _key, fields in TAB_FIELDS.items():
        cur = client.get(f"/api/accounts/{aid}/rules",
                         headers=h).json()["rules"]
        merged = dict(cur)
        for f in fields:
            merged[f] = FULL_RULES[f]
        r = client.put(f"/api/accounts/{aid}/rules", headers=h,
                       json={"rules": merged})
        assert r.status_code == 200, r.text
    return aid, h


def test_db_lives_in_data_dir():
    """数据库文件必须在 data 目录下。"""
    assert "db" in str(DB_FILE)
    assert "strmhub.db" == DB_FILE.name


def test_all_tabs_save_and_read_back(client, configured_account):
    """各 tab 字段保存后 GET 读回一致。"""
    aid, h = configured_account
    got = client.get(f"/api/accounts/{aid}/rules", headers=h).json()["rules"]
    for f, v in FULL_RULES.items():
        assert got.get(f) == v, f"字段 {f} 读回不一致: {got.get(f)!r}"


def test_persisted_after_restart(configured_account):
    """重启(新应用实例)后配置仍在。"""
    aid, _h = configured_account
    with TestClient(app) as c2:
        h2 = {"Authorization": f"Bearer {_login(c2)}"}
        got = c2.get(f"/api/accounts/{aid}/rules", headers=h2).json()["rules"]
        assert got["organize_dirs"]["pending"]["name"] == "待整理"
        assert got["ai"]["model"] == "gpt-4o-mini"
        assert got["rename_movie_folder"].startswith("{first_letter}")
        assert got["category_yaml"].startswith("movie:")
        assert got["min_video_size_mb"] == 100


def test_rules_stored_in_db_file(configured_account):
    """规则确实写入 data 目录下的 SQLite(config_json), 凭据密文不泄露明文。"""
    aid, _h = configured_account
    assert DB_FILE.exists(), f"数据库文件不存在: {DB_FILE}"
    conn = sqlite3.connect(DB_FILE)
    try:
        row = conn.execute(
            "SELECT config_json FROM accounts WHERE id=?", (aid,)).fetchone()
    finally:
        conn.close()
    assert row, "账户记录不存在"
    cfg = json.loads(row[0])
    rules = cfg.get("rules", {})
    assert rules["organize_dirs"]["pending"]["id"] == "342"
    assert rules["category_yaml"].startswith("movie:")
    assert rules["ai"]["api_key"] == "sk-test"
    assert "UID=1" not in json.dumps(cfg)  # 凭据已加密, 无明文

"""前端托管集成测试: 根路径返回前端页面, /assets 静态资源可访问, API 不受影响。"""
from __future__ import annotations

import pytest
from fastapi.testclient import TestClient

from app.main import app
from app.web import INDEX_HTML, has_frontend


@pytest.fixture(scope="module")
def client():
    with TestClient(app) as c:
        yield c


class TestWeb:
    def test_index_serves_frontend(self, client):
        r = client.get("/")
        if not has_frontend():
            pytest.skip("前端未构建, 跳过页面断言")
        assert r.status_code == 200
        assert "text/html" in r.headers["content-type"]
        assert "STRMhub" in r.text

    def test_index_contains_app_mount(self, client):
        if not has_frontend():
            pytest.skip("前端未构建")
        assert "id=\"app\"" in client.get("/").text

    def test_assets_served(self, client):
        if not has_frontend():
            pytest.skip("前端未构建")
        # 从 index.html 提取 assets 引用并请求
        import re
        html = client.get("/").text
        m = re.search(r'src="(/assets/[^"]+)"', html)
        assert m, "index.html 应引用 assets 脚本"
        r = client.get(m.group(1))
        assert r.status_code == 200
        assert "javascript" in r.headers["content-type"]

    def test_api_unaffected(self, client):
        r = client.get("/api/health")
        assert r.status_code == 200
        assert r.json()["status"] == "ok"

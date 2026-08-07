"""TMDB 客户端(可注入 http 便于测试)。

无 TMDB_API_KEY 时所有搜索返回空(刮削降级为仅建索引)。
"""
from __future__ import annotations

import os

import httpx

API_BASE = "https://api.themoviedb.org/3"


class TMDBClient:
    def __init__(self, api_key: str | None = None, http: httpx.Client | None = None):
        self.api_key = api_key if api_key is not None else os.environ.get("TMDB_API_KEY", "")
        self.http = http or httpx.Client(timeout=20)

    def available(self) -> bool:
        return bool(self.api_key)

    def _get(self, path: str, params: dict) -> dict:
        if not self.available():
            return {}
        params = dict(params)
        params["api_key"] = self.api_key
        params["language"] = params.get("language", "zh-CN")
        resp = self.http.get(f"{API_BASE}{path}", params=params)
        if resp.status_code != 200:
            return {}
        return resp.json()

    def search_movie(self, title: str, year: int | None = None) -> list[dict]:
        data = self._get("/search/movie", {"query": title, "year": year or ""})
        return data.get("results") or []

    def search_tv(self, title: str, year: int | None = None) -> list[dict]:
        data = self._get("/search/tv", {"query": title, "first_air_date_year": year or ""})
        return data.get("results") or []

    def movie_details(self, tmdb_id: int) -> dict:
        return self._get(f"/movie/{tmdb_id}", {})

    def tv_details(self, tmdb_id: int) -> dict:
        return self._get(f"/tv/{tmdb_id}", {})

    @staticmethod
    def poster_url(path: str, size: str = "w500") -> str:
        return f"https://image.tmdb.org/t/p/{size}{path}"

    def download_poster(self, path: str, dest, size: str = "w500") -> bool:
        """下载海报到 dest(文件路径), 失败返回 False。"""
        if not path:
            return False
        resp = self.http.get(self.poster_url(path, size))
        if resp.status_code != 200:
            return False
        dest.write_bytes(resp.content)
        return True

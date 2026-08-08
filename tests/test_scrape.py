"""刮削测试: 分组/类型推断/TMDB 匹配(fake)/nfo+海报写入/海报墙索引。"""
from __future__ import annotations

import json
from pathlib import Path

import httpx
import pytest

from app.services.scrape.scan import scan_strm_dir
from app.services.scrape.service import ScrapeService
from app.services.scrape.tmdb import TMDBClient


def _make_strm_library(tmp_path: Path) -> Path:
    """构造 strm 媒体库: 一部电影 + 一部剧集 + 库根散文件。"""
    lib = tmp_path / "library"
    (lib / "Avatar (2009)").mkdir(parents=True)
    (lib / "Avatar (2009)" / "avatar.strm").write_text("http://hub:6060/x",
                                                       encoding="utf-8")
    (lib / "Breaking Bad").mkdir()
    (lib / "Breaking Bad" / "Season 1").mkdir()
    (lib / "Breaking Bad" / "Season 1" / "S01E01.strm").write_text(
        "http://hub:6060/y", encoding="utf-8")
    (lib / "Solo Movie 2021.strm").write_text("http://hub:6060/z",
                                              encoding="utf-8")
    return lib


def _fake_tmdb(api_key="k"):
    """返回 (client, handler); 用 MockTransport 模拟 TMDB API。"""
    def handler(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if path == "/3/search/movie":
            q = request.url.params.get("query", "")
            if "Avatar" in q or "Solo" in q:
                return httpx.Response(200, json={"results": [{
                    "id": 19995, "title": "Avatar", "release_date": "2009-12-18",
                    "poster_path": "/avatar.jpg",
                }]})
            return httpx.Response(200, json={"results": []})
        if path == "/3/search/tv":
            if "Breaking" in q_of(request):
                return httpx.Response(200, json={"results": [{
                    "id": 1396, "name": "Breaking Bad",
                    "first_air_date": "2008-01-20", "poster_path": "/bb.jpg",
                }]})
            return httpx.Response(200, json={"results": []})
        if path == "/3/movie/19995":
            return httpx.Response(200, json={"id": 19995, "overview": "蓝人",
                                             "original_title": "Avatar"})
        if path == "/3/tv/1396":
            return httpx.Response(200, json={
                "id": 1396, "overview": "老白", "original_name": "Breaking Bad",
                "seasons": [{"season_number": 1, "episode_count": 7},
                            {"season_number": 2, "episode_count": 13}]})
        if path == "/3/tv/1396/season/1":
            return httpx.Response(200, json={
                "id": 3627, "name": "第 1 季", "overview": "第一季简介",
                "air_date": "2008-01-20", "poster_path": "/s1.jpg",
                "episodes": [{
                    "id": 62085, "name": "试播集", "overview": "第一集",
                    "air_date": "2008-01-20", "episode_number": 1,
                    "still_path": "/st1.jpg"}]})
        if path == "/t/p/w500/s1.jpg":
            return httpx.Response(200, content=b"IMG-S1")
        if path == "/t/p/w500/st1.jpg":
            return httpx.Response(200, content=b"IMG-ST1")
        if path == "/t/p/w500/avatar.jpg":
            return httpx.Response(200, content=b"IMG-A")
        if path == "/t/p/w500/bb.jpg":
            return httpx.Response(200, content=b"IMG-B")
        return httpx.Response(404, text="nope")

    def q_of(request):
        return request.url.params.get("query", "")

    client = TMDBClient(api_key=api_key,
                        http=httpx.Client(transport=httpx.MockTransport(handler)))
    return client


class TestScan:
    def test_groups(self, tmp_path: Path):
        lib = _make_strm_library(tmp_path)
        groups = scan_strm_dir(lib)
        by_root = {g.root: g for g in groups}
        assert set(by_root) == {lib / "Avatar (2009)", lib / "Breaking Bad", lib}
        # 剧集根: Season 目录上溯
        bb = by_root[lib / "Breaking Bad"]
        assert bb.is_tv is True and len(bb.files) == 1
        av = by_root[lib / "Avatar (2009)"]
        assert av.is_tv is False
        assert av.title_hint == "Avatar" and av.year_hint == 2009


class TestScrapeService:
    def test_run_with_tmdb(self, tmp_path: Path):
        lib = _make_strm_library(tmp_path)
        tmdb = _fake_tmdb()
        result = ScrapeService().run(lib, "task1", tmdb)
        assert result.groups == 3
        assert result.matched == 3
        assert result.posters >= 2

        # nfo 断言
        movie_nfo = lib / "Avatar (2009)" / "avatar.nfo"
        assert movie_nfo.exists()
        content = movie_nfo.read_text(encoding="utf-8")
        assert "<tmdbid>19995</tmdbid>" in content
        assert "Avatar" in content
        tv_nfo = lib / "Breaking Bad" / "tvshow.nfo"
        assert tv_nfo.exists()
        assert "<tmdbid>1396</tmdbid>" in tv_nfo.read_text(encoding="utf-8")

        # 海报断言
        assert (lib / "Avatar (2009)" / "poster.jpg").exists()
        assert (lib / "Breaking Bad" / "poster.jpg").exists()

        # 海报墙索引
        items = ScrapeService().list_items("task1")
        by_title = {i["title"]: i for i in items}
        assert by_title["Avatar"]["media_type"] == "movie"
        assert by_title["Avatar"]["tmdb_id"] == 19995
        bb = by_title["Breaking Bad"]
        assert bb["media_type"] == "tv" and bb["ep_tmdb"] == 20

    def test_tv_season_episode_nfo(self, tmp_path: Path):
        """剧集刮削生成季/集 nfo + 季海报 + 集缩略图(LitePan 方式)。"""
        lib = tmp_path / "lib"
        (lib / "Breaking Bad" / "Season 1").mkdir(parents=True)
        (lib / "Breaking Bad" / "Season 1" / "S01E01.strm").write_text(
            "http://hub:6060/y", encoding="utf-8")
        (lib / "Breaking Bad" / "Season 1" / "S01E02.strm").write_text(
            "http://hub:6060/y2", encoding="utf-8")
        svc = ScrapeService()
        result = svc.run(lib, "t1", tmdb=_fake_tmdb())
        assert result.matched == 1
        season_dir = lib / "Breaking Bad" / "Season 1"
        assert (season_dir / "season.nfo").exists()
        assert (season_dir / "poster.jpg").exists()
        assert (season_dir / "S01E01.nfo").exists()
        ep_nfo = (season_dir / "S01E01.nfo").read_text(encoding="utf-8")
        assert "<episodedetails>" in ep_nfo
        assert "<season>1</season>" in ep_nfo
        assert "<episode>1</episode>" in ep_nfo
        assert "试播集" in ep_nfo
        assert "<showtitle>Breaking Bad</showtitle>" in ep_nfo
        assert (season_dir / "S01E01-thumb.jpg").exists()
        # 第二集无对应季详情集 -> 兜底标题"第 2 集"
        assert (season_dir / "S01E02.nfo").exists()


    def test_run_without_key(self, tmp_path: Path):
        """无 TMDB key: 不写 nfo, 仅建索引(none 状态)。"""
        lib = _make_strm_library(tmp_path)
        tmdb = TMDBClient(api_key="")
        result = ScrapeService().run(lib, "task2", tmdb)
        assert result.matched == 0 and result.none == 3
        assert not (lib / "Avatar (2009)" / "avatar.nfo").exists()
        items = ScrapeService().list_items("task2")
        assert all(i["status"] == "none" for i in items)

    def test_poster_download_failure_tolerated(self, tmp_path: Path):
        lib = _make_strm_library(tmp_path)
        tmdb = _fake_tmdb()

        # 覆盖 poster 下载为失败
        tmdb.download_poster = lambda p, d: False
        result = ScrapeService().run(lib, "task3", tmdb)
        assert result.posters == 0
        assert result.matched == 3  # nfo 仍写入

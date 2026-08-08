"""刮削编排: 扫描分组 -> TMDB 匹配 -> nfo/海报写入 -> 海报墙索引。"""
from __future__ import annotations

import re
from dataclasses import dataclass, field
from pathlib import Path

from sqlalchemy import select

from ...db.models import ScrapeItem
from ...db.session import session_scope
from . import nfo as nfo_writer
from .scan import WorkGroup, scan_strm_dir
from .tmdb import TMDBClient

EPISODE_RE = re.compile(r"[Ss](\d{1,2})[Ee](\d{1,3})")


@dataclass
class ScrapeResult:
    groups: int = 0
    matched: int = 0
    doubt: int = 0
    none: int = 0
    posters: int = 0
    error: str = ""


class ScrapeService:
    def run(self, strm_dir: str | Path, task_id: str,
            tmdb: TMDBClient | None = None) -> ScrapeResult:
        """刮削一个 strm 目录。tmdb 缺省时新建(需 TMDB_API_KEY)。"""
        result = ScrapeResult()
        tmdb = tmdb or TMDBClient()
        root = Path(strm_dir)
        groups = scan_strm_dir(root)
        result.groups = len(groups)

        for g in groups:
            item = self._match(tmdb, g)
            self._write_metadata(root, g, item, result, tmdb)
            self._upsert_index(task_id, g, item)

        return result

    # ---- TMDB 匹配 ----
    def _match(self, tmdb: TMDBClient, g: WorkGroup) -> dict:
        """返回 {'status','title','year','tmdb_id','plot','original_title','poster_path','ep_tmdb'}。"""
        base = {"status": "none", "title": g.title_hint or g.root.name,
                "year": g.year_hint, "tmdb_id": None, "plot": "",
                "original_title": "", "poster_path": None, "ep_tmdb": 0}
        if not tmdb.available():
            base["status"] = "none"
            return base

        search = tmdb.search_tv if (g.is_tv is True) else tmdb.search_movie
        hits = search(base["title"], base["year"])
        if not hits:
            # 年份未命中时回退: 不限年份再搜一次
            hits = search(base["title"], None)
        if not hits:
            base["status"] = "none"
            return base

        pick = hits[0]
        detail = (tmdb.tv_details(pick["id"]) if g.is_tv is True
                  else tmdb.movie_details(pick["id"]))
        base.update({
            "status": "matched",
            "tmdb_id": pick["id"],
            "title": pick.get("name") or pick.get("title") or base["title"],
            "year": (pick.get("first_air_date") or pick.get("release_date") or "")[:4] or base["year"],
            "plot": detail.get("overview", ""),
            "original_title": detail.get("original_title") or detail.get("original_name", ""),
            "poster_path": pick.get("poster_path"),
            "ep_tmdb": sum(int(s.get("episode_count") or 0)
                           for s in detail.get("seasons") or []),
        })
        return base

    # ---- 写入 ----
    def _write_metadata(self, root: Path, g: WorkGroup, item: dict,
                        result: ScrapeResult, tmdb: TMDBClient) -> None:
        if item["status"] == "matched":
            result.matched += 1
        elif item["status"] == "doubt":
            result.doubt += 1
        else:
            result.none += 1
            return

        poster_downloader = (
            (lambda p, d: tmdb.download_poster(p, d)) if tmdb.available()
            else (lambda p, d: False)
        )
        if g.is_tv is True:
            nfo_writer.write_tvshow_nfo(
                g.root, item["title"], self._int(item["year"]), item["tmdb_id"],
                item["plot"], item["original_title"])
            poster = nfo_writer.write_poster(g.root, item["poster_path"], poster_downloader)
            if tmdb.available():
                self._write_tv_season_episode_nfo(g, item, tmdb, poster_downloader)
        else:
            nfo_writer.write_movie_nfo(
                g.root, item["title"], self._int(item["year"]), item["tmdb_id"],
                item["plot"], item["original_title"])
            poster = nfo_writer.write_poster(g.root, item["poster_path"], poster_downloader)
            # 电影海报: 兼容 poster.jpg 与 同名-poster.jpg 均指向 poster.jpg
        if poster:
            result.posters += 1

    def _write_tv_season_episode_nfo(self, g: WorkGroup, item: dict,
                                     tmdb: TMDBClient, poster_downloader) -> None:
        """按 LitePan 方式写季/集 nfo: 季目录 season.nfo+poster.jpg, 集同名 .nfo+-thumb.jpg。"""
        episodes: dict[int, list[tuple[int, Path]]] = {}  # season -> [(episode, strm)]
        for f in g.files:
            m = EPISODE_RE.search(f.name)
            if m:
                sn, en = int(m.group(1)), int(m.group(2))
                episodes.setdefault(sn, []).append((en, f))
        if not episodes:
            return
        import time as _time
        for sn in sorted(episodes):
            detail = tmdb.tv_season_details(item["tmdb_id"], sn)
            if not detail:
                continue
            season_dir = episodes[sn][0][1].parent
            nfo_writer.write_season_nfo(
                season_dir, sn, detail.get("name") or "",
                detail.get("overview") or "", detail.get("air_date") or "")
            if detail.get("poster_path"):
                nfo_writer.write_poster(season_dir, detail["poster_path"],
                                        poster_downloader)
            ep_by_num = {e.get("episode_number"): e
                         for e in (detail.get("episodes") or [])}
            for en, strm_path in episodes[sn]:
                ep = ep_by_num.get(en) or {}
                nfo_writer.write_episode_nfo(
                    strm_path,
                    ep.get("name") or f"第 {en} 集",
                    item["title"], ep.get("overview") or "",
                    ep.get("air_date") or "",
                    int(ep.get("id") or 0), sn, en)
                nfo_writer.write_thumb(strm_path, ep.get("still_path"),
                                       poster_downloader)
            _time.sleep(0.25)  # TMDB 限流节流

    # ---- 海报墙索引 ----
    def _upsert_index(self, task_id: str, g: WorkGroup, item: dict) -> None:
        ep_local = sum(1 for f in g.files if EPISODE_RE.search(f.name))
        poster_rel = "poster.jpg"
        root_rel = str(g.root.relative_to(g.root.anchor)) if g.root.is_absolute() else str(g.root)
        with session_scope() as s:
            existing = s.scalar(select(ScrapeItem).where(
                ScrapeItem.task_id == task_id, ScrapeItem.title == item["title"]))
            if existing is None:
                existing = ScrapeItem(task_id=task_id, title=item["title"])
                s.add(existing)
            existing.year = self._int(item["year"])
            existing.media_type = "tv" if g.is_tv is True else "movie"
            existing.status = item["status"]
            existing.tmdb_id = self._int(item["tmdb_id"])
            existing.poster_rel = poster_rel
            existing.root_rel = root_rel
            existing.ep_local = ep_local
            existing.ep_tmdb = int(item["ep_tmdb"] or 0)

    def list_items(self, task_id: str) -> list[dict]:
        with session_scope() as s:
            rows = s.scalars(select(ScrapeItem).where(
                ScrapeItem.task_id == task_id).order_by(ScrapeItem.title)).all()
            return [{
                "title": r.title, "year": r.year, "media_type": r.media_type,
                "status": r.status, "tmdb_id": r.tmdb_id,
                "poster_rel": r.poster_rel, "root_rel": r.root_rel,
                "ep_local": r.ep_local, "ep_tmdb": r.ep_tmdb,
            } for r in rows]

    @staticmethod
    def _int(v) -> int | None:
        try:
            return int(v) if v not in (None, "", 0) else None
        except (TypeError, ValueError):
            return None

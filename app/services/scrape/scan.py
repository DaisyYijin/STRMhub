"""刮削扫描与作品分组。

设计来源: LitePan internal/strmscrape/scan.go ——
- 以作品(workGroup)为单位, 按目录分组;
- 自动跳过 Season/特别篇 结构目录上溯到剧集根;
- 库根散落的单个 .strm 各自成组;
- 媒体类型推断: 目录结构优先 -> SxxExx 文件名 -> 多文件集号。
"""
from __future__ import annotations

import re
from dataclasses import dataclass, field
from pathlib import Path

STRM_SUFFIX = ".strm"
SEASON_DIR_RE = re.compile(r"^(season|special|第.+季)", re.IGNORECASE)
EPISODE_RE = re.compile(r"[Ss](\d{1,2})[Ee](\d{1,3})")
YEAR_RE = re.compile(r"\((\d{4})\)")


@dataclass
class WorkGroup:
    """一个待刮削的作品: 由若干 .strm 文件组成。"""
    root: Path            # 作品根目录
    files: list[Path] = field(default_factory=list)
    is_tv: bool | None = None   # True=剧集 False=电影 None=未知(由匹配决定)
    title_hint: str = ""        # 从目录名推断的标题
    year_hint: int | None = None


def _parse_dir_name(name: str) -> tuple[str, int | None]:
    """目录名 'Title (2024)' -> (title, 2024)。"""
    m = YEAR_RE.search(name)
    year = int(m.group(1)) if m else None
    title = YEAR_RE.sub("", name).strip()
    return title, year


def _infer_tv_from_files(files: list[Path]) -> bool:
    """任一文件名含 SxxExx -> 剧集。"""
    return any(EPISODE_RE.search(p.name) for p in files)


def scan_strm_dir(strm_dir: str | Path) -> list[WorkGroup]:
    """扫描 strm 目录, 返回作品分组列表。"""
    root = Path(strm_dir)
    if not root.is_dir():
        return []
    strm_files = sorted(root.rglob(f"*{STRM_SUFFIX}"))
    if not strm_files:
        return []

    groups: list[WorkGroup] = []

    def add_file(path: Path) -> None:
        # 上溯: 跳过 Season/Special/第X季 目录, 归到剧集根
        parent = path.parent
        while parent != root and SEASON_DIR_RE.match(parent.name or ""):
            parent = parent.parent
        for g in groups:
            if g.root == parent:
                g.files.append(path)
                return
        groups.append(WorkGroup(root=parent, files=[path]))

    for f in strm_files:
        add_file(f)

    for g in groups:
        # 类型推断: 根目录下直接含 Season 子目录 -> TV
        has_season_dir = any(
            p.is_dir() and SEASON_DIR_RE.match(p.name or "")
            for p in g.root.iterdir()) if g.root != root else False
        # 库根散文件: 单个文件 -> 电影, 多个含集号 -> TV
        if g.root == root and g.files:
            g.is_tv = _infer_tv_from_files(g.files)
            # 标题从文件名推断(目录名是库根, 无意义)
            name = g.files[0].name
            base = name[: name.lower().rfind(".strm")]
            g.title_hint, g.year_hint = _parse_dir_name(base)
        else:
            g.is_tv = has_season_dir or _infer_tv_from_files(g.files)
            g.title_hint, g.year_hint = _parse_dir_name(g.root.name)
            if not g.title_hint and g.files:
                name = g.files[0].name
                base = name[: name.lower().rfind(".strm")]
                g.title_hint, g.year_hint = _parse_dir_name(base)

    return groups

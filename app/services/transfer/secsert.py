"""秒传 JSON 解析器: 123 网盘分享生态的事实标准。

协议来源: TgtoDrive tgto123.py —— 支持四种前缀:
- 123FSLinkV1$  旧版字符串
- 123FSLinkV2$  base62 etag 版
- 123FLCPV1$ / 123FLCPV2$  JSON 版(含 commonPath / usesBase62EtagsInExport / files[])
- md5 支持 hex / base64 双编码归一化(robust_normalize_md5)
"""
from __future__ import annotations

import base64
import json
import re
from dataclasses import dataclass, field

PREFIX_V1 = "123FSLinkV1$"
PREFIX_V2 = "123FSLinkV2$"
PREFIX_JSON_V1 = "123FLCPV1$"
PREFIX_JSON_V2 = "123FLCPV2$"

_ETAG_PATTERN = re.compile(r"^[a-zA-Z0-9+/=]+$")


@dataclass
class SecFile:
    """解析后的单个文件条目。"""
    name: str                 # 文件名(JSON 版为相对 commonPath 的路径)
    size: int = 0
    etag: str = ""            # md5(hex 或 base64)或 base62 etag
    is_dir: bool = False


@dataclass
class SecsertBundle:
    format: str               # 前缀标识
    common_path: str = ""     # JSON 版公共目录
    files: list = field(default_factory=list)
    error: str = ""

    @property
    def total_size(self) -> int:
        return sum(f.size for f in self.files)


def normalize_md5(md5_hex_or_b64: str) -> str:
    """md5 双编码归一化 -> 32 位小写 hex。

    hex 形式(32 字符)原样小写; base64 形式(24 字符含 =)解码后转 hex。
    """
    value = (md5_hex_or_b64 or "").strip()
    if not value:
        return ""
    if len(value) == 32 and re.fullmatch(r"[0-9a-fA-F]{32}", value):
        return value.lower()
    # base64 解码 -> 16 字节 -> hex
    try:
        padded = value + "=" * (-len(value) % 4)
        raw = base64.b64decode(padded, validate=True)
        if len(raw) == 16:
            return raw.hex()
    except (ValueError, binascii.Error):
        pass
    return value.lower()


def decode_base62_etag(etag: str) -> str:
    """base62 etag -> 32 位 hex md5(部分网盘版本; 失败返回原值)。

    base62 字符集: 0-9A-Za-z(123 用 0-9a-zA-Z 顺序实现, 此处按通用表解码)。
    """
    charset = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
    if not etag or any(c not in charset for c in etag):
        return etag
    n = 0
    for ch in etag:
        n = n * 62 + charset.index(ch)
    return f"{n:032x}"


def _parse_json_payload(payload: str, is_v2: bool) -> SecsertBundle:
    """解析 JSON 版秒传串(123FLCPV1$/V2$ 之后的部分)。"""
    bundle = SecsertBundle(format=PREFIX_JSON_V2 if is_v2 else PREFIX_JSON_V1)
    try:
        data = json.loads(payload)
    except json.JSONDecodeError:
        bundle.error = "JSON 解析失败"
        return bundle
    bundle.common_path = str(data.get("commonPath") or data.get("common_path") or "")
    use_b62 = bool(data.get("usesBase62EtagsInExport")) or is_v2
    raw_files = data.get("files") or []
    for item in raw_files:
        name = str(item.get("name") or item.get("fileName") or item.get("filename") or "")
        size = int(item.get("size") or 0)
        etag = str(item.get("etag") or item.get("md5") or "")
        if use_b62:
            etag = decode_base62_etag(etag)
        else:
            etag = normalize_md5(etag)
        is_dir = bool(item.get("isDir") or item.get("is_dir"))
        bundle.files.append(SecFile(name=name, size=size, etag=etag, is_dir=is_dir))
    if not raw_files:
        bundle.error = "files 为空"
    return bundle


def parse_secsert(text: str) -> SecsertBundle:
    """解析 123 秒传串(支持四种前缀 + 无前缀 JSON 兜底)。"""
    text = (text or "").strip()
    if not text:
        return SecsertBundle(format="", error="空输入")
    if text.startswith(PREFIX_JSON_V2):
        return _parse_json_payload(text[len(PREFIX_JSON_V2):], True)
    if text.startswith(PREFIX_JSON_V1):
        return _parse_json_payload(text[len(PREFIX_JSON_V1):], False)
    if text.startswith(PREFIX_V2):
        return _parse_line_payload(text[len(PREFIX_V2):], True)
    if text.startswith(PREFIX_V1):
        return _parse_line_payload(text[len(PREFIX_V1):], False)
    # 无前缀: 尝试 JSON
    if text.lstrip().startswith("{"):
        return _parse_json_payload(text, False)
    return SecsertBundle(format="", error="无法识别秒传格式")


def _parse_line_payload(payload: str, is_v2: bool) -> SecsertBundle:
    """解析字符串版秒传(每行: 文件名|大小|etag, 或 路径/文件名|大小|etag)。"""
    bundle = SecsertBundle(format=PREFIX_V2 if is_v2 else PREFIX_V1)
    for line in payload.splitlines():
        line = line.strip()
        if not line:
            continue
        parts = line.split("|")
        if len(parts) < 2:
            bundle.error = f"行格式错误: {line[:60]}"
            continue
        name = parts[0].strip()
        try:
            size = int(parts[1].strip())
        except ValueError:
            size = 0
        etag = parts[2].strip() if len(parts) > 2 else ""
        if is_v2:
            etag = decode_base62_etag(etag)
        else:
            etag = normalize_md5(etag)
        bundle.files.append(SecFile(name=name, size=size, etag=etag))
    if not bundle.files:
        bundle.error = "无可解析条目"
    return bundle

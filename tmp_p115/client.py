#!/usr/bin/env python3
# encoding: utf-8

from __future__ import annotations

__all__ = ["check_response", "P115OpenClient", "P115Client"]
__doc__ = "115 客户端模块"

from asyncio import Lock as AsyncLock
from base64 import b64encode
from collections import UserString
from collections.abc import (
    AsyncIterable, Awaitable, Buffer, Callable, Coroutine, 
    Iterable, Iterator, Mapping, MutableMapping, Sequence, 
)
from datetime import date, datetime, timedelta
from hashlib import md5, sha1
from http.cookiejar import Cookie, CookieJar
from http.cookies import Morsel, BaseCookie
from inspect import isawaitable
from operator import itemgetter
from os import fsdecode, isatty, PathLike
from pathlib import Path, PurePath
from re import compile as re_compile, Match, MULTILINE
from string import digits
from threading import Lock
from time import time
from typing import cast, overload, Any, Final, Literal, Self
from urllib.parse import parse_qsl, quote, unquote, urlencode, urlsplit
from uuid import uuid4
from warnings import warn

from asynctools import ensure_async
from cookietools import cookies_to_dict, update_cookies
from dicttools import (
    get_first, dict_update, dict_key_to_lower_merge, iter_items, KeyLowerDict, 
)
from ensure import ensure_bytes
from errno2 import errno
from filewrap import to_bytes_view, SupportsRead
from http_request import complete_url as make_url, SupportsGeturl
from http_response import get_status_code
from httpfile import HTTPFileReader, AsyncHTTPFileReader
from iterutils import run_gen_step
from orjson import dumps, loads
from p115cipher import (
    rsa_encrypt, rsa_decrypt, ecdh_aes_encrypt, ecdh_aes_decrypt, 
    ecdh_encode_token, make_upload_payload, 
)
from p115oss import upload_file
from p115pickcode import to_id, to_pickcode
from property import locked_cacheproperty
from temporary import temp_globals
from yarl import URL

from .const import (
    CLIENT_API_METHODS_MAP, CLIENT_METHOD_API_MAP, 
    SSOENT_TO_APP, 
)
from .exception import (
    throw, P115OSError, P115Warning, P115AccessTokenError, 
    P115AuthenticationError, P115LoginError, P115OpenAppAuthLimitExceeded, 
    P115OperationalError, P115DownloadFileNotFoundError, 
)
from .type import P115Cookies, P115URL
from .util import complete_url, share_extract_payload, normalize_pickcode


CRE_SET_COOKIE: Final = re_compile(r"[0-9a-f]{32}=[0-9a-f]{32}.*")
CRE_COOKIES_UID_search: Final = re_compile(r"(?<=\bUID=)[^\s;]+").search
CRE_AREA_DATA_search: Final = re_compile(r"(?<=n=)\{[\s\S]+?\}(?=;)").search

_default_k_ec = {"k_ec": ecdh_encode_token(0).decode()}
_default_code_verifier = "0" * 64
_default_code_challenge = b64encode(md5(b"0" * 64).digest()).decode()
_default_code_challenge_method = "md5"
_app_version = "36.2.28"


def content_maybe_decrypt(content: Buffer, /) -> Buffer:
    data = to_bytes_view(content)
    if data[:1].tobytes() + data[-1:].tobytes() not in (b"{}", b"[]", b'""'):
        return ecdh_aes_decrypt(data)
    return content


def json_loads(content: Buffer, /):
    try:
        return loads(to_bytes_view(content))
    except Exception:
        throw(errno.ENODATA, bytes(content))


def json_decrypt_loads(content: Buffer, /):
    return json_loads(ecdh_aes_decrypt(content))


def json_maybe_decrypt_loads(content: Buffer, /):
    return json_loads(content_maybe_decrypt(content))


def json_parse(_, content: Buffer, /):
    return json_loads(content)


def json_decrypt_parse(_, content: Buffer, /):
    return json_decrypt_loads(content)


def json_maybe_decrypt_parse(_, content: Buffer, /):
    return json_maybe_decrypt_loads(content)


def get_request(
    url: str, 
    method: str = "GET", 
    payload: Any = None, 
    headers: Any = None, 
    ecdh_encrypt: bool = False, 
    request: None | Callable = None, 
    self: None | ClientRequestMixin = None, 
    **request_kwargs, 
) -> tuple[Callable, dict]:
    if self is not None:
        request_kwargs.update(
            url=url, 
            method=method, 
            payload=payload, 
            headers=headers, 
            ecdh_encrypt=ecdh_encrypt, 
            request=request, 
        )
        return self.request, request_kwargs
    if request is None:
        from urllib3_future_request import request
    request = cast(Callable, request)
    request_kwargs.update(url=url, method=method)
    is_open_api = URL(url).path.startswith("/open/")
    if is_open_api:
        ecdh_encrypt = False
    if payload is not None:
        request_kwargs.setdefault(
            "data" if method.upper() in ("POST", "PUT") else "params", 
            payload, 
        )
    params = request_kwargs.get("params")
    if isinstance(params, dict):
        params.setdefault("app_ver", _app_version)
    headers = request_kwargs["headers"] = dict_key_to_lower_merge(headers or ())
    headers["referer"] = headers.get("referer") or str(URL(url).origin())
    if ecdh_encrypt:
        url = request_kwargs["url"] = make_url(url, params=_default_k_ec)
        if data := request_kwargs.get("data"):
            if not isinstance(data, (Buffer, str, UserString)):
                data = urlencode(data)
            request_kwargs["data"] = ecdh_aes_encrypt(ensure_bytes(data) + b"&")
            headers["content-type"] = "application/x-www-form-urlencoded"
        request_kwargs.setdefault("parse", json_decrypt_parse)
    else:
        request_kwargs.setdefault("parse", json_parse)
    return request, request_kwargs


def md5_secret_password(password: None | int | str = "670b14728ad9902aecba32e22fa4f6bd", /) -> str:
    if not password:
        return "670b14728ad9902aecba32e22fa4f6bd"
    if isinstance(password, str) and len(password) == 32:
        return password
    return md5(f"{password:>06}".encode("ascii")).hexdigest()


def parse_upload_init_response(_, content: bytes, /) -> dict:
    data = ecdh_aes_decrypt(content)
    if not isinstance(data, (bytes, bytearray)):
        data = to_bytes_view(data)
    return json_loads(data)


def expand_payload(
    payload: dict[str, Any] | Iterable[tuple[str, Any]], 
    prefix: str = "", 
    enum_seq: bool | int = False, 
    seq_types: type | tuple[type, ...] = (tuple, list), 
    map_types: type | tuple[type, ...] = dict, 
) -> Iterable[tuple[str, Any]]:
    if prefix:
        prefix = f"{prefix}["
    for k, v in iter_items(payload):
        if prefix and not k.startswith(prefix):
            k = f"{prefix}{k}]"
        if isinstance(v, seq_types):
            v = cast(Sequence, v)
            if isinstance(enum_seq, bool):
                if enum_seq:
                    enum_seq = 0
                else:
                    for v2 in v:
                        yield from expand_payload(v2, f"{k}[]")
                    continue
                for i, v2 in enumerate(v, cast(int, enum_seq)):
                    yield from expand_payload(v2, f"{k}[{i}]")
        elif isinstance(v, map_types):
            v = cast(Mapping, v)
            k2: Any
            for k2, v2 in iter_items(v):
                yield from expand_payload(v2, f"{k}[{k2}]")
        else:
            yield k, v


@overload
def check_response(resp: dict, /) -> dict:
    ...
@overload
def check_response(resp: Awaitable[dict], /) -> Coroutine[Any, Any, dict]:
    ...
def check_response(resp: dict | Awaitable[dict], /) -> dict | Coroutine[Any, Any, dict]:
    """检测 115 的某个接口的响应，如果成功则直接返回，否则根据具体情况抛出一个异常，基本上是 OSError 的实例
    """
    def check(resp, /) -> dict:
        if not isinstance(resp, dict):
            raise P115OSError(errno.EIO, resp)
        if resp.get("state", True):
            return resp
        if code := get_first(resp, "errno", "errNo", "errcode", "errCode", "code", "msg_code", default=None):
            resp.setdefault("errno", code)
            if "error" not in resp:
                resp.setdefault("error", get_first(resp, "msg", "error_msg", "message", default=None))
            match code:
                # {"state": False, "error": "网盘单个文件夹限5万个文件，请清理后再添加！", "errno": 2}
                case 2:
                    raise P115OperationalError(errno.EIO, resp)
                # {"state": false, "errno": 99, "error": "请重新登录"}
                case 99:
                    raise P115LoginError(errno.EAUTH, resp)
                # {"state": false, "errno": 911, "error": "请验证账号"}
                case 911:
                    throw(errno.EAUTH, resp)
                # {"state": false, "errno": 1001, "error": "参数错误"}
                case 1001:
                    throw(errno.EINVAL, resp)
                # {"state": false, "errno": 10004, "error": "错误的链接"}
                case 10004:
                    throw(errno.EINVAL, resp)
                # {"state": false, "errno": 10014, "error": "云端目录不存在，请恢复后重新上传"}
                case 10014:
                    throw(errno.ENOENT, resp)
                # {"state": false, "errno": 20001, "error": "目录名称不能为空"}
                case 20001:
                    throw(errno.EINVAL, resp)
                # {"state": false, "errno": 20002, "error": "目录名称不能超出20个字。"}
                case 20002:
                    throw(errno.EINVAL, resp)
                # {"state": false, "errno": 20003, "error": "文件名不能包含以下任意字符之一 “ " < > ”。"}
                case 20003:
                    throw(errno.EINVAL, resp)
                # {"state": false, "errno": 20004, "error": "该目录名称已存在。"}
                case 20004:
                    throw(errno.EEXIST, resp)
                # {"state": false, "errno": 20009, "error": "父目录不存在。"}
                case 20009:
                    throw(errno.ENOENT, resp)
                # {"state": false, "errno": 20013, "error": "文件夹不存在或已删除。"}
                # {"state": false, "errno": 20018, "error": "文件不存在或已删除。"}
                # {"state": false, "errno": 31003, "error": "文件不存在或已删除。"}
                # {"state": false, "errno": 50015, "error": "文件不存在或已删除。"}
                # {"state": false, "errno": 70005, "error": "文件不存在或已删除"}
                # {"state": false, "errno": 70008, "error": "文件不存在或已删除"}
                # {"state": false, "errno": 90008, "error": "文件（夹）不存在或已经删除。"}
                # {"state": false, "errno": 430004, "error": "文件（夹）不存在或已删除。"}
                case 20013 | 20018 | 31003 | 50015 | 70005 | 70008 | 90008 | 430004:
                    if resp.get("is_download"):
                        raise P115DownloadFileNotFoundError(errno.ENOENT, resp)
                    throw(errno.ENOENT, resp)
                # {"state": false, "errno": 20020, "error": "后缀名不正确，请重新输入"}
                # {"state": false, "errno": 20021, "error": "后缀名不正确，请重新输入"}
                case 20020 | 20021:
                    throw(errno.EINVAL, resp)
                # {"state": false, "errno": 31001, "error": "所预览的文件不存在。"}
                case 31001:
                    throw(errno.ENOENT, resp)
                # {"state": false, "errno": 31004, "error": "文档未上传完整，请上传完成后再进行查看。"}
                case 31004:
                    throw(errno.ENOENT, resp)
                # {"state": false, "errno": 50003, "error": "很抱歉，该文件提取码不存在。"}
                case 50003:
                    throw(errno.ENOENT, resp)
                # {"state": false, "errno": 50028, "error": "文件大小超出限制，请使用115电脑端下载"}
                case 50028:
                    throw(errno.EFBIG, resp)
                # {"state": false, "errno": 50038, "error": "下载失败，含违规内容"}
                case 50038:
                    throw(errno.EACCES, resp)
                # {"state": false, "errno": 51011, "error": "不允许转存空文件夹"}
                case 51011:
                    raise P115OperationalError(errno.EPERM, resp)
                # {"state": false, "errno": 51012, "error": "已有文件正在解压中，请稍后再试"}
                case 51012:
                    throw(errno.EBUSY, resp)
                # {"state": false, "errno": 70004, "error": "文件上传不完整"}
                case 70004:
                    throw(errno.EISDIR, resp)
                # {"state": false, "errno": 91002, "error": "不能将文件复制到自身或其子目录下。"}
                case 91002:
                    throw(errno.ENOTSUP, resp)
                # {"state": false, "errno": 91004, "error": "操作的文件(夹)数量超过5万个"}
                case 91004:
                    throw(errno.ENOTSUP, resp)
                # {"state": false, "errno": 91005, "error": "空间不足，复制失败。"}
                case 91005:
                    throw(errno.ENOSPC, resp)
                # {"state": false, "errno": 231011, "error": "文件已删除，请勿重复操作"}
                case 231011:
                    throw(errno.ENOENT, resp)
                # {"state": false, "errno": 300104, "error": "文件超过200MB，暂不支持播放"}
                case 300104:
                    throw(errno.EFBIG, resp)
                # {"state": false, "errno": 300105, "error": "文件超过500MB，暂不支持添加到我听"}
                case 300105:
                    throw(errno.EFBIG, resp)
                # {"state": false, "errno": 320001, "error": "很抱歉,安全密钥不正确"}
                case 320001:
                    throw(errno.EINVAL, resp)
                # {"state": false, "errno": 590075, "error": "操作太频繁，请稍候再试"}
                case 590075:
                    throw(errno.EBUSY, resp)
                # {"state": false, "errno": 800001, "error": "目录不存在。"}
                case 800001:
                    throw(errno.ENOENT, resp)
                # {"state": false, "errno": 980006, "error": "404 Not Found"}
                case 980006:
                    throw(errno.ENOSYS, resp)
                # {"state": false, "errno": 990001, "error": "登陆超时，请重新登陆。"}
                case 990001:
                    # NOTE: 可能就是被下线了
                    throw(errno.EAUTH, resp)
                # {"state": false, "errno": 990002, "error": "参数错误。"}
                case 990002:
                    throw(errno.EINVAL, resp)
                # {"state": false, "errno": 990003, "error": "操作失败。"}
                case 990003:
                    raise P115OperationalError(errno.EIO, resp)
                # {"state": false, "errno": 990005, "error": "你的账号有类似任务正在处理，请稍后再试！"}
                case 990005:
                    throw(errno.EBUSY, resp)
                # {"state": false, "errno": 990009, "error": "删除[...]操作尚未执行完成，请稍后再试！"}
                # {"state": false, "errno": 990009, "error": "还原[...]操作尚未执行完成，请稍后再试！"}
                # {"state": false, "errno": 990009, "error": "复制[...]操作尚未执行完成，请稍后再试！"}
                # {"state": false, "errno": 990019, "error": "移动[...]操作尚未执行完成，请稍后再试！"}
                case 990009 | 990019:
                    throw(errno.EBUSY, resp)
                # {"state": false, "errno": 990023, "error": "操作的文件(夹)数量超过5万个"}
                case 990023:
                    throw(errno.ENOTSUP, resp)
                # {"state": false, "errno": 4100026, "error": "该文件分享链接不存在或已被删除"}
                case 4100026:
                    throw(errno.ENOENT, resp)
                # {"state": false, "errno": 4100030, "error": "接收文件过多"}
                case 4100030:
                    throw(errno.EIO, resp)
                # {"state": 0, "errno": 40100000, "error": "参数错误！"}
                # {"state": 0, "errno": 40100000, "error": "参数缺失"}
                case 40100000:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40101004, "error": "IP登录异常,请稍候再登录！"}
                case 40101004:
                    raise P115LoginError(errno.EAUTH, resp)
                # {"state": 0, "errno": 40101017, "error": "用户验证失败！"}
                case 40101017:
                    throw(errno.EAUTH, resp)
                # {"state": 0, "errno": 40101032, "error": "请重新登录"}
                case 40101032:
                    raise P115LoginError(errno.EAUTH, resp)
                #################################################################
                # Reference: https://www.yuque.com/115yun/open/rnq0cbz8tt7cu43i #
                #################################################################
                # {"state": 0, "errno": 40110000, "error": "请求异常需要重试"}
                case 40110000:
                    raise P115OperationalError(errno.EAGAIN, resp)
                # {"state": 0, "errno": 40140100, "error": "client_id 错误"}
                case 40140100:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140101, "error": "code_challenge 必填"}
                case 40140101:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140102, "error": "code_challenge_method 必须是 sha256、sha1、md5 之一"}
                case 40140102:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140103, "error": "sign 必填"}
                case 40140103:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140104, "error": "sign 签名失败"}
                case 40140104:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140105, "error": "生成二维码失败"}
                case 40140105:
                    raise P115OperationalError(errno.EIO, resp)
                # {"state": 0, "errno": 40140106, "error": "APP ID 无效"}
                case 40140106:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140107, "error": "应用不存在"}
                case 40140107:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140108, "error": "应用未审核通过"}
                case 40140108:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140109, "error": "应用已被停用"}
                case 40140109:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140110, "error": "应用已过期"}
                case 40140110:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140111, "error": "APP Secret 错误"}
                case 40140111:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140112, "error": "code_verifier 长度要求43~128位"}
                case 40140112:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140113, "error": "code_verifier 验证失败"}
                case 40140113:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140114, "error": "refresh_token 格式错误（防篡改）"}
                case 40140114:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140115, "error": "refresh_token 签名校验失败（防篡改）"}
                case 40140115:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140116, "error": "refresh_token 无效（已解除授权）"}
                case 40140116:
                    raise P115OperationalError(errno.EIO, resp)
                # {"state": 0, "errno": 40140117, "error": "access_token 刷新太频繁"}
                case 40140117:
                    throw(errno.EBUSY, resp)
                # {"state": 0, "errno": 40140118, "error": "开发者认证已过期"}
                case 40140118:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140119, "error": "refresh_token 已过期"}
                case 40140119:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140120, "error": "refresh_token 检验失败（防篡改）"}
                case 40140120:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140121, "error": "access_token 刷新失败"}
                case 40140121:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140122, "error": "超出授权应用个数上限"}
                case 40140122:
                    raise P115OpenAppAuthLimitExceeded(errno.EDQUOT, resp)
                # {"state": 0, "errno": 40140123, "error": "access_token 格式错误（防篡改）"}
                case 40140123:
                    raise P115AccessTokenError(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140124, "error": "access_token 签名校验失败（防篡改）"}
                case 40140124:
                    raise P115AccessTokenError(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140125, "error": "access_token 无效（已过期或者已解除授权）"}
                case 40140125:
                    raise P115AccessTokenError(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140126, "error": "access_token 校验失败（防篡改）"}
                case 40140126:
                    raise P115AccessTokenError(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140127, "error": "response_type 错误"}
                case 40140127:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140128, "error": "redirect_uri 缺少协议"}
                case 40140128:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140129, "error": "redirect_uri 缺少域名"}
                case 40140129:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140130, "error": "没有配置重定向域名"}
                case 40140130:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140131, "error": "redirect_uri 非法域名"}
                case 40140131:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140132, "error": "grant_type 错误"}
                case 40140132:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140133, "error": "client_secret 验证失败"}
                case 40140133:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140134, "error": "授权码 code 验证失败"}
                case 40140134:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140135, "error": "client_id 验证失败"}
                case 40140135:
                    throw(errno.EINVAL, resp)
                # {"state": 0, "errno": 40140136, "error": "redirect_uri 验证失败（防MITM）"}
                case 40140136:
                    throw(errno.EINVAL, resp)
        elif error := resp.get("error"):
            if error == "更新的数据为空":
                resp["state"] = True
                return resp
            if "文件不存在" in error or "目录不存在" in error:
                throw(errno.ENOENT, resp)
            elif "目录名称已存在" in error:
                throw(errno.EEXIST, resp)
            elif error == "更新的数据为空":
                throw(errno.EINVAL, resp)
        throw(errno.EIO, resp)
    if isinstance(resp, dict):
        return check(resp)
    elif isawaitable(resp):
        async def check_await() -> dict:
            return check(await resp)
        return check_await()
    throw(errno.EIO, resp)


class ClientRequestMixin:
    app_id: int = 0
    cookies_path: PurePath

    @locked_cacheproperty
    def cookies(self, /) -> CookieJar:
        """公用 cookiejar
        """
        return CookieJar()

    @locked_cacheproperty
    def headers(self, /) -> KeyLowerDict[str, str]:
        """公用请求头
        """
        return KeyLowerDict[str, str]({
            "accept": "*/*", 
            "accept-encoding": "gzip, deflate, br, zstd", 
            "connection": "keep-alive", 
            "user-agent": "Mozilla/5.0", 
        })

    @locked_cacheproperty
    def user_id(self, /) -> int:
        for cookie in self.cookies:
            if cookie.name == "UID":
                return int(cookie.value.partition("_")[0])
        if isinstance(self, P115OpenClient):
            resp = check_response(self.user_info_open())
            return int(resp["data"]["user_id"])
        raise LookupError("can't get user_id")

    @locked_cacheproperty
    def request_lock(self, /) -> Lock:
        return Lock()

    @locked_cacheproperty
    def request_alock(self, /) -> AsyncLock:
        return AsyncLock()

    @property
    def cookies_str(self, /) -> P115Cookies:
        """所有名为 *ID 的 cookie 值
        """
        return P115Cookies(self.cookies)

    def _read_cookies(self, encoding: str = "latin-1", /):
        if cookies_path := self.__dict__.get("cookies_path"):
            try:
                with cookies_path.open("rb") as f:
                    cookies = str(f.read(), encoding)
                if cookies:
                    update_cookies(self.cookies, cookies_to_dict(cookies))
            except OSError:
                pass

    def _write_cookies(self, encoding: str = "latin-1", /):
        if cookies_path := self.__dict__.get("cookies_path"):
            cookies_bytes = bytes(self.cookies_str, encoding)
            with cookies_path.open("wb") as f:
                f.write(cookies_bytes)

    def update_cookies(
        self, 
        cookies: None | str | CookieJar | BaseCookie | Mapping[str, Any] | Iterable[Any] | ClientRequestMixin = None, 
        /, 
    ):
        """更新 cookies（如果为 None 则是清空）
        """
        if isinstance(cookies, ClientRequestMixin):
            cookies = cookies.cookies
        if cookies is None:
            self.cookies.clear()
        else:
            if isinstance(cookies, str):
                cookies = cookies_to_dict(cookies.strip().rstrip(";"))
            if cookies:
                update_cookies(self.cookies, cookies)
                self._write_cookies()

    def request(
        self, 
        /, 
        url: str, 
        method: str = "GET", 
        payload: Any = None, 
        *, 
        check: bool = False, 
        ecdh_encrypt: bool = False, 
        request: None | Callable = None, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ):
        """执行网络请求

        :param url:     HTTP 的请求链接
        :param method:  HTTP 的请求方法
        :param payload: HTTP 的请求载体（``method`` 为 "POST" 或 "PUT" 时，视为 ``data``，否则视为 ``params``）
        :param ecdh_encrypt: 是否加密通信，如果是 open 接口则无效
        :param check: 是否检查响应
        :param request: HTTP 请求调用，如果为 None，则用默认设置
            如果传入调用，则必须至少能接受以下几个关键词参数（如果接收到了不用的参数，也要能自动忽略）：

            - url:     HTTP 的请求链接
            - method:  HTTP 的请求方法
            - params:  HTTP 的请求链接附加的查询参数
            - data:    HTTP 的请求体
            - json:    JSON 数据（往往未被序列化）作为请求体
            - files:   要用 multipart 上传的若干文件
            - headers: HTTP 的请求头
            - follow_redirects: 是否跟进重定向，默认值为 True
            - raise_for_status: 是否对响应码 >= 400 时抛出异常
            - cookies: 至少能接受 ``http.cookiejar.CookieJar`` 和 ``http.cookies.BaseCookie``，会因响应头的 "set-cookie" 而更新
            - parse:   解析 HTTP 响应的方法，默认会构建一个 Callable，会把响应的字节数据视为 JSON 进行反序列化解析

                - 如果为 None，则直接把响应对象返回
                - 如果为 ...(Ellipsis)，则把响应对象关闭后将其返回
                - 如果为 True，则根据响应头来确定把响应得到的字节数据解析成何种格式（反序列化），请求也会被自动关闭
                - 如果为 False，则直接返回响应得到的字节数据，请求也会被自动关闭
                - 如果为 Callable，则使用此调用来解析数据，接受 1-2 个位置参数，并把解析结果返回给 ``request`` 的调用者，请求也会被自动关闭
                    - 如果只接受 1 个位置参数，则把响应对象传给它
                    - 如果能接受 2 个位置参数，则把响应对象和响应得到的字节数据（响应体）传给它

        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 直接返回 ``request`` 执行请求后的返回值

        .. note:: 
            ``request`` 可以由不同的请求库来提供，下面是封装了一些模块

            1. `httpcore_request <https://pypi.org/project/httpcore_request/>`_，由 `httpcore <https://pypi.org/project/httpcore/>`_ 封装，支持同步和异步请求

                .. code:: python

                    from httpcore_request import request

            2. `httpx_request <https://pypi.org/project/httpx_request/>`_，由 `httpx <https://pypi.org/project/httpx/>`_ 封装，支持同步和异步请求

                .. code:: python

                    from httpx_request import request

            3. `http_client_request <https://pypi.org/project/http_client_request/>`_，由 `http.client <https://docs.python.org/3/library/http.client.html>`_ 封装，支持同步请求

                .. code:: python

                    from http_client_request import request

            4. `python-urlopen <https://pypi.org/project/python-urlopen/>`_，由 `urllib.request.urlopen <https://docs.python.org/3/library/urllib.request.html#urllib.request.urlopen>`_ 封装，支持同步请求

                .. code:: python

                    from urlopen import request

            5. `urllib3_request <https://pypi.org/project/urllib3_request/>`_，由 `urllib3 <https://pypi.org/project/urllib3/>`_ 封装，支持同步请求

                .. code:: python

                    from urllib3_request import request

            6. `requests_request <https://pypi.org/project/requests_request/>`_，由 `requests <https://pypi.org/project/requests/>`_ 封装，支持同步请求

                .. code:: python

                    from requests_request import request

            7. `aiohttp_client_request <https://pypi.org/project/aiohttp_client_request/>`_，由 `aiohttp <https://pypi.org/project/aiohttp/>`_ 封装，支持异步请求

                .. code:: python

                    from aiohttp_client_request import request

            8. `blacksheep_client_request <https://pypi.org/project/blacksheep_client_request/>`_，由 `blacksheep <https://pypi.org/project/blacksheep/>`_ 封装，支持异步请求

                .. code:: python

                    from blacksheep_client_request import request

            9. `asks_request <https://pypi.org/project/asks_request/>`_，由 `asks <https://pypi.org/project/asks/>`_ 封装，支持异步请求

                .. code:: python

                    from asks_request import request

            10. `pycurl_request <https://pypi.org/project/pycurl_request/>`_，由 `pycurl <https://pypi.org/project/pycurl/>`_ 封装，支持同步请求

                .. code:: python

                    from pycurl_request import request

            11. `curl_cffi_request <https://pypi.org/project/curl_cffi_request/>`_，由 `curl_cffi <https://pypi.org/project/curl_cffi/>`_ 封装，支持同步和异步请求

                .. code:: python

                    from curl_cffi_request import request

            12. `aiosonic_request <https://pypi.org/project/aiosonic_request/>`_，由 `aiosonic <https://pypi.org/project/aiosonic/>`_ 封装，支持异步请求

                .. code:: python

                    from aiosonic_request import request

            13. `tornado_client_request <https://pypi.org/project/tornado_client_request/>`_，由 `tornado <https://www.tornadoweb.org/en/latest/httpclient.html>`_ 封装，支持同步和异步请求

                .. code:: python

                    from tornado_client_request import request

            14. `urllib3_future_request <https://pypi.org/project/urllib3_future_request/>`_，由 `urllib3.future <https://urllib3future.readthedocs.io/en/latest/>`_ 封装，支持同步和异步请求

                .. code:: python

                    from urllib3_future_request import request

            15. `niquests_request <https://pypi.org/project/niquests_request/>`_，由 `niquests <https://niquests.readthedocs.io/en/latest/>`_ 封装，支持同步和异步请求

                .. code:: python

                    from niquests_request import request
        """
        headers = self.headers.copy()
        headers.update(request_kwargs.pop("headers", None) or ())
        request, request_kwargs = get_request(
            url=url, 
            method=method, 
            payload=payload, 
            headers=headers, 
            ecdh_encrypt=ecdh_encrypt, 
            request=request, 
            **request_kwargs, 
        )
        headers = request_kwargs["headers"]
        if URL(url).path.startswith("/open/"):
            if "authorization" not in headers:
                assert isinstance(self, P115OpenClient)
                def gen_step():
                    if async_:
                        lock: Lock | AsyncLock = self.request_alock
                    else:
                        lock = self.request_lock
                    if not hasattr(self, "access_token"):
                        yield lock.acquire()
                        try:
                            if not getattr(self, "access_token", None):
                                if getattr(self, "refresh_token", None):
                                    yield self.refresh_access_token(async_=async_)
                                elif hasattr(self, "login_another_open"):
                                    yield self.login_another_open(replace=True, async_=async_)
                                else:
                                    raise RuntimeError("can't get access token")
                        finally:
                            lock.release()
                    while True:
                        access_token = self.access_token
                        headers["authorization"] = "Bearer " + access_token
                        resp = yield request(async_=async_, **request_kwargs)
                        if resp.get("errno") != 40140125:
                            if check:
                                return check_response(resp)
                            return resp
                        yield lock.acquire()
                        try:
                            if access_token == self.access_token:
                                yield self.refresh_access_token(async_=async_)
                        finally:
                            lock.release()
                return run_gen_step(gen_step, async_)
        elif "cookie" not in headers:
            request_kwargs["cookies"] = self.cookies
        resp = request(async_=async_, **request_kwargs)
        if check:
            return check_response(resp)
        return resp

    def open(
        self, 
        url: str, 
        /, 
        start: int | str = 0, 
        urlopen: None | Callable = None, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ):
        """打开下载链接，返回响应对象

        :param url: 下载链接
        :param start: 开始索引，从 0 开始
        :param async_: 是否异步
        :param request_kwargs: 其它请求参数

        :return: 响应对象
        """
        headers = request_kwargs["headers"] = dict(request_kwargs.get("headers") or ())
        headers.setdefault("user-agent", "")
        if isinstance(start, str):
            if not start.startswith("bytes="):
                start = "bytes=" + start
        elif start >= 0:
            start = f"bytes={start}-"
        else:
            start = f"bytes={start}"
        headers["range"] = start
        if isinstance(url, P115URL):
            headers.update(url.get("headers") or ())
        if urlopen is None:
            from urllib3_future_request import request as urlopen
        urlopen = cast(Callable, urlopen)
        return urlopen(url, async_=async_, **request_kwargs)

    ########## App API ##########

    @overload
    @staticmethod
    def app_area_list(
        base_url: str | Callable[[], str] = "https://cdnres.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs
    ) -> dict:
        ...
    @overload
    @staticmethod
    def app_area_list(
        base_url: str | Callable[[], str] = "https://cdnres.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs
    ) -> Coroutine[Any, Any, dict]:
        ...
    @staticmethod
    def app_area_list(
        base_url: str | Callable[[], str] = "https://cdnres.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取地区编码列表

        GET https://cdnres.115.com/my/m_r/setting_new/js/ylmf_area.js
        """
        api = complete_url("/my/m_r/setting_new/js/ylmf_area.js", base_url=base_url)
        def iter_area(data: dict, /) -> Iterator[tuple[int, str]]:
            for code, detail in data.items():
                if isinstance(code, str):
                    continue
                if isinstance(detail, dict):
                    yield code, detail["n"]
                    for key in ("c", "t"):
                        if key in detail and detail[key]:
                            yield from iter_area(detail[key])
                            break
                else:
                    yield code, detail
        def parse(_, content, /):
            data_str = cast(Match[str], CRE_AREA_DATA_search(content.decode("utf-8")))[0]
            data = eval(data_str, {"n": "n", "c": "c", "t": "t", "l": "l"})
            return {"state": True, "data": list(iter_area(data))}
        request_kwargs.setdefault("parse", parse)
        request, request_kwargs = get_request(url=api, **request_kwargs)
        return request(async_=async_, **request_kwargs)

    @overload
    @staticmethod
    def app_face_codes(
        base_url: str | Callable[[], str] = "https://my.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    @staticmethod
    def app_face_codes(
        base_url: str | Callable[[], str] = "https://my.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    @staticmethod
    def app_face_codes(
        base_url: str | Callable[[], str] = "https://my.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取表情包

        GET https://my.115.com/api/face_code.js
        """
        api = complete_url("/api/face_code.js", base_url=base_url)
        def parse(_, content, /):
            return json_loads(content[25:-1])
        request_kwargs.setdefault("parse", parse)
        request, request_kwargs = get_request(url=api, **request_kwargs)
        return request(async_=async_, **request_kwargs)

    @overload
    @staticmethod
    def app_publick_key(
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://passportapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs
    ) -> dict:
        ...
    @overload
    @staticmethod
    def app_publick_key(
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://passportapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs
    ) -> Coroutine[Any, Any, dict]:
        ...
    @staticmethod
    def app_publick_key(
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://passportapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取 RSA 加密公钥，用于某些情况下的加密

        GET https://passportapi.115.com/app/1.0/{app}/1.0/login/getKey

        .. note::
            返回的公钥是签名证书，并经过 BASE64 处理，可用下面步骤还原

            .. code:: python

                from base64 import b64decode
                from p115client import P115Client

                resp = P115Client.app_publick_key()
                perm = b64decode(resp["data"]["key"])

                # pip install pycryptodome
                from Crypto.PublicKey import RSA

                pubkey = RSA.import_key(perm)
                print(repr(pubkey))
        """
        api = complete_url(f"/app/1.0/{app}/1.0/login/getKey", base_url=base_url)
        request, request_kwargs = get_request(url=api, **request_kwargs)
        return request(async_=async_, **request_kwargs)

    @overload
    @staticmethod
    def app_version_list(
        base_url: str | Callable[[], str] = "https://appversion.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs
    ) -> dict:
        ...
    @overload
    @staticmethod
    def app_version_list(
        base_url: str | Callable[[], str] = "https://appversion.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs
    ) -> Coroutine[Any, Any, dict]:
        ...
    @staticmethod
    def app_version_list(
        base_url: str | Callable[[], str] = "https://appversion.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取当前各平台最新版 115 app 下载链接

        GET https://appversion.115.com/1.0/web/1.0/api/chrome
        """
        api = complete_url("/1.0/web/1.0/api/chrome", base_url=base_url)
        request, request_kwargs = get_request(url=api, **request_kwargs)
        return request(async_=async_, **request_kwargs)

    @overload
    @staticmethod
    def app_version_list2(
        base_url: str | Callable[[], str] = "https://appversion.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs
    ) -> dict:
        ...
    @overload
    @staticmethod
    def app_version_list2(
        base_url: str | Callable[[], str] = "https://appversion.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs
    ) -> Coroutine[Any, Any, dict]:
        ...
    @staticmethod
    def app_version_list2(
        base_url: str | Callable[[], str] = "https://appversion.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取当前各平台最新版 115 app 下载链接

        GET https://appversion.115.com/1.0/web/1.0/api/getMultiVer
        """
        api = complete_url("/1.0/web/1.0/api/getMultiVer", base_url=base_url)
        request, request_kwargs = get_request(url=api, **request_kwargs)
        return request(async_=async_, **request_kwargs)

    ########## Qrcode API ##########

    @overload
    @staticmethod
    def login_authorize_open(
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    @staticmethod
    def login_authorize_open(
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    @staticmethod
    def login_authorize_open(
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """授权码方式请求开放接口应用授权

        GET https://qrcodeapi.115.com/open/authorize

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/okr2cq0wywelscpe#EiOrD

        .. note::
            同一个开放应用 id，最多同时有 3 个登入，如果有新的登录，则自动踢掉较早的那一个

        :payload:
            - client_id: int | str 💡 AppID
            - redirect_uri: str 💡 授权成功后重定向到指定的地址并附上授权码 code，需要先到 https://open.115.com/ 应用管理应用域名设置
            - response_type: str = "code" 💡 授权模式，固定为 code，表示授权码模式
            - state: int | str = <default> 💡 随机值，会通过 redirect_uri 原样返回，可用于验证以防 MITM 和 CSRF
        """
        api = complete_url("/open/authorize", base_url=base_url)
        request_kwargs["follow_redirects"] = False
        def parse(resp, content, /):
            if get_status_code(resp) == 302:
                return {
                    "state": True, 
                    "url": resp.headers["location"], 
                    "data": dict(parse_qsl(urlsplit(resp.headers["location"]).query)), 
                    "headers": dict(resp.headers), 
                }
            else:
                return json_maybe_decrypt_loads(content)
        request_kwargs.setdefault("parse", parse)
        request, request_kwargs = get_request(
            url=api, params={"response_type": "code", **payload}, **request_kwargs)
        return request(async_=async_, **request_kwargs)

    @overload
    @staticmethod
    def login_authorize_access_token_open(
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    @staticmethod
    def login_authorize_access_token_open(
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    @staticmethod
    def login_authorize_access_token_open(
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """用授权码获取开放接口应用的 access_token

        POST https://qrcodeapi.115.com/open/authCodeToToken

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/okr2cq0wywelscpe#JnDgl

        :payload:
            - client_id: int | str 💡 AppID
            - client_secret: str 💡 AppSecret
            - code: str 💡 授权码，/open/authCodeToToken 重定向地址里面
            - redirect_uri: str 💡 与 /open/authCodeToToken 传的 redirect_uri 一致，可用于验证以防 MITM 和 CSRF
            - grant_type: str = "authorization_code" 💡 授权类型，固定为 authorization_code，表示授权码类型
        """
        api = complete_url("/open/authCodeToToken", base_url=base_url)
        request, request_kwargs = get_request(
            url=api, 
            method="POST", 
            data={"grant_type": "authorization_code", **payload}, 
            **request_kwargs, 
        )
        return request(async_=async_, **request_kwargs)

    @overload
    @staticmethod
    def login_qrcode(
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> bytes:
        ...
    @overload
    @staticmethod
    def login_qrcode(
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, bytes]:
        ...
    @staticmethod
    def login_qrcode(
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> bytes | Coroutine[Any, Any, bytes]:
        """下载登录二维码图片

        GET https://qrcodeapi.115.com/api/1.0/{app}/1.0/qrcode

        :param uid: 二维码的 uid

        :return: 图片的二进制数据（PNG 图片）
        """
        api = complete_url(f"/api/1.0/{app}/1.0/qrcode", base_url=base_url)
        if isinstance(payload, str):
            payload = {"uid": payload}
        request_kwargs.setdefault("parse", False)
        request, request_kwargs = get_request(url=api, **request_kwargs)
        return request(async_=async_, **request_kwargs)

    @overload
    @staticmethod
    def login_qrcode_access_token_open(
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    @staticmethod
    def login_qrcode_access_token_open(
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    @staticmethod
    def login_qrcode_access_token_open(
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """绑定扫码并获取开放平台应用的 access_token 和 refresh_token

        POST https://qrcodeapi.115.com/open/deviceCodeToToken

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/shtpzfhewv5nag11#QCCVQ

        :payload:
            - uid: str
            - code_verifier: str = <default> 💡 默认字符串是 64 个 "0"
        """
        api = complete_url("/open/deviceCodeToToken", base_url=base_url)
        if isinstance(payload, str):
            payload = {"uid": payload, "code_verifier": _default_code_verifier}
        request, request_kwargs = get_request(
            url=api, method="POST", data=payload, **request_kwargs)
        return request(async_=async_, **request_kwargs)

    @overload
    def login_qrcode_scan(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def login_qrcode_scan(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def login_qrcode_scan(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """扫描二维码，payload 数据取自 ``login_qrcode_token`` 接口响应

        GET https://qrcodeapi.115.com/api/2.0/prompt.php

        :payload:
            - uid: str
        """
        api = complete_url("/api/2.0/prompt.php", base_url=base_url)
        if isinstance(payload, str):
            payload = {"uid": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def login_qrcode_scan_cancel(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def login_qrcode_scan_cancel(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def login_qrcode_scan_cancel(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """取消扫描二维码，payload 数据取自 ``login_qrcode_scan`` 接口响应

        GET https://qrcodeapi.115.com/api/2.0/cancel.php

        :payload:
            - key: str
            - uid: str
            - client: int = 0
        """
        api = complete_url("/api/2.0/cancel.php", base_url=base_url)
        if isinstance(payload, str):
            payload = {"key": payload, "uid": payload, "client": 0}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def login_qrcode_scan_confirm(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def login_qrcode_scan_confirm(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def login_qrcode_scan_confirm(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """确认扫描二维码，payload 数据取自 ``login_qrcode_scan`` 接口响应

        GET https://qrcodeapi.115.com/api/2.0/slogin.php

        :payload:
            - key: str
            - uid: str
            - client: int = 0
        """
        api = complete_url("/api/2.0/slogin.php", base_url=base_url)
        if isinstance(payload, str):
            payload = {"key": payload, "uid": payload, "client": 0}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    @staticmethod
    def login_qrcode_scan_result(
        uid: str, 
        app: str = "alipaymini", 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    @staticmethod
    def login_qrcode_scan_result(
        uid: str, 
        app: str = "alipaymini", 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    @staticmethod
    def login_qrcode_scan_result(
        uid: str, 
        app: str = "alipaymini", 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取扫码登录的结果，包含 cookie

        POST https://qrcodeapi.115.com/app/1.0/{app}/1.0/login/qrcode/

        .. note::
            如果报错“IP登录异常”，那么要到次日零点才能解禁（用 VPN 可以绕过），其中尤其是 ``app="web"`` 最容易遇到此问题

        :param uid: 扫码的 uid
        :param app: 绑定的 app
        :param request: 自定义请求函数
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 接口返回值
        """
        if app == "desktop":
            app = "web"
        elif app in ("windows", "mac", "linux"):
            app = "os_" + app
        elif app in ("ios", "qios", "ipad", "qipad"):
            headers = request_kwargs["headers"] = dict(request_kwargs.get("headers", ()))
            match app:
                case "ios":
                    headers["user-agent"] = "UPhone/1.0.0"
                case "qios":
                    headers["user-agent"] = "OfficePhone/1.0.0"
                case "ipad":
                    headers["user-agent"] = "UPad/1.0.0"
                case "qipad":
                    headers["user-agent"] = "OfficePad/1.0.0"
            app = "ios"
        api = complete_url(f"/app/1.0/{app}/1.0/login/qrcode/", base_url=base_url)
        request, request_kwargs = get_request(
            url=api, method="POST", data={"account": uid}, **request_kwargs)
        return request(async_=async_, **request_kwargs)

    @overload
    @staticmethod
    def login_qrcode_scan_status(
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    @staticmethod
    def login_qrcode_scan_status(
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    @staticmethod
    def login_qrcode_scan_status(
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取二维码的状态（未扫描、已扫描、已登录、已取消、已过期等），payload 数据取自 ``login_qrcode_token`` 接口响应

        GET https://qrcodeapi.115.com/get/status/

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/shtpzfhewv5nag11#lAsp2

        :payload:
            - uid: str
            - time: int
            - sign: str
        """
        api = complete_url("/get/status/", base_url=base_url)
        request, request_kwargs = get_request(
            url=api, params=payload, **request_kwargs)
        return request(async_=async_, **request_kwargs)

    @overload
    @staticmethod
    def login_qrcode_token(
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    @staticmethod
    def login_qrcode_token(
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    @staticmethod
    def login_qrcode_token(
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取登录二维码，扫码可用

        GET https://qrcodeapi.115.com/api/1.0/{app}/1.0/token/
        """
        api = complete_url(f"/api/1.0/{app}/1.0/token/", base_url=base_url)
        request, request_kwargs = get_request(url=api, **request_kwargs)
        return request(async_=async_, **request_kwargs)

    @overload
    @staticmethod
    def login_qrcode_token_open(
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    @staticmethod
    def login_qrcode_token_open(
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    @staticmethod
    def login_qrcode_token_open(
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取开放平台的登录二维码，扫码可用，采用 PKCE (Proof Key for Code Exchange)

        POST https://qrcodeapi.115.com/open/authDeviceCode

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/shtpzfhewv5nag11#WzRhM

        .. tip::
            此接口也可用于检测 app_id 是否可用

        .. note::
            同一个开放应用 id，最多同时有 3 个登入，如果有新的登录，则自动踢掉较早的那一个

        .. note::
            code_challenge 默认用的字符串为 64 个 0，hash 算法为 md5

        .. tip::
            如果仅仅想要检查 AppID 是否有效，可以用如下的代码：

            .. code:: python

                from p115client import P115Client

                app_id = 100195125
                response = P115Client.login_qrcode_token_open(app_id)
                if response["code"]:
                    print("无效 AppID:", app_id, "因为:", response["error"])
                else:
                    print("有效 AppID:", app_id)

        .. tip::
            如果想要罗列出所有可用的 AppID，可以用如下的代码：

            .. code:: python

                from itertools import count
                from p115client import P115Client

                get_qrcode_token = P115Client.login_qrcode_token_open
                for app_id in count(100195125, 2):
                    response = get_qrcode_token(app_id)
                    if not response["code"]:
                        print(app_id)

        :payload:
            - client_id: int | str 💡 AppID
            - code_challenge: str = <default> 💡 PKCE 相关参数，计算方式如下

                .. code:: python

                    from base64 import b64encode
                    from hashlib import sha256
                    from secrets import token_bytes

                    # code_verifier 可以是 43~128 位随机字符串
                    code_verifier = token_bytes(64).hex()
                    code_challenge = b64encode(sha256(code_verifier.encode()).digest()).decode()

            - code_challenge_method: str = <default> 💡 计算 ``code_challenge`` 的 hash 算法，支持 "md5", "sha1", "sha256"
        """
        api = complete_url("/open/authDeviceCode", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {
                "client_id": payload, 
                "code_challenge": _default_code_challenge, 
                "code_challenge_method": _default_code_challenge_method, 
            }
        request, request_kwargs = get_request(
            url=api, method="POST", data=payload, **request_kwargs)
        return request(async_=async_, **request_kwargs)

    @overload
    @staticmethod
    def login_refresh_token_open(
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    @staticmethod
    def login_refresh_token_open(
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    @staticmethod
    def login_refresh_token_open(
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """用一个 refresh_token 去获取新的 access_token 和 refresh_token，然后原来的 refresh_token 作废

        POST https://qrcodeapi.115.com/open/refreshToken

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/shtpzfhewv5nag11#ve54x

            https://www.yuque.com/115yun/open/opnx8yezo4at2be6

        :payload:
            - refresh_token: str
        """
        api = complete_url("/open/refreshToken", base_url=base_url)
        if isinstance(payload, str):
            payload = {"refresh_token": payload}
        request, request_kwargs = get_request(
            url=api, method="POST", data=payload, **request_kwargs)
        return request(async_=async_, **request_kwargs)

    @overload
    @classmethod
    def login_with_qrcode(
        cls, 
        /, 
        app: None | str = "", 
        console_qrcode: bool = True, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    @classmethod
    def login_with_qrcode(
        cls, 
        /, 
        app: None | str = "", 
        console_qrcode: bool = True, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    @classmethod
    def login_with_qrcode(
        cls, 
        /, 
        app: None | str = "", 
        console_qrcode: bool = True, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """二维码扫码登录

        .. hint::
            仅获取响应，如果需要更新此 ``client`` 的 `cookies`，请直接用 ``login`` 方法

        :param app: 扫二维码后绑定的 ``app`` （或者叫 `device`）
        :param console_qrcode: 在命令行输出二维码，否则在浏览器中打开
        :param base_url: 接口的基地址
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 响应信息，如果 ``app`` 为 None 或 ""，则返回二维码信息，否则返回绑定扫码后的信息（包含 cookies）

        -----

        :设备列表如下:

        +-------+----------+------------+----------------------+
        | No.   | ssoent   | app        | description          |
        +=======+==========+============+======================+
        | 01    | A1       | web        | 115生活_网页端       |
        +-------+----------+------------+----------------------+
        | --    | A1       | desktop    | 115浏览器            |
        +-------+----------+------------+----------------------+
        | --    | A2       | ?          | 未知: android        |
        +-------+----------+------------+----------------------+
        | --    | A3       | ?          | 未知: ios            |
        +-------+----------+------------+----------------------+
        | --    | A4       | ?          | 未知: ipad           |
        +-------+----------+------------+----------------------+
        | --    | B1       | ?          | 未知: android        |
        +-------+----------+------------+----------------------+
        | 02    | D1       | ios        | 115生活_苹果端       |
        +-------+----------+------------+----------------------+
        | 03    | D2       | bios       | 未知: ios            |
        +-------+----------+------------+----------------------+
        | 04    | D3       | 115ios     | 115_苹果端           |
        +-------+----------+------------+----------------------+
        | 05    | F1       | android    | 115生活_安卓端       |
        +-------+----------+------------+----------------------+
        | 06    | F2       | bandroid   | 未知: android        |
        +-------+----------+------------+----------------------+
        | 07    | F3       | 115android | 115_安卓端           |
        +-------+----------+------------+----------------------+
        | 08    | H1       | ipad       | 115生活_苹果平板端   |
        +-------+----------+------------+----------------------+
        | 09    | H2       | bipad      | 未知: ipad           |
        +-------+----------+------------+----------------------+
        | 10    | H3       | 115ipad    | 115_苹果平板端       |
        +-------+----------+------------+----------------------+
        | 11    | I1       | tv         | 115生活_安卓电视端   |
        +-------+----------+------------+----------------------+
        | 12    | I2       | apple_tv   | 115生活_苹果电视端   |
        +-------+----------+------------+----------------------+
        | 13    | M1       | qandriod   | 115管理_安卓端       |
        +-------+----------+------------+----------------------+
        | 14    | N1       | qios       | 115管理_苹果端       |
        +-------+----------+------------+----------------------+
        | 15    | O1       | qipad      | 115管理_苹果平板端   |
        +-------+----------+------------+----------------------+
        | 16    | P1       | os_windows | 115生活_Windows端    |
        +-------+----------+------------+----------------------+
        | 17    | P2       | os_mac     | 115生活_macOS端      |
        +-------+----------+------------+----------------------+
        | 18    | P3       | os_linux   | 115生活_Linux端      |
        +-------+----------+------------+----------------------+
        | 19    | R1       | wechatmini | 115生活_微信小程序端 |
        +-------+----------+------------+----------------------+
        | 20    | R2       | alipaymini | 115生活_支付宝小程序 |
        +-------+----------+------------+----------------------+
        | 21    | S1       | harmony    | 115_鸿蒙端           |
        +-------+----------+------------+----------------------+
        """
        def gen_step():
            nonlocal console_qrcode
            resp = yield cls.login_qrcode_token(
                async_=async_, 
                base_url=base_url, 
                **request_kwargs, 
            )
            qrcode_token = resp["data"]
            login_uid = qrcode_token["uid"]
            qrcode = qrcode_token.pop("qrcode", "")
            if not qrcode:
                qrcode = "https://115.com/scan/dg-" + login_uid
            if not console_qrcode:
                try:
                    from startfile import startfile, startfile_async
                except ImportError:
                    console_qrcode = True
            if console_qrcode:
                from qrcode import QRCode # type: ignore
                qr = QRCode(border=1)
                qr.add_data(qrcode)
                qr.print_ascii(tty=isatty(1))
            else:
                url = complete_url("/api/1.0/web/1.0/qrcode", base_url=base_url, query={"uid": login_uid})
                if async_:
                    yield startfile_async(url)
                else:
                    startfile(url)
            while True:
                try:
                    resp = yield cls.login_qrcode_scan_status(
                        qrcode_token, 
                        base_url=base_url, 
                        async_=async_, 
                        **request_kwargs, 
                    )
                except Exception:
                    continue
                match resp["data"].get("status"):
                    case 0:
                        print("[status=0] qrcode: waiting")
                    case 1:
                        print("[status=1] qrcode: scanned")
                    case 2:
                        print("[status=2] qrcode: signed in")
                        break
                    case -1:
                        raise P115LoginError(errno.EAUTH, "[status=-1] qrcode: expired")
                    case -2:
                        raise P115LoginError(errno.EAUTH, "[status=-2] qrcode: canceled")
                    case _:
                        raise P115LoginError(errno.EAUTH, f"qrcode: aborted with {resp!r}")
            if app:
                return cls.login_qrcode_scan_result(
                    login_uid, 
                    app=app, 
                    base_url=base_url, 
                    async_=async_, 
                    **request_kwargs, 
                )
            else:
                return qrcode_token
        return run_gen_step(gen_step, async_)

    @overload
    @classmethod
    def login_with_app_id(
        cls, 
        /, 
        app_id: int, 
        console_qrcode: bool = True, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    @classmethod
    def login_with_app_id(
        cls, 
        /, 
        app_id: int, 
        console_qrcode: bool = True, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    @classmethod
    def login_with_app_id(
        cls, 
        /, 
        app_id: int, 
        console_qrcode: bool = True, 
        base_url: str | Callable[[], str] = "https://qrcodeapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """二维码扫码登录开放平台

        :param console_qrcode: 在命令行输出二维码，否则在浏览器中打开
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 响应信息
        """
        def gen_step():
            nonlocal console_qrcode
            resp = yield cls.login_qrcode_token_open(
                app_id, 
                base_url=base_url, 
                async_=async_, 
                **request_kwargs, 
            )
            qrcode_token = resp["data"]
            login_uid = qrcode_token["uid"]
            qrcode = qrcode_token.pop("qrcode", "")
            if not qrcode:
                qrcode = "https://115.com/scan/dg-" + login_uid
            if not console_qrcode:
                try:
                    from startfile import startfile, startfile_async
                except ImportError:
                    console_qrcode = True
            if console_qrcode:
                from qrcode import QRCode # type: ignore
                qr = QRCode(border=1)
                qr.add_data(qrcode)
                qr.print_ascii(tty=isatty(1))
            else:
                url = complete_url("/api/1.0/web/1.0/qrcode", base_url=base_url, query={"uid": login_uid})
                if async_:
                    yield startfile_async(url)
                else:
                    startfile(url)
            while True:
                try:
                    resp = yield cls.login_qrcode_scan_status(
                        qrcode_token, 
                        base_url=base_url, 
                        async_=async_, 
                        **request_kwargs, 
                    )
                except Exception:
                    continue
                match resp["data"].get("status"):
                    case 0:
                        print("[status=0] qrcode: waiting")
                    case 1:
                        print("[status=1] qrcode: scanned")
                    case 2:
                        print("[status=2] qrcode: signed in")
                        break
                    case -1:
                        raise P115LoginError(errno.EAUTH, "[status=-1] qrcode: expired")
                    case -2:
                        raise P115LoginError(errno.EAUTH, "[status=-2] qrcode: canceled")
                    case _:
                        raise P115LoginError(errno.EAUTH, f"qrcode: aborted with {resp!r}")
            return cls.login_qrcode_access_token_open(
                login_uid, 
                base_url=base_url, 
                async_=async_, 
                **request_kwargs, 
            )
        return run_gen_step(gen_step, async_)

    ########## Upload API ##########

    @overload
    @staticmethod
    def upload_gettoken(
        base_url: str | Callable[[], str] = "https://uplb.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    @staticmethod
    def upload_gettoken(
        base_url: str | Callable[[], str] = "https://uplb.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    @staticmethod
    def upload_gettoken(
        base_url: str | Callable[[], str] = "https://uplb.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取阿里云 OSS 的 token（上传凭证）

        GET https://uplb.115.com/3.0/gettoken.php
        """
        api = complete_url("/3.0/gettoken.php", base_url=base_url)
        request, request_kwargs = get_request(url=api, **request_kwargs)
        return request(async_=async_, **request_kwargs)

    @overload
    @staticmethod
    def upload_url(
        base_url: str | Callable[[], str] = "https://uplb.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    @staticmethod
    def upload_url(
        base_url: str | Callable[[], str] = "https://uplb.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    @staticmethod
    def upload_url(
        base_url: str | Callable[[], str] = "https://uplb.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取用于上传的一些 http 接口，此接口具有一定幂等性，请求一次，然后把响应记下来即可

        GET https://uplb.115.com/3.0/getuploadinfo.php

        :response:
            - endpoint: 此接口用于上传文件到阿里云 OSS 
            - gettokenurl: 上传前需要用此接口获取 token
        """
        api = complete_url("/3.0/getuploadinfo.php", base_url=base_url)
        request, request_kwargs = get_request(url=api, **request_kwargs)
        return request(async_=async_, **request_kwargs)


# TODO: 支持对 access_token 和 refresh_token 保存到本地，就像 cookies 一样
class P115OpenClient(ClientRequestMixin):
    """115 的客户端对象

    .. admonition:: Reference

        https://www.yuque.com/115yun/open

    :param access_token: 访问令牌
    :param refresh_token: 刷新令牌
    :param app_id: 授权的 open 应用的 AppID
    :param console_qrcode: 需要扫码登录时，是否在命令行输出二维码
    """
    def __init__(
        self, 
        /, 
        access_token: str = "", 
        refresh_token: str = "", 
        app_id: int = 0, 
        console_qrcode: bool = True, 
    ):
        self.init(
            access_token=access_token, 
            refresh_token=refresh_token, 
            app_id=app_id, 
            console_qrcode=console_qrcode, 
            instance=self, 
        )

    def __eq__(self, other, /) -> bool:
        return type(self) is type(other) and self.user_id == other.user_id

    def __hash__(self, /) -> int:
        return id(self)

    def __repr__(self, /) -> str:
        cls = type(self)
        return f"<{cls.__module__}.{cls.__qualname__}(app_id={self.app_id}) at {hex(id(self))}>"

    @overload
    @classmethod
    def init(
        cls, 
        /, 
        access_token: str = "", 
        refresh_token: str = "", 
        app_id: int = 0, 
        console_qrcode: bool = True, 
        instance: None | Self = None, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> Self:
        ...
    @overload
    @classmethod
    def init(
        cls, 
        /, 
        access_token: str = "", 
        refresh_token: str = "", 
        app_id: int = 0, 
        console_qrcode: bool = True, 
        instance: None | Self = None, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, Self]:
        ...
    @classmethod
    def init(
        cls, 
        /, 
        access_token: str = "", 
        refresh_token: str = "", 
        app_id: int = 0, 
        console_qrcode: bool = True, 
        instance: None | Self = None, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> Self | Coroutine[Any, Any, Self]:
        def gen_step():
            if instance is None:
                self = cls.__new__(cls)
            else:
                self = instance
            self.app_id = app_id
            self.access_token = access_token
            self.refresh_token = refresh_token
            if not access_token:
                if refresh_token:
                    resp = yield self.login_refresh_token_open(
                        refresh_token, 
                        async_=async_, 
                        **request_kwargs, 
                    )
                else:
                    resp = yield self.login_with_app_id(
                        app_id or 100195125, 
                        console_qrcode=console_qrcode, 
                        async_=async_, 
                        **request_kwargs, 
                    )
                check_response(resp)
                data = resp["data"]
                self.refresh_token = data["refresh_token"]
                self.access_token  = data["access_token"]
            return self
        return run_gen_step(gen_step, async_)

    @locked_cacheproperty
    def pickcode_stable_point(self, /) -> str:
        """获取 pickcode 的不动点，或者也叫本征值

        .. todo::
            不动点可能和用户 id 有某种联系，但目前样本不足，难以推断，以后再尝试分析
        """
        from .util import get_stable_point, set_stable_point
        try:
            return get_stable_point(self.user_id)
        except KeyError:
            resp = self.fs_files({"show_dir": 1, "limit": 1, "cid": 0})
            check_response(resp)
            if resp["data"]:
                info = resp["data"][0]
                pickcode = info["pc"]
            else:
                if hasattr(self, "upload_file_sample"):
                    resp = self.upload_file_sample(b"", "U_120_0")
                    check_response(resp)
                    pickcode = resp["data"]["pick_code"]
                else:
                    resp = self.upload_file(b"", filename="test")
                    pickcode = resp["data"]["pickcode"]
                    try:
                        self.fs_delete(self.to_id(pickcode))
                    except Exception:
                        pass
            return set_stable_point(self.user_id, pickcode)

    @overload
    def refresh_access_token(
        self, 
        /, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def refresh_access_token(
        self, 
        /, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def refresh_access_token(
        self, 
        /, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """更新 access_token 和 refresh_token （⚠️ 目前是 7200 秒内就要求刷新一次）
        """
        def gen_step():
            if refresh_token := getattr(self, "refresh_token", ""):
                resp = yield self.login_refresh_token_open(
                    refresh_token, 
                    async_=async_, 
                    **request_kwargs, 
                )
            elif app_id := self.app_id:
                if hasattr(self, "login_with_open"):
                    resp = yield self.login_with_open(
                        app_id, 
                        async_=async_, 
                        **request_kwargs, 
                    )
                else:
                    resp = yield self.login_with_app_id(
                        app_id, 
                        console_qrcode=True, 
                        async_=async_, 
                        **request_kwargs, 
                    )
            else:
                raise RuntimeError("no ``refresh_token`` or ``app_id`` provided")
            check_response(resp)
            data = resp["data"]
            self.refresh_token = data["refresh_token"]
            self.access_token = data["access_token"]
            return data
        return run_gen_step(gen_step, async_)

    ########## Cloud Download API ##########

    @overload
    def clouddownload_quota_info(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_quota_info(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_quota_info(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取云下载配额信息

        GET https://proapi.115.com/open/offline/get_quota_info

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/gif2n3smh54kyg0p
        """
        api = complete_url("/open/offline/get_quota_info", base_url)
        return self.request(url=api, async_=async_, **request_kwargs)

    @overload
    def clouddownload_task_add_bt(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_add_bt(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_add_bt(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """添加云下载 BT 任务

        POST https://proapi.115.com/open/offline/add_task_bt 

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/svfe4unlhayvluly

        :payload:
            - info_hash: str 💡 种子文件的 info_hash
            - pick_code: str 💡 种子文件的提取码
            - save_path: str 💡 保存到 ``wp_path_id`` 对应目录下的相对路径
            - torrent_sha1: str 💡 种子文件的 sha1
            - wanted: str 💡 选择文件进行下载（是数字索引，从 0 开始计数，用 "," 分隔）
            - wp_path_id: int | str = <default> 💡 保存目标目录 id
        """
        api = complete_url("/open/offline/add_task_bt ", base_url)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def clouddownload_task_add_urls(
        self, 
        payload: str | Iterable[str] | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_add_urls(
        self, 
        payload: str | Iterable[str] | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_add_urls(
        self, 
        payload: str | Iterable[str] | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """添加云下载链接任务

        POST https://proapi.115.com/open/offline/add_task_urls

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/zkyfq2499gdn3mty

        :payload:
            - urls: str 💡 链接，用 "\\n" 分隔，支持HTTP、HTTPS、FTP、磁力链和电驴链接
            - wp_path_id: int | str = <default> 💡 保存到目录的 id
        """
        api = complete_url("/open/offline/add_task_urls", base_url)
        if isinstance(payload, str):
            payload = {"urls": payload.strip("\n")}
        elif not isinstance(payload, dict):
            payload = {"urls": ",".join(payload)}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def clouddownload_task_clear(
        self, 
        payload: int | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_clear(
        self, 
        payload: int | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_clear(
        self, 
        payload: int | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """清空云下载任务

        POST https://proapi.115.com/open/offline/clear_task

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/uu5i4urb5ylqwfy4

        :payload:
            - flag: int = 0 💡 标识，用于对应某种情况

                - 0: 已完成
                - 1: 全部
                - 2: 已失败
                - 3: 进行中
                - 4: 已完成+删除源文件
                - 5: 全部+删除源文件
        """
        api = complete_url("/open/offline/clear_task", base_url)
        if isinstance(payload, int):
            payload = {"flag": payload}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def clouddownload_task_del(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_del(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_del(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """删除云下载任务

        POST https://proapi.115.com/open/offline/del_task

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/pmgwc86lpcy238nw

        :payload:
            - info_hash: str 💡 待删除任务的 info_hash
            - del_source_file: 0 | 1 = <default> 💡 是否删除源文件 1:删除 0:不删除
        """
        api = complete_url("/open/offline/del_task", base_url)
        if isinstance(payload, str):
            payload = {"info_hash": payload}
        return self.request(api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def clouddownload_task_list(
        self, 
        payload: int | dict = 1, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_list(
        self, 
        payload: int | dict = 1, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_list(
        self, 
        payload: int | dict = 1, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取云下载任务列表

        GET https://proapi.115.com/open/offline/get_task_list

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/av2mluz7uwigz74k

        :payload:
            - page: int = 1
        """
        api = complete_url("/open/offline/get_task_list", base_url)
        if isinstance(payload, int):
            payload = {"page": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def clouddownload_torrent(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_torrent(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_torrent(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """解析 BT 种子

        POST https://proapi.115.com/open/offline/torrent

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/evez3u50cemoict1

        :payload:
            - torrent_sha1: str 💡 种子文件的 sha1
            - pick_code: str    💡 种子文件的提取码
        """
        api = complete_url("/open/offline/torrent", base_url)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    ########## Download API ##########

    @overload
    def download_url(
        self, 
        pickcode: int | str, 
        /, 
        strict: bool = True, 
        user_agent: None | str = None, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> P115URL:
        ...
    @overload
    def download_url(
        self, 
        pickcode: int | str, 
        /, 
        strict: bool = True, 
        user_agent: None | str = None, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, P115URL]:
        ...
    def download_url(
        self, 
        pickcode: int | str, 
        /, 
        strict: bool = True, 
        user_agent: None | str = None, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> P115URL | Coroutine[Any, Any, P115URL]:
        """获取文件的下载链接，此接口是对 ``download_url_info`` 的封装

        .. note::
            获取的直链中，部分查询参数的解释：

            - ``t``: 过期时间戳
            - ``u``: 用户 id
            - ``c``: 允许同时打开次数，如果为 0，则是无限次数
            - ``f``: 请求时要求携带请求头
                - 如果为空，则无要求
                - 如果为 1，则需要 user-agent（和请求直链时的一致）
                - 如果为 3，则需要 user-agent（和请求直链时的一致） 和 Cookie（由请求直链时的响应所返回的 Set-Cookie 响应头）

        :param pickcode: id 或者提取码
        :param strict: 如果为 True，当目标是目录时，会抛出 IsADirectoryError 异常
        :param user_agent: 如果不为 None，则作为请求头 "user-agent" 的值
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 下载链接
        """
        pickcode = self.to_pickcode(pickcode)
        def gen_step():
            resp = yield self.download_url_info_open(
                pickcode, 
                user_agent=user_agent, 
                async_=async_, 
                **request_kwargs, 
            )
            resp["pickcode"] = pickcode
            resp["is_download"] = True
            data = resp.get("data")
            if not data:
                resp["state"] = False
                resp["errno"] = resp.get("errno") or 50015
                resp.setdefault("message", "文件不存在、是目录或者不支持此操作")
            check_response(resp)
            for fid, info in data.items():
                url = info["url"]
                if strict and not url:
                    throw(
                        errno.EISDIR, 
                        f"{fid} is a directory, with response {resp}", 
                    )
                return P115URL(
                    url["url"] if url else "", 
                    id=int(fid), 
                    pickcode=info["pick_code"], 
                    name=info["file_name"], 
                    size=int(info["file_size"]), 
                    sha1=info["sha1"], 
                    is_dir=not url, 
                    headers=resp["headers"], 
                )
            throw(
                errno.ENOENT, 
                f"no such pickcode: {pickcode!r}, with response {resp}", 
            )
        return run_gen_step(gen_step, async_)

    @overload
    def download_urls(
        self, 
        pickcodes: int | str | Iterable[int | str], 
        /, 
        strict: bool = True, 
        user_agent: None | str = None, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict[int, P115URL]:
        ...
    @overload
    def download_urls(
        self, 
        pickcodes: int | str | Iterable[int | str], 
        /, 
        strict: bool = True, 
        user_agent: None | str = None, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict[int, P115URL]]:
        ...
    def download_urls(
        self, 
        pickcodes: int | str | Iterable[int | str], 
        /, 
        strict: bool = True, 
        user_agent: None | str = None, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict[int, P115URL] | Coroutine[Any, Any, dict[int, P115URL]]:
        """批量获取文件的下载链接，此接口是对 ``download_url_info`` 的封装

        .. note::
            获取的直链中，部分查询参数的解释：

            - ``t``: 过期时间戳
            - ``u``: 用户 id
            - ``c``: 允许同时打开次数，如果为 0，则是无限次数
            - ``f``: 请求时要求携带请求头
                - 如果为空，则无要求
                - 如果为 1，则需要 user-agent（和请求直链时的一致）
                - 如果为 3，则需要 user-agent（和请求直链时的一致） 和 Cookie（由请求直链时的响应所返回的 Set-Cookie 响应头）

        :param pickcodes: 提取码，多个用逗号 "," 隔开
        :param strict: 如果为 True，当目标是目录时，会直接忽略
        :param user_agent: 如果不为 None，则作为请求头 "user-agent" 的值
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 一批下载链接
        """
        if isinstance(pickcodes, (int, str)):
            pickcodes = self.to_pickcode(pickcodes)
        else:
            pickcodes = ",".join(map(self.to_pickcode, pickcodes))
        def gen_step():
            resp = yield self.download_url_info_open(
                pickcodes, 
                user_agent=user_agent, 
                async_=async_, 
                **request_kwargs, 
            )
            resp["pickcode"] = pickcodes
            resp["is_download"] = True
            data = resp.get("data")
            if not data:
                resp["state"] = False
                resp["errno"] = resp.get("errno") or 50015
                resp.setdefault("message", "文件不存在、是目录或者不支持此操作")
                throw(errno.EIO, resp)
            urls: dict[int, P115URL] = {}
            if resp.get("errno") != 50003:
                check_response(resp)
                for fid, info in data.items():
                    url = info["url"]
                    if strict and not url:
                        continue
                    fid = int(fid)
                    urls[fid] = P115URL(
                        url["url"] if url else "", 
                        id=fid, 
                        pickcode=info["pick_code"], 
                        name=info["file_name"], 
                        size=int(info["file_size"]), 
                        sha1=info["sha1"], 
                        is_dir=not url, 
                        headers=resp["headers"], 
                    )
            return urls
        return run_gen_step(gen_step, async_)

    @overload
    def download_url_info(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        user_agent: None | str = None, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def download_url_info(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        user_agent: None | str = None, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def download_url_info(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        user_agent: None | str = None, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取文件的下载链接

        POST https://proapi.115.com/open/ufile/downurl

        .. hint::
            相当于 `P115Client.download_url_app(app="chrome")`

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/um8whr91bxb5997o

        :payload:
            - pick_code: str 💡 提取码，多个用逗号 "," 隔开
        """
        api = complete_url("/open/ufile/downurl", base_url)
        if isinstance(payload, str):
            payload = {"pick_code": payload}
        headers = request_kwargs["headers"] = dict(request_kwargs.get("headers") or ())
        if user_agent is None:
            headers.setdefault("user-agent", "")
        else:
            headers["user-agent"] = user_agent
        def parse(_, content: bytes, /) -> dict:
            json = json_maybe_decrypt_loads(content)
            json["headers"] = headers
            return json
        request_kwargs.setdefault("parse", parse)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    ########## File System API ##########

    @overload
    def fs_copy(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        pid: int | str = 0, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_copy(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        pid: int | str = 0, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_copy(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        pid: int | str = 0, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """文件复制

        POST https://proapi.115.com/open/ufile/copy

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/lvas49ar94n47bbk

        .. caution::
            ⚠️ 请不要并发执行，限制在 5 万个文件和目录以内

        :payload:
            - file_id: int | str 💡 文件或目录的 id，多个用逗号 "," 隔开
            - pid: int | str = 0 💡 父目录 id
            - nodupli: 0 | 1 = 0 💡 复制的文件在目标目录是否允许重复：0:可以 1:不可以
        """
        api = complete_url("/open/ufile/copy", base_url)
        if isinstance(payload, (int, str)):
            payload = {"file_id": payload}
        elif not isinstance(payload, dict):
            payload = {"file_id": ",".join(map(str, payload))}
        cast(dict, payload).setdefault("pid", pid)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_delete(
        self, 
        payload: int | str | dict | Iterable[int | str], 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_delete(
        self, 
        payload: int | str | dict | Iterable[int | str], 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_delete(
        self, 
        payload: int | str | dict | Iterable[int | str], 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """删除文件或目录

        POST https://proapi.115.com/open/ufile/delete

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/kt04fu8vcchd2fnb

        .. caution::
            ⚠️ 请不要并发执行，限制在 5 万个文件和目录以内

        :payload:
            - file_ids: int | str 💡 文件或目录的 id，多个用逗号 "," 隔开
        """
        api = complete_url("/open/ufile/delete", base_url)
        if isinstance(payload, (int, str)):
            payload = {"file_ids": payload}
        elif not isinstance(payload, dict):
            payload = {"file_ids": ",".join(map(str, payload))}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_files(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取目录中的文件列表和基本信息

        GET https://proapi.115.com/open/ufile/files

        .. hint::
            相当于 ``P115Client.fs_files_app()``

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/kz9ft9a7s57ep868

        :payload:
            - cid: int | str = 0 💡 目录 id，对应 parent_id
            - limit: int = 32 💡 分页大小，最大值不一定，看数据量，7,000 应该总是安全的，10,000 有可能报错，但有时也可以 20,000 而成功
            - offset: int = 0 💡 分页开始的索引，索引从 0 开始计算

            - aid: int = 1 💡 area_id

                - 0: 会被视为 1
                - 1: 正常文件
                - 2: <unknown>
                - 3: <unknown>
                - 4: <unknown>
                - 5: <unknown>
                - 7: 回收站文件
                - 9: <unknown>
                - 12: 瞬间文件
                - 15: <unknown>
                - 120: 彻底删除文件、简历附件
                - <其它>: 会被视为 0

            - asc: 0 | 1 = <default> 💡 是否升序排列。0:降序 1:升序
            - count_folders: 0 | 1 = 1 💡 统计文件数和目录数
            - cur: 0 | 1 = <default>   💡 是否只显示当前目录
            - custom_order: 0 | 1 | 2 = <default> 💡 是否使用记忆排序。如果指定了 "asc"、"fc_mix"、"o" 中其一，则此参数会被自动设置为 2

                - 0: 使用记忆排序（自定义排序失效） 
                - 1: 使用自定义排序（不使用记忆排序） 
                - 2: 自定义排序（非目录置顶）

            - fc_mix: 0 | 1 = <default> 💡 是否目录和文件混合，如果为 0 则目录在前（目录置顶）
            - fields: str = <default>
            - for: str = <default> 💡 文件格式，例如 "doc"
            - is_q: 0 | 1 = <default>
            - is_share: 0 | 1 = <default>
            - min_size: int = 0 💡 最小的文件大小
            - max_size: int = 0 💡 最大的文件大小（含），<= 0 表示不限，因此并不能借此仅筛选出空文件
            - natsort: 0 | 1 = <default> 💡 是否执行自然排序(natural sorting)
            - nf: 0 | 1 = <default> 💡 不要显示文件（即仅显示目录），但如果 show_dir=0，则此参数无效
            - o: str = <default> 💡 用某字段排序

                - "file_name": 文件名
                - "file_size": 文件大小
                - "file_type": 文件种类
                - "user_etime": 事件时间（无效，效果相当于 "user_utime"）
                - "user_utime": 修改时间
                - "user_ptime": 创建时间（无效，效果相当于 "user_utime"）
                - "user_otime": 上一次打开时间（无效，效果相当于 "user_utime"）

            - qid: int = <default>
            - r_all: 0 | 1 = <default>
            - record_open_time: 0 | 1 = 1 💡 是否要记录目录的打开时间
            - scid: int | str = <default>
            - show_dir: 0 | 1 = 1 💡 是否显示目录
            - snap: 0 | 1 = <default>
            - source: str = <default>
            - star: 0 | 1 = <default> 💡 是否星标文件
            - stdir: 0 | 1 = <default> 💡 筛选文件时，是否显示目录：1:展示 0:不展示
            - suffix: str = <default> 💡 后缀名（优先级高于 `type`）
            - type: int = <default> 💡 文件类型

                - 0: 全部（仅当前目录）
                - 1: 文档
                - 2: 图片
                - 3: 音频
                - 4: 视频
                - 5: 压缩包
                - 6: 软件/应用
                - 7: 书籍
                - 8: 其它
                - 9: 相当于 8
                - 10: 相当于 8
                - 11: 相当于 8
                - 12: ？？？
                - 13: ？？？
                - 14: ？？？
                - 15: 图片和视频，相当于 2 和 4
                - >= 16: 相当于 8
        """
        api = complete_url("/open/ufile/files", base_url)
        if payload is None:
            return self.request(url=api, async_=async_, **request_kwargs)
        if isinstance(payload, (int, str)):
            payload = {"cid": payload}
        payload = {
            "aid": 1, "count_folders": 1, "limit": 32, "offset": 0, 
            "record_open_time": 1, "show_dir": 1, "cid": 0, **payload, 
        }
        if payload.keys() & frozenset(("asc", "fc_mix", "o")):
            payload.setdefault("custom_order", 2)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_info(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        method: str = "GET", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_info(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        method: str = "GET", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_info(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        method: str = "GET", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取文件或目录详情

        GET https://proapi.115.com/open/folder/get_info

        .. note::
            支持 GET 和 POST 方法。`file_id` 和 ``path`` 需必传一个

        .. hint::
            具有 ``P115Client.fs_category_get()`` 的能力，而且更强，因为支持用 path 查询

        .. caution::
            尝试获取目录的信息时，会去计算目录中文件和目录的数量、总文件大小 等信息，可能会消耗大量时间，但短时间内再次查询同一目录，耗时可能会大大减小

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/rl8zrhe2nag21dfw

        :payload:
            - file_id: int | str 💡 文件或目录的 id
            - path: str = <default> 💡 文件或目录的路径。分隔符支持 / 和 > 两种符号，最前面需分隔符开头，以分隔符分隔目录层级
        """
        api = complete_url("/open/folder/get_info", base_url)
        if isinstance(payload, int):
            payload = {"file_id": payload}
        elif isinstance(payload, str):
            if payload.startswith("0") or payload.strip(digits):
                payload = {"path": payload}
            else:
                payload = {"file_id": payload}
        if path := payload.get("path"):
            if path.startswith(("/", ">")):
                sep = path[0]
                payload["path"] = sep + path.strip(sep)
            elif ">" in path:
                payload["path"] = "/" + path.rstrip("/")
            else:
                payload["path"] = ">" + path.rstrip(">")
        if method.upper() == "POST":
            request_kwargs["data"] = payload
        else:
            request_kwargs["params"] = payload
        return self.request(url=api, method=method, async_=async_, **request_kwargs)

    @overload
    def fs_mkdir(
        self, 
        payload: str | dict, 
        /, 
        pid: int | str = 0, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_mkdir(
        self, 
        payload: str | dict, 
        /, 
        pid: int | str = 0, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_mkdir(
        self, 
        payload: str | dict, 
        /, 
        pid: int | str = 0, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """新建目录

        POST https://proapi.115.com/open/folder/add

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/qur839kyx9cgxpxi

        :payload:
            - file_name: str     💡 新建目录名称，限制 255 个字符
            - pid: int | str = 0 💡 新建目录所在的父目录ID (根目录的ID为0)
        """
        api = complete_url("/open/folder/add", base_url)
        if isinstance(payload, str):
            payload = {"pid": pid, "file_name": payload}
        payload.setdefault("pid", pid)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_move(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        pid: int | str = 0, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_move(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        pid: int | str = 0, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_move(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        pid: int | str = 0, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """文件移动

        POST https://proapi.115.com/open/ufile/move

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/vc6fhi2mrkenmav2

        .. caution::
            ⚠️ 请不要并发执行，限制在 5 万个文件和目录以内

        :payload:
            - file_ids: int | str 💡 文件或目录的 id，多个用逗号 "," 隔开
            - to_cid: int | str = 0 💡 父目录 id
        """
        api = complete_url("/open/ufile/move", base_url)
        if isinstance(payload, (int, str)):
            payload = {"file_ids": payload}
        elif not isinstance(payload, dict):
            payload = {"file_ids": ",".join(map(str, payload))}
        cast(dict, payload).setdefault("to_cid", pid)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_rename(
        self, 
        payload: tuple[int | str, str] | dict, 
        /, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_rename(
        self, 
        payload: tuple[int | str, str] | dict, 
        /, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_rename(
        self, 
        payload: tuple[int | str, str] | dict, 
        /, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """重命名文件或目录，此接口是对 ``fs_update_open`` 的封装

        .. caution::
            改名时，虽然不能修改扩展名，但是一定要带上扩展名（无论是啥），不然会把最后一个句点 . 及其之后文字截断

        :payload:
            - file_id: int | str 💡 文件 id
            - file_name: str     💡 文件名
        """
        if isinstance(payload, tuple):
            payload = {"file_id": payload[0], "file_name": payload[1]}
        return self.fs_update_open(payload, async_=async_, **request_kwargs)

    @overload
    def fs_search(
        self, 
        payload: int | str | dict = ".", 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_search(
        self, 
        payload: int | str | dict = ".", 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_search(
        self, 
        payload: int | str | dict = ".", 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """搜索文件或目录

        GET https://proapi.115.com/open/ufile/search

        .. attention::
            最多只能取回前 10,000 条数据，也就是 `limit + offset <= 10_000`，不过可以一次性取完

            不过就算正确设置了 ``limit`` 和 `offset`，并且总数据量大于 `limit + offset`，可能也不足 `limit`，这应该是 bug，也就是说，就算数据总量足够你也取不到足量

            它返回数据中的 ``count`` 字段的值表示总数据量（即使你只能取前 10,000 条），往往并不准确，最多能当作一个可参考的估计值

        .. note::
            这个方法似乎不支持仅搜索目录本身，搜索范围是从指定目录开始的整个目录树

        .. hint::
            相当于 ``P115Client.fs_search_app2()``

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/ft2yelxzopusus38

        :payload:
            - aid: int = 1 💡 area_id

                - 0: 会被视为 1
                - 1: 正常文件
                - 2: <unknown>
                - 3: <unknown>
                - 4: <unknown>
                - 5: <unknown>
                - 7: 回收站文件
                - 9: <unknown>
                - 12: 瞬间文件
                - 15: <unknown>
                - 120: 彻底删除文件、简历附件
                - <其它>: 会被视为 0

            - asc: 0 | 1 = <default> 💡 是否升序排列
            - cid: int | str = 0 💡 目录 id。cid=-1 时，表示不返回列表任何内容
            - count_folders: 0 | 1 = <default>
            - date: str = <default> 💡 筛选日期
            - fc: 0 | 1 = <default> 💡 只显示文件或目录。1:只显示目录 2:只显示文件
            - fc_mix: 0 | 1 = <default> 💡 是否目录和文件混合，如果为 0 则目录在前（目录置顶）
            - file_label: int | str = <default> 💡 标签 id
            - gte_day: str 💡 搜索结果匹配的开始时间；格式：YYYY-MM-DD
            - limit: int = 1150 💡 一页大小，意思就是 page_size
            - lte_day: str 💡 搜索结果匹配的结束时间；格式：YYYY-MM-DD
            - o: str = <default> 💡 用某字段排序

                - "file_name": 文件名
                - "file_size": 文件大小
                - "file_type": 文件种类
                - "user_utime": 修改时间
                - "user_ptime": 创建时间
                - "user_otime": 上一次打开时间

            - offset: int = 0  💡 索引偏移，索引从 0 开始计算
            - search_value: str = "." 💡 搜索文本，可以是 sha1
            - source: str = <default>
            - star: 0 | 1 = <default> 💡 是否星标文件
            - suffix: str = <default> 💡 后缀名（优先级高于 `type`）
            - type: int = <default> 💡 文件类型

                - 0: 全部（仅当前目录）
                - 1: 文档
                - 2: 图片
                - 3: 音频
                - 4: 视频
                - 5: 压缩包
                - 6: 软件/应用
                - 7: 书籍
                - 99: 所有文件

            - version: str = <default> 💡 版本号，比如 3.1
        """
        api = complete_url("/open/ufile/search", base_url)
        if isinstance(payload, str):
            payload = {"search_value": payload}
        elif isinstance(payload, int):
            payload = {"file_label": payload}
        payload = {
            "aid": 1, "cid": 0, "limit": 1150, "offset": 0, 
            "show_dir": 1, **payload, 
        }
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_star_set(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        star: bool = True, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_star_set(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        star: bool = True, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_star_set(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        star: bool = True, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """为文件或目录设置或取消星标，此接口是对 ``fs_update_open`` 的封装

        .. note::
            即使其中任何一个 id 目前已经被删除，也可以操作成功

        :payload:
            - file_id: int | str    💡 只能传入 1 个
            - file_id[0]: int | str 💡 如果有多个，则按顺序给出
            - file_id[1]: int | str
            - ...
            - star: 0 | 1 = 1
        """
        if isinstance(payload, (int, str)):
            payload = {"file_id": payload}
        elif not isinstance(payload, dict):
            payload = {f"file_id[{i}]": id for i, id in enumerate(payload)}
        payload.setdefault("star", int(star))
        return self.fs_update_open(payload, async_=async_, **request_kwargs)

    @overload
    def fs_video(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_video(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_video(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取视频在线播放地址（和视频文件相关数据）

        GET https://proapi.115.com/open/video/play

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/hqglxv3cedi3p9dz

        .. hint::
            需切换音轨时，在请求返回的播放地址中增加请求参数 `&audio_track=${index}`，值就是接口响应中 ``multitrack_list`` 中某个成员的索引，从 0 开始计数

        :payload:
            - pick_code: str 💡 文件提取码
            - share_id: int | str = <default> 💡 共享 id，获取共享文件播放地址所需
        """
        api = complete_url("/open/video/play", base_url)
        if isinstance(payload, str):
            payload = {"pick_code": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_video_history(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_video_history(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_video_history(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取视频播放进度

        GET https://proapi.115.com/open/video/history

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/gssqdrsq6vfqigag

        :payload:
            - pick_code: str 💡 文件提取码
        """
        api = complete_url("/open/video/history", base_url)
        if isinstance(payload, str):
            payload = {"pick_code": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_video_history_set(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_video_history_set(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_video_history_set(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """记忆视频播放进度

        POST https://proapi.115.com/open/video/history

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/bshagbxv1gzqglg4

        :payload:
            - pick_code: str 💡 文件提取码
            - time: int = <default> 💡 视频播放进度时长 (单位秒)
            - watch_end: int = <default> 💡 视频是否播放播放完毕 0:未完毕 1:完毕
        """
        api = complete_url("/open/video/history", base_url)
        if isinstance(payload, str):
            payload = {"pick_code": payload}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_video_push(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_video_push(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_video_push(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """提交视频转码

        POST https://proapi.115.com/open/video/video_push

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/nxt8r1qcktmg3oan

        :payload:
            - pick_code: str 💡 文件提取码
            - op: str = "vip_push" 💡 提交视频加速转码方式

                - "vip_push": 根据；vip 等级加速
                - "pay_push": 枫叶加速
        """
        api = complete_url("/open/video/video_push", base_url)
        if isinstance(payload, str):
            payload = {"pick_code": payload}
        payload.setdefault("op", "vip_push")
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_video_subtitle(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_video_subtitle(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_video_subtitle(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """视频字幕列表

        GET https://proapi.115.com/open/video/subtitle

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/nx076h3glapoyh7u

        :payload:
            - pick_code: str 💡 文件提取码
        """
        api = complete_url("/open/video/subtitle", base_url)
        if isinstance(payload, str):
            payload = {"pick_code": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_update(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_update(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_update(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """设置文件或目录（备注、标签、封面等）

        POST https://proapi.115.com/open/ufile/update

        .. hint::
            即使文件已经被删除，也可以操作成功

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/gyrpw5a0zc4sengm

        :payload:
            - file_id: int | str    💡 只能传入 1 个
            - file_id[0]: int | str 💡 如果有多个，则按顺序给出
            - file_id[1]: int | str
            - ...
            - file_name: str = <default> 💡 文件名
            - star: 0 | 1 = <default> 💡 是否星标：0:取消星标 1:设置星标
            - ...
        """
        api = complete_url("/open/ufile/update", base_url)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    ########## Recyclebin API ##########

    @overload
    def recyclebin_clean(
        self, 
        payload: int | str | Iterable[int | str] | dict = {}, 
        /, 
        password: str = "000000", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def recyclebin_clean(
        self, 
        payload: int | str | Iterable[int | str] | dict = {}, 
        /, 
        password: str = "000000", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def recyclebin_clean(
        self, 
        payload: int | str | Iterable[int | str] | dict = {}, 
        /, 
        password: str = "000000", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """回收站：删除或清空

        POST https://proapi.115.com/open/rb/del

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/gwtof85nmboulrce

        .. note::
            ``password`` 参数是不用传的

        :payload:
            - tid: int | str 💡 不传就是清空，多个用逗号 "," 隔开
        """
        api = complete_url("/open/rb/del", base_url)
        if isinstance(payload, (int, str)):
            payload = {"tid": payload}
        elif not isinstance(payload, dict):
            payload = {"tid": ",".join(map(str, payload))}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def recyclebin_list(
        self, 
        payload: int | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def recyclebin_list(
        self, 
        payload: int | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def recyclebin_list(
        self, 
        payload: int | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """回收站：列表

        GET https://proapi.115.com/open/rb/list

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/bg7l4328t98fwgex

        :payload:
            - limit: int = 32
            - offset: int = 0
        """ 
        api = complete_url("/open/rb/list", base_url)
        if isinstance(payload, int):
            payload = {"limit": 32, "offset": payload}
        payload.setdefault("limit", 32)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def recyclebin_revert(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def recyclebin_revert(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def recyclebin_revert(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """回收站：还原

        POST https://proapi.115.com/open/rb/revert

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/gq293z80a3kmxbaq

        .. caution::
            ⚠️ 请不要并发执行，限制在 5 万个文件和目录以内

        :payload:
            - tid: int | str 💡 多个用逗号 "," 隔开
        """
        api = complete_url("/open/rb/revert", base_url)
        if isinstance(payload, (int, str)):
            payload = {"tid": payload}
        elif not isinstance(payload, dict):
            payload = {"tid": ",".join(map(str, payload))}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    ########## Upload API ##########

    @overload
    def upload_gettoken_open(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def upload_gettoken_open(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def upload_gettoken_open(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取阿里云 OSS 的 token（上传凭证）

        GET https://proapi.115.com/open/upload/get_token

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/kzacvzl0g7aiyyn4
        """
        api = complete_url("/open/upload/get_token", base_url)
        return self.request(url=api, async_=async_, **request_kwargs)

    @overload
    def upload_init(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def upload_init(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def upload_init(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """初始化上传任务，可能秒传

        POST https://proapi.115.com/open/upload/init

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/ul4mrauo5i2uza0q

        :payload:
            - file_name: str 💡 文件名
            - fileid: str 💡 文件的 sha1 值
            - file_size: int 💡 文件大小，单位是字节
            - target: str 💡 上传目标，格式为 f"U_{aid}_{pid}" 或 f"S_{share_id}_{pid}"
            - topupload: int = 0 💡 上传调度文件类型调度标记

                -  0: 单文件上传任务标识 1 条单独的文件上传记录
                -  1: 目录任务调度的第 1 个子文件上传请求标识 1 次目录上传记录
                -  2: 目录任务调度的其余后续子文件不作记作单独上传的上传记录 
                - -1: 没有该参数

            - sign_key: str = "" 💡 2 次验证时读取文件的范围
            - sign_val: str = "" 💡 2 次验证的签名值
        """
        api = complete_url("/open/upload/init", base_url)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def upload_resume(
        self, 
        payload: dict | str, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def upload_resume(
        self, 
        payload: dict | str, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def upload_resume(
        self, 
        payload: dict | str, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取恢复断点续传所需信息

        POST https://proapi.115.com/open/upload/resume

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/tzvi9sbcg59msddz

        :payload:
            - pick_code: str 💡 上传任务 key
            - target: str    💡 上传目标，默认为 "U_1_0"，格式为 f"U_{aid}_{pid}" 或 f"S_{share_id}_{pid}"
            - fileid: str    💡 文件的 sha1 值（⚠️ 可以是任意值）
            - file_size: int 💡 文件大小，单位是字节（⚠️ 可以是任意值）
        """
        api = complete_url("/open/upload/resume", base_url)
        if isinstance(payload, str):
            payload = {"pick_code": payload}
        else:
            payload = dict(payload)
            if "pick_code" not in payload:
                if "pickcode" in payload:
                    payload["pick_code"] = payload["pickcode"]
            callback_var: None | dict = None
            if "callback_var" in payload:
                callback_var = loads(payload["callback_var"])
            elif "callback" in payload:
                callback_var = loads(payload["callback"]["callback_var"])
            if callback_var:
                payload.update(
                    pick_code=callback_var["x:pick_code"], 
                    target=callback_var["x:target"], 
                )
        payload.setdefault("fileid", "0" * 40)
        payload.setdefault("file_size", 1)
        payload.setdefault("target", "U_1_0")
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def upload_file_init(
        self, 
        /, 
        filename: str, 
        filesha1: str, 
        filesize: int, 
        dirname: str = "", 
        read_range_bytes_or_hash: None | Callable[[str], str | Buffer] = None, 
        pid: int | str = 0, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def upload_file_init(
        self, 
        /, 
        filename: str, 
        filesha1: str, 
        filesize: int, 
        dirname: str = "", 
        read_range_bytes_or_hash: None | Callable[[str], str | Buffer] = None, 
        pid: int | str = 0, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def upload_file_init(
        self, 
        /, 
        filename: str, 
        filesha1: str, 
        filesize: int, 
        dirname: str = "", 
        read_range_bytes_or_hash: None | Callable[[str], str | Buffer] = None, 
        pid: int | str = 0, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """初始化上传，可能秒传，此接口是对 ``upload_init_open`` 的封装

        .. note::
            - 文件大小 和 sha1 是必需的，只有 sha1 是没用的。
            - 如果文件大于等于 1 MB (1048576 B)，就需要 2 次检验一个范围哈希，就必须提供 `read_range_bytes_or_hash`

        :param filename: 文件名
        :param filesha1: 文件的 sha1
        :param filesize: 文件大小
        :param dirname: 保存目录，是在 ``pid`` 对应目录下的相对路径，默认为 ``pid`` 所对应目录本身
        :param read_range_bytes_or_hash: 调用以获取 2 次验证的数据或计算 sha1，接受一个数据范围，格式符合:
            `HTTP Range Requests <https://developer.mozilla.org/en-US/docs/Web/HTTP/Range_requests>`_，
            返回值如果是 str，则视为计算好的 sha1，如果为 Buffer，则视为数据（之后会被计算 sha1）
        :param pid: 上传文件到此目录的 id，或者指定的 target（格式为 f"U_{aid}_{pid}" 或 f"S_{share_id}_{pid}"，但若 `aid != 1`，则会报参数错误）
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 接口响应
        """
        def gen_step():
            if isinstance(pid, str) and pid.startswith(("U_", "S_")):
                target = pid
            else:
                target = f"U_1_{pid}"
            payload = {
                "file_name": filename, 
                "fileid": filesha1.upper(), 
                "file_size": filesize, 
                "target": target, 
                "path": dirname, 
                "topupload": 1, 
            }
            resp = yield self.upload_init_open(
                payload, 
                async_=async_, 
                **request_kwargs, 
            )
            if not resp["state"]:
                return resp
            data = resp["data"]
            if data["status"] == 7:
                if read_range_bytes_or_hash is None:
                    raise ValueError("filesize >= 1 MB, thus need pass the ``read_range_bytes_or_hash`` argument")
                payload["sign_key"] = data["sign_key"]
                sign_check: str = data["sign_check"]
                content: str | Buffer
                if async_:
                    content = yield ensure_async(read_range_bytes_or_hash)(sign_check)
                else:
                    content = read_range_bytes_or_hash(sign_check)
                if isinstance(content, str):
                    payload["sign_val"] = content.upper()
                else:
                    payload["sign_val"] = sha1(content).hexdigest().upper()
                resp = yield self.upload_init_open(
                    payload, 
                    async_=async_, # type: ignore
                    **request_kwargs, 
                )
            resp["reuse"] = resp["data"].get("status") == 2
            return resp
        return run_gen_step(gen_step, async_)

    @overload
    def upload_file(
        self, 
        /, 
        file: Buffer | str | PathLike | URL | SupportsGeturl | SupportsRead, 
        pid: int | str = 0, 
        share_id: int = 0, 
        filename: str = "", 
        filesha1: str = "", 
        filesize: int = -1, 
        dirname: str = "", 
        payload: None | Mapping = None, 
        partsize: int = 0, 
        callback: None | dict = None, 
        upload_id: str = "", 
        endpoint: str = "https://oss-cn-shenzhen.aliyuncs.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def upload_file(
        self, 
        /, 
        file: Buffer | str | PathLike | URL | SupportsGeturl | SupportsRead, 
        pid: int | str = 0, 
        share_id: int = 0, 
        filename: str = "", 
        filesha1: str = "", 
        filesize: int = -1, 
        dirname: str = "", 
        payload: None | Mapping = None, 
        partsize: int = 0, 
        callback: None | dict = None, 
        upload_id: str = "", 
        endpoint: str = "https://oss-cn-shenzhen.aliyuncs.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def upload_file(
        self, 
        /, 
        file: Buffer | str | PathLike | URL | SupportsGeturl | SupportsRead, 
        pid: int | str = 0, 
        share_id: int = 0, 
        filename: str = "", 
        filesha1: str = "", 
        filesize: int = -1, 
        dirname: str = "", 
        payload: None | Mapping = None, 
        partsize: int = 0, 
        callback: None | dict = None, 
        upload_id: str = "", 
        endpoint: str = "https://oss-cn-shenzhen.aliyuncs.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """上传文件

        .. note::
            如果提供了 ``callback``，则强制为分块上传。
            此时，最好提供一下 ``upload_id``，否则就是从头开始。
            此时可以省略 ``pid``、``filename``、``filesha1``、``filesize``、``partsize``

        .. caution::
            ``partsize > 0`` 时，不要把 ``partsize`` 设置得太小，起码得 10 MB (10485760) 以上

        :param file: 待上传的文件
        :param pid: 上传文件到此目录的 id 或 pickcode，或者指定的 target（格式为 f"U_{aid}_{pid}" 或 f"S_{share_id}_{pid}"，但若 `aid != 1`，则会报参数错误）
        :param share_id: 共享 id
        :param filename: 文件名，如果为空，则会自动确定
        :param filesha1: 文件的 sha1，如果为空，则会自动确定
        :param filesize: 文件大小，如果为 -1，则会自动确定
        :param dirname: 保存目录，是在 ``pid`` 对应目录下的相对路径，默认为 ``pid`` 所对应目录本身
        :param payload: 其它的查询参数
        :param partsize: 分块上传的分块大小。如果为 0，则不做分块上传；如果 < 0，则会自动确定
        :param callback: 回调数据
        :param upload_id: 上传任务 id
        :param endpoint: 上传目的网址
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 接口响应
        """
        request_kwargs["headers"] = dict(
            request_kwargs.get("headers") or (), 
            authorization=self.headers["authorization"], 
        )
        return upload_file(
            file=file, 
            pid=pid, 
            share_id=share_id, 
            filename=filename, 
            filesha1=filesha1, 
            filesize=filesize, 
            dirname=dirname, 
            payload=payload, 
            partsize=partsize, 
            callback=callback, 
            upload_id=upload_id, 
            endpoint=endpoint, 
            async_=async_, # type: ignore
            **request_kwargs, 
        )

    ########## User API ##########

    @overload
    def user_info(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def user_info(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def user_info(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取用户信息

        GET https://proapi.115.com/open/user/info

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/ot1litggzxa1czww
        """
        api = complete_url("/open/user/info", base_url)
        return self.request(url=api, async_=async_, **request_kwargs)

    ########## Other API ##########s

    @overload
    def vip_qr_url(
        self, 
        payload: int | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def vip_qr_url(
        self, 
        payload: int | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def vip_qr_url(
        self, 
        payload: int | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取产品列表地址（即引导用户扫码购买 115 的 VIP 服务，以获取提成）

        GET https://proapi.115.com/open/vip/qr_url

        .. admonition:: Reference

            https://www.yuque.com/115yun/open/cguk6qshgapwg4qn#oByvI

        :payload:
            - open_device: int
            - default_product_id: int = <default> 💡 打开产品列表默认选中的产品对应的产品id，如果没有则使用默认的产品顺序。

                - 月费: 5
                - 年费: 1
                - 尝鲜1天: 101
                - 长期VIP(长期): 24072401
                - 超级VIP: 24072402
        """
        api = complete_url("/open/vip/qr_url", base_url)
        if not isinstance(payload, dict):
            payload = {"open_device": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    clouddownload_quota_info_open = clouddownload_quota_info
    clouddownload_task_add_bt_open = clouddownload_task_add_bt
    clouddownload_task_add_urls_open = clouddownload_task_add_urls
    clouddownload_task_clear_open = clouddownload_task_clear
    clouddownload_task_del_open = clouddownload_task_del
    clouddownload_task_list_open = clouddownload_task_list
    clouddownload_torrent_open = clouddownload_torrent
    download_url_open = download_url
    download_urls_open = download_urls
    download_url_info_open = download_url_info
    fs_copy_open = fs_copy
    fs_delete_open = fs_delete
    fs_files_open = fs_files
    fs_info_open = fs_info
    fs_mkdir_open = fs_mkdir
    fs_move_open = fs_move
    fs_rename_open = fs_rename
    fs_search_open = fs_search
    fs_star_set_open = fs_star_set
    fs_video_open = fs_video
    fs_video_history_open = fs_video_history
    fs_video_history_set_open = fs_video_history_set
    fs_video_push_open = fs_video_push
    fs_video_subtitle_open = fs_video_subtitle
    fs_update_open = fs_update
    recyclebin_clean_open = recyclebin_clean
    recyclebin_list_open = recyclebin_list
    recyclebin_revert_open = recyclebin_revert
    upload_init_open = upload_init
    upload_resume_open = upload_resume
    user_info_open = user_info
    upload_file_init_open = upload_file_init
    upload_file_open = upload_file
    vip_qr_url_open = vip_qr_url

    to_id = staticmethod(to_id)

    def to_pickcode(
        self, 
        id: int | str, 
        /, 
        prefix: Literal["a", "b", "c", "d", "e", "fa", "fb", "fc", "fd", "fe"] = "a", 
        stable_point: str = "", 
    ) -> str:
        """把可能是 id 或 pickcode 的一律转换成 pickcode

        .. note::
            规定：空提取码 "" 对应的 id 是 0

        :param id: 可能是 id 或 pickcode
        :param prefix: 前缀
        :param stable_point: 提取码的本征值，不同用户的值不同，你也可以直接传一个此用户的有效 pickcode

        :return: pickcode
        """
        return to_pickcode(
            id, 
            stable_point=stable_point or self.pickcode_stable_point, 
            prefix=prefix, 
        )


class P115Client(P115OpenClient):
    """115 的客户端对象

    .. note::
        目前允许 1 个用户同时登录多个开放平台应用（用 AppID 区别），也允许多次授权登录同 1 个应用

        同一个开放应用 id，最多同时有 3 个登入，如果有新的登录，则自动踢掉较早的那一个

        目前不允许短时间内再次用 ``refresh_token`` 刷新 ``access_token``，但你可以用登录的方式再次授权登录以获取 ``access_token``，即可不受频率限制

        1 个 ``refresh_token`` 只能使用 1 次，可获取新的 ``refresh_token`` 和 ``access_token``，如果请求刷新时，发送成功但读取失败，可能导致 ``refresh_token`` 报废，这时需要重新授权登录

    :param cookies: 115 的 cookies，要包含 ``UID``、``CID``、``KID`` 和 ``SEID`` 等

        - 如果是 None，则会要求人工扫二维码登录
        - 如果是 str，则要求是格式正确的 cookies 字符串，例如 "UID=...; CID=...; KID=...; SEID=..."
        - 如果是 bytes 或 os.PathLike，则视为路径，当更新 cookies 时，也会往此路径写入文件，格式要求同上面的 `str`
        - 如果是 collections.abc.Mapping，则是一堆 cookie 的名称到值的映射
        - 如果是 collections.abc.Iterable，则其中每一条都视为单个 cookie

    :param app: 重新登录时人工扫二维码后绑定的 ``app`` （或者叫 `device`），如果不指定，则根据 cookies 的 UID 字段来确定，如果不能确定，则用 "qandroid"
    :param app_id: 授权的 open 应用的 AppID
    :param console_qrcode: 在命令行输出二维码，否则在浏览器中打开

    -----

    :设备列表如下:

    +-------+----------+------------+----------------------+
    | No.   | ssoent   | app        | description          |
    +=======+==========+============+======================+
    | 01    | A1       | web        | 115生活_网页端       |
    +-------+----------+------------+----------------------+
    | --    | A1       | desktop    | 115浏览器            |
    +-------+----------+------------+----------------------+
    | --    | A2       | ?          | 未知: android        |
    +-------+----------+------------+----------------------+
    | --    | A3       | ?          | 未知: ios            |
    +-------+----------+------------+----------------------+
    | --    | A4       | ?          | 未知: ipad           |
    +-------+----------+------------+----------------------+
    | --    | B1       | ?          | 未知: android        |
    +-------+----------+------------+----------------------+
    | 02    | D1       | ios        | 115生活_苹果端       |
    +-------+----------+------------+----------------------+
    | 03    | D2       | bios       | 未知: ios            |
    +-------+----------+------------+----------------------+
    | 04    | D3       | 115ios     | 115_苹果端           |
    +-------+----------+------------+----------------------+
    | 05    | F1       | android    | 115生活_安卓端       |
    +-------+----------+------------+----------------------+
    | 06    | F2       | bandroid   | 未知: android        |
    +-------+----------+------------+----------------------+
    | 07    | F3       | 115android | 115_安卓端           |
    +-------+----------+------------+----------------------+
    | 08    | H1       | ipad       | 115生活_苹果平板端   |
    +-------+----------+------------+----------------------+
    | 09    | H2       | bipad      | 未知: ipad           |
    +-------+----------+------------+----------------------+
    | 10    | H3       | 115ipad    | 115_苹果平板端       |
    +-------+----------+------------+----------------------+
    | 11    | I1       | tv         | 115生活_安卓电视端   |
    +-------+----------+------------+----------------------+
    | 12    | I2       | apple_tv   | 115生活_苹果电视端   |
    +-------+----------+------------+----------------------+
    | 13    | M1       | qandriod   | 115管理_安卓端       |
    +-------+----------+------------+----------------------+
    | 14    | N1       | qios       | 115管理_苹果端       |
    +-------+----------+------------+----------------------+
    | 15    | O1       | qipad      | 115管理_苹果平板端   |
    +-------+----------+------------+----------------------+
    | 16    | P1       | os_windows | 115生活_Windows端    |
    +-------+----------+------------+----------------------+
    | 17    | P2       | os_mac     | 115生活_macOS端      |
    +-------+----------+------------+----------------------+
    | 18    | P3       | os_linux   | 115生活_Linux端      |
    +-------+----------+------------+----------------------+
    | 19    | R1       | wechatmini | 115生活_微信小程序端 |
    +-------+----------+------------+----------------------+
    | 20    | R2       | alipaymini | 115生活_支付宝小程序 |
    +-------+----------+------------+----------------------+
    | 21    | S1       | harmony    | 115_鸿蒙端           |
    +-------+----------+------------+----------------------+
    """
    def __init__(
        self, 
        /, 
        cookies: None | str | PathLike | Mapping[str, str] | Iterable[Mapping | Cookie | Morsel] = None, 
        app: str = "", 
        app_id: int = 0, 
        console_qrcode: bool = True, 
    ):
        self.init(
            cookies=cookies, 
            app=app, 
            app_id=app_id, 
            console_qrcode=console_qrcode, 
            instance=self, 
        )

    def __repr__(self, /) -> str:
        cls = type(self)
        try:
            user_id = self.user_id
        except LookupError:
            user_id = 0
        return f"<{cls.__module__}.{cls.__qualname__}(user_id={user_id}, app={self.login_app()!r}, app_id={self.app_id}) at {hex(id(self))}>"

    @overload # type: ignore
    @classmethod
    def init(
        cls, 
        /, 
        cookies: None | str | PathLike | Mapping[str, str] | Iterable[Mapping | Cookie | Morsel] = None, 
        app: str = "", 
        app_id: int = 0, 
        console_qrcode: bool = True, 
        instance: None | Self = None, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> P115Client:
        ...
    @overload
    @classmethod
    def init(
        cls, 
        /, 
        cookies: None | str | PathLike | Mapping[str, str] | Iterable[Mapping | Cookie | Morsel] = None, 
        app: str = "", 
        app_id: int = 0, 
        console_qrcode: bool = True, 
        instance: None | Self = None, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, P115Client]:
        ...
    @classmethod
    def init(
        cls, 
        /, 
        cookies: None | str | PathLike | Mapping[str, str] | Iterable[Mapping | Cookie | Morsel] = None, 
        app: str = "", 
        app_id: int = 0, 
        console_qrcode: bool = True, 
        instance: None | Self = None, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> P115Client | Coroutine[Any, Any, P115Client]:
        def gen_step():
            if instance is None:
                self = cls.__new__(cls)
            else:
                self = instance
            self.app_id = app_id
            if cookies is None:
                yield self.login(
                    app or "alipaymini", 
                    console_qrcode=console_qrcode, 
                    async_=async_, 
                    **request_kwargs, 
                )
            elif isinstance(cookies, PathLike):
                if isinstance(cookies, PurePath) and hasattr(cookies, "open"):
                    self.cookies_path = cookies
                else:
                    self.cookies_path = Path(fsdecode(cookies))
                self._read_cookies()
            elif cookies:
                self.update_cookies(cookies)
            return self
        return run_gen_step(gen_step, async_)

    @classmethod
    def from_path(
        cls, 
        /, 
        path: bytes | str | PathLike = Path("~/115-cookies.txt").expanduser(), 
        app_id: int = 0, 
    ) -> P115Client:
        if not isinstance(path, PurePath):
            path = Path(fsdecode(path))
        return cls(path, app_id=app_id)

    @locked_cacheproperty
    def user_key(self, /) -> str:
        from .util import get_user_key, set_user_key
        try:
            return get_user_key(self.user_id)
        except KeyError:
            resp = self.upload_info()
            check_response(resp)
            return set_user_key(self.user_id, resp["userkey"])

    @overload
    def login(
        self, 
        /, 
        app: None | str = None, 
        console_qrcode: bool = True, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> Self:
        ...
    @overload
    def login(
        self, 
        /, 
        app: None | str = None, 
        console_qrcode: bool = True, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, Self]:
        ...
    def login(
        self, 
        /, 
        app: None | str = None, 
        console_qrcode: bool = True, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> Self | Coroutine[Any, Any, Self]:
        """扫码二维码登录，如果已登录则忽略

        :param app: 扫二维码后绑定的 ``app`` （或者叫 `device`），如果不指定，则根据 cookies 的 UID 字段来确定，如果不能确定，则用 "qandroid"
        :param console_qrcode: 在命令行输出二维码，否则在浏览器中打开
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 返回对象本身

        -----

        :设备列表如下:

        +-------+----------+------------+----------------------+
        | No.   | ssoent   | app        | description          |
        +=======+==========+============+======================+
        | 01    | A1       | web        | 115生活_网页端       |
        +-------+----------+------------+----------------------+
        | --    | A1       | desktop    | 115浏览器            |
        +-------+----------+------------+----------------------+
        | --    | A2       | ?          | 未知: android        |
        +-------+----------+------------+----------------------+
        | --    | A3       | ?          | 未知: ios            |
        +-------+----------+------------+----------------------+
        | --    | A4       | ?          | 未知: ipad           |
        +-------+----------+------------+----------------------+
        | --    | B1       | ?          | 未知: android        |
        +-------+----------+------------+----------------------+
        | 02    | D1       | ios        | 115生活_苹果端       |
        +-------+----------+------------+----------------------+
        | 03    | D2       | bios       | 未知: ios            |
        +-------+----------+------------+----------------------+
        | 04    | D3       | 115ios     | 115_苹果端           |
        +-------+----------+------------+----------------------+
        | 05    | F1       | android    | 115生活_安卓端       |
        +-------+----------+------------+----------------------+
        | 06    | F2       | bandroid   | 未知: android        |
        +-------+----------+------------+----------------------+
        | 07    | F3       | 115android | 115_安卓端           |
        +-------+----------+------------+----------------------+
        | 08    | H1       | ipad       | 115生活_苹果平板端   |
        +-------+----------+------------+----------------------+
        | 09    | H2       | bipad      | 未知: ipad           |
        +-------+----------+------------+----------------------+
        | 10    | H3       | 115ipad    | 115_苹果平板端       |
        +-------+----------+------------+----------------------+
        | 11    | I1       | tv         | 115生活_安卓电视端   |
        +-------+----------+------------+----------------------+
        | 12    | I2       | apple_tv   | 115生活_苹果电视端   |
        +-------+----------+------------+----------------------+
        | 13    | M1       | qandriod   | 115管理_安卓端       |
        +-------+----------+------------+----------------------+
        | 14    | N1       | qios       | 115管理_苹果端       |
        +-------+----------+------------+----------------------+
        | 15    | O1       | qipad      | 115管理_苹果平板端   |
        +-------+----------+------------+----------------------+
        | 16    | P1       | os_windows | 115生活_Windows端    |
        +-------+----------+------------+----------------------+
        | 17    | P2       | os_mac     | 115生活_macOS端      |
        +-------+----------+------------+----------------------+
        | 18    | P3       | os_linux   | 115生活_Linux端      |
        +-------+----------+------------+----------------------+
        | 19    | R1       | wechatmini | 115生活_微信小程序端 |
        +-------+----------+------------+----------------------+
        | 20    | R2       | alipaymini | 115生活_支付宝小程序 |
        +-------+----------+------------+----------------------+
        | 21    | S1       | harmony    | 115_鸿蒙端           |
        +-------+----------+------------+----------------------+
        """
        def gen_step():
            nonlocal app
            status = yield self.login_status(async_=async_, **request_kwargs)
            if status:
                return self
            if not app:
                app = yield self.login_app(async_=async_, **request_kwargs)
            if not app:
                app = "alipaymini"
            resp = yield self.login_with_qrcode(
                app, 
                console_qrcode=console_qrcode, 
                async_=async_, 
                **request_kwargs, 
            )
            while True:
                try:
                    check_response(resp)
                    break
                except P115AuthenticationError:
                    print("login error:", resp)
                    resp = yield self.login_with_qrcode(
                        app, 
                        console_qrcode=console_qrcode, 
                        async_=async_, 
                        **request_kwargs, 
                    )
            self.update_cookies(resp["data"]["cookie"])
            return self
        return run_gen_step(gen_step, async_)

    @overload
    def login_with_app(
        self, 
        /, 
        app: None | str = None, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def login_with_app(
        self, 
        /, 
        app: None | str = None, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def login_with_app(
        self, 
        /, 
        app: None | str = None, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """执行一次自动扫登录二维码，然后绑定到指定设备

        :param app: 绑定的 ``app`` （或者叫 `device`），如果为 None 或 ""，则和当前 client 的登录设备相同
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 响应信息，包含 cookies

        -----

        :设备列表如下:

        +-------+----------+------------+----------------------+
        | No.   | ssoent   | app        | description          |
        +=======+==========+============+======================+
        | 01    | A1       | web        | 115生活_网页端       |
        +-------+----------+------------+----------------------+
        | --    | A1       | desktop    | 115浏览器            |
        +-------+----------+------------+----------------------+
        | --    | A2       | ?          | 未知: android        |
        +-------+----------+------------+----------------------+
        | --    | A3       | ?          | 未知: ios            |
        +-------+----------+------------+----------------------+
        | --    | A4       | ?          | 未知: ipad           |
        +-------+----------+------------+----------------------+
        | --    | B1       | ?          | 未知: android        |
        +-------+----------+------------+----------------------+
        | 02    | D1       | ios        | 115生活_苹果端       |
        +-------+----------+------------+----------------------+
        | 03    | D2       | bios       | 未知: ios            |
        +-------+----------+------------+----------------------+
        | 04    | D3       | 115ios     | 115_苹果端           |
        +-------+----------+------------+----------------------+
        | 05    | F1       | android    | 115生活_安卓端       |
        +-------+----------+------------+----------------------+
        | 06    | F2       | bandroid   | 未知: android        |
        +-------+----------+------------+----------------------+
        | 07    | F3       | 115android | 115_安卓端           |
        +-------+----------+------------+----------------------+
        | 08    | H1       | ipad       | 115生活_苹果平板端   |
        +-------+----------+------------+----------------------+
        | 09    | H2       | bipad      | 未知: ipad           |
        +-------+----------+------------+----------------------+
        | 10    | H3       | 115ipad    | 115_苹果平板端       |
        +-------+----------+------------+----------------------+
        | 11    | I1       | tv         | 115生活_安卓电视端   |
        +-------+----------+------------+----------------------+
        | 12    | I2       | apple_tv   | 115生活_苹果电视端   |
        +-------+----------+------------+----------------------+
        | 13    | M1       | qandriod   | 115管理_安卓端       |
        +-------+----------+------------+----------------------+
        | 14    | N1       | qios       | 115管理_苹果端       |
        +-------+----------+------------+----------------------+
        | 15    | O1       | qipad      | 115管理_苹果平板端   |
        +-------+----------+------------+----------------------+
        | 16    | P1       | os_windows | 115生活_Windows端    |
        +-------+----------+------------+----------------------+
        | 17    | P2       | os_mac     | 115生活_macOS端      |
        +-------+----------+------------+----------------------+
        | 18    | P3       | os_linux   | 115生活_Linux端      |
        +-------+----------+------------+----------------------+
        | 19    | R1       | wechatmini | 115生活_微信小程序端 |
        +-------+----------+------------+----------------------+
        | 20    | R2       | alipaymini | 115生活_支付宝小程序 |
        +-------+----------+------------+----------------------+
        | 21    | S1       | harmony    | 115_鸿蒙端           |
        +-------+----------+------------+----------------------+
        """
        def gen_step():
            nonlocal app
            if not app:
                app = yield self.login_app(async_=async_, **request_kwargs)
            if not app:
                raise ValueError("can't determine the login app")
            uid: str = yield self.login_without_app(async_=async_, **request_kwargs)
            return self.login_qrcode_scan_result(
                uid, 
                app=app, 
                async_=async_, 
                **request_kwargs, 
            )
        return run_gen_step(gen_step, async_)

    @overload
    def login_without_app(
        self, 
        /, 
        show_warning: bool = False, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> str:
        ...
    @overload
    def login_without_app(
        self, 
        /, 
        show_warning: bool = False, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, str]:
        ...
    def login_without_app(
        self, 
        /, 
        show_warning: bool = False, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> str | Coroutine[Any, Any, str]:
        """执行一次自动扫登录二维码，但不绑定设备，返回扫码的 uid，可用于之后绑定设备

        :param show_warning: 是否显示提示信息
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 二维码的 uid
        """
        def gen_step():
            uid = check_response((yield self.login_qrcode_token( # type: ignore
                async_=async_, 
                **request_kwargs, 
            )))["data"]["uid"]
            resp = yield self.login_qrcode_scan(
                uid, 
                async_=async_, 
                **request_kwargs, 
            )
            check_response(resp)
            if show_warning:
                warn(f"qrcode scanned: {resp}", category=P115Warning)
            resp = yield self.login_qrcode_scan_confirm(
                uid, 
                async_=async_, 
                **request_kwargs, 
            )
            check_response(resp)
            return uid
        return run_gen_step(gen_step, async_)

    @overload
    def login_info_open(
        self, 
        /, 
        app_id: int = 0, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def login_info_open(
        self, 
        /, 
        app_id: int = 0, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def login_info_open(
        self, 
        /, 
        app_id: int = 0, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取某个开放接口应用的信息（目前可获得名称和头像）

        :param app_id: AppID
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 接口返回值
        """
        if not app_id:
            app_id = self.app_id
        if not app_id:
            app_id = 100195125
        def gen_step():
            resp = yield self.login_qrcode_token_open(app_id, async_=async_, **request_kwargs)
            check_response(resp)
            login_uid = resp["data"]["uid"]
            resp = yield self.login_qrcode_scan(login_uid, async_=async_, **request_kwargs)
            check_response(resp)
            tip_txt = resp["data"]["tip_txt"]
            return {
                "app_id": app_id, 
                "name": tip_txt[:-10].removeprefix("\ufeff"), 
                "icon": resp["data"]["icon"], 
            }
        return run_gen_step(gen_step, async_)

    @overload
    def login_with_open(
        self, 
        /, 
        app_id: int = 0, 
        *, 
        show_warning: bool = False, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def login_with_open(
        self, 
        /, 
        app_id: int = 0, 
        *, 
        show_warning: bool = False, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def login_with_open(
        self, 
        /, 
        app_id: int = 0, 
        *, 
        show_warning: bool = False, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """登录某个开放接口应用

        .. note::
            同一个开放应用 id，最多同时有 3 个登入，如果有新的登录，则自动踢掉较早的那一个

        :param app_id: AppID
        :param show_warning: 是否显示提示信息
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 接口返回值
        """
        if not app_id:
            app_id = self.app_id
        if not app_id:
            app_id = 100195125
        def gen_step():
            resp = yield self.login_qrcode_token_open(app_id, async_=async_, **request_kwargs)
            check_response(resp)
            login_uid = resp["data"]["uid"]
            resp = yield self.login_qrcode_scan(login_uid, async_=async_, **request_kwargs)
            check_response(resp)
            if show_warning:
                warn(f"qrcode scanned: {resp}", category=P115Warning)
            resp = yield self.login_qrcode_scan_confirm(login_uid, async_=async_, **request_kwargs)
            check_response(resp)
            return self.login_qrcode_access_token_open(login_uid, async_=async_, **request_kwargs)
        return run_gen_step(gen_step, async_)

    @overload
    def login_another_app(
        self, 
        /, 
        app: None | str = None, 
        replace: bool | Self = False, 
        show_warning: bool = False, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> Self:
        ...
    @overload
    def login_another_app(
        self, 
        /, 
        app: None | str = None, 
        replace: bool | Self = False, 
        show_warning: bool = False, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, Self]:
        ...
    def login_another_app(
        self, 
        /, 
        app: None | str = None, 
        replace: bool | Self = False, 
        show_warning: bool = False, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> Self | Coroutine[Any, Any, Self]:
        """登录某个设备（同一个设备可以有多个同时在线，但可以通过某些操作，把除了最近登录的那个都下线，也可以专门把最近登录那个也下线）

        .. hint::
            一个设备被新登录者下线，意味着这个 cookies 失效了，不能执行任何需要权限的操作

            但一个设备的新登录者，并不总是意味着把较早的登录者下线，一般需要触发某个检查机制后，才会把同一设备下除最近一次登录外的所有 cookies 失效

            所以你可以用一个设备的 cookies 专门用于扫码登录，获取另一个设备的 cookies 执行网盘操作，第 2 个 cookies 失效了，则用第 1 个 cookies 扫码，如此可避免单个 cookies 失效后，不能自动获取新的

        :param app: 要登录的 app，如果为 None，则用当前登录设备，如果无当前登录设备，则报错
        :param replace: 替换某个 ``P115Client`` 对象的 cookie

            - 如果为 ``P115Client``, 则更新到此对象
            - 如果为 True，则更新到 `self`
            - 如果为 False，否则返回新的 ``P115Client`` 对象

        :param show_warning: 是否显示提示信息
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 客户端实例

        -----

        :设备列表如下:

        +-------+----------+------------+----------------------+
        | No.   | ssoent   | app        | description          |
        +=======+==========+============+======================+
        | 01    | A1       | web        | 115生活_网页端       |
        +-------+----------+------------+----------------------+
        | --    | A1       | desktop    | 115浏览器            |
        +-------+----------+------------+----------------------+
        | --    | A2       | ?          | 未知: android        |
        +-------+----------+------------+----------------------+
        | --    | A3       | ?          | 未知: ios            |
        +-------+----------+------------+----------------------+
        | --    | A4       | ?          | 未知: ipad           |
        +-------+----------+------------+----------------------+
        | --    | B1       | ?          | 未知: android        |
        +-------+----------+------------+----------------------+
        | 02    | D1       | ios        | 115生活_苹果端       |
        +-------+----------+------------+----------------------+
        | 03    | D2       | bios       | 未知: ios            |
        +-------+----------+------------+----------------------+
        | 04    | D3       | 115ios     | 115_苹果端           |
        +-------+----------+------------+----------------------+
        | 05    | F1       | android    | 115生活_安卓端       |
        +-------+----------+------------+----------------------+
        | 06    | F2       | bandroid   | 未知: android        |
        +-------+----------+------------+----------------------+
        | 07    | F3       | 115android | 115_安卓端           |
        +-------+----------+------------+----------------------+
        | 08    | H1       | ipad       | 115生活_苹果平板端   |
        +-------+----------+------------+----------------------+
        | 09    | H2       | bipad      | 未知: ipad           |
        +-------+----------+------------+----------------------+
        | 10    | H3       | 115ipad    | 115_苹果平板端       |
        +-------+----------+------------+----------------------+
        | 11    | I1       | tv         | 115生活_安卓电视端   |
        +-------+----------+------------+----------------------+
        | 12    | I2       | apple_tv   | 115生活_苹果电视端   |
        +-------+----------+------------+----------------------+
        | 13    | M1       | qandriod   | 115管理_安卓端       |
        +-------+----------+------------+----------------------+
        | 14    | N1       | qios       | 115管理_苹果端       |
        +-------+----------+------------+----------------------+
        | 15    | O1       | qipad      | 115管理_苹果平板端   |
        +-------+----------+------------+----------------------+
        | 16    | P1       | os_windows | 115生活_Windows端    |
        +-------+----------+------------+----------------------+
        | 17    | P2       | os_mac     | 115生活_macOS端      |
        +-------+----------+------------+----------------------+
        | 18    | P3       | os_linux   | 115生活_Linux端      |
        +-------+----------+------------+----------------------+
        | 19    | R1       | wechatmini | 115生活_微信小程序端 |
        +-------+----------+------------+----------------------+
        | 20    | R2       | alipaymini | 115生活_支付宝小程序 |
        +-------+----------+------------+----------------------+
        | 21    | S1       | harmony    | 115_鸿蒙端           |
        +-------+----------+------------+----------------------+
        """
        def gen_step():
            nonlocal app
            if not app and isinstance(replace, P115Client):
                app = yield replace.login_app(async_=True)
            resp = yield self.login_with_app(
                app, 
                show_warning=show_warning, 
                async_=async_, 
                **request_kwargs, 
            )
            check_response(resp)
            cookies = resp["data"]["cookie"]
            ssoent = self.login_ssoent
            if isinstance(replace, P115Client):
                inst = replace
                inst.update_cookies(cookies)
            elif replace:
                inst = self
                inst.update_cookies(cookies)
            else:
                inst = type(self)(cookies)
            if self is not inst and ssoent == inst.login_ssoent:
                warn(f"login with the same ssoent {ssoent!r}, {self!r} will expire within 60 seconds", category=P115Warning)
            return inst
        return run_gen_step(gen_step, async_)

    @overload
    def login_another_open(
        self, 
        /, 
        app_id: int = 0, 
        replace: bool | P115OpenClient = False, 
        show_warning: bool = False, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> P115OpenClient:
        ...
    @overload
    def login_another_open(
        self, 
        /, 
        app_id: int = 0, 
        replace: bool | P115OpenClient = False, 
        show_warning: bool = False, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, P115OpenClient]:
        ...
    def login_another_open(
        self, 
        /, 
        app_id: int = 0, 
        replace: bool | P115OpenClient = False, 
        show_warning: bool = False, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> P115OpenClient | Coroutine[Any, Any, P115OpenClient]:
        """登录某个开放接口应用

        .. note::
            同一个开放应用 id，最多同时有 3 个登入，如果有新的登录，则自动踢掉较早的那一个

        :param app_id: AppID
        :param replace: 替换某个 client 对象的 ``access_token`` 和 `refresh_token`

            - 如果为 ``P115Client``, 则更新到此对象
            - 如果为 True，则更新到 `self`
            - 如果为 False，否则返回新的 ``P115Client`` 对象

        :param show_warning: 是否显示提示信息
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 客户端实例
        """
        if not app_id:
            app_id = self.app_id
        if not app_id:
            app_id = 100195125
        def gen_step():
            resp = yield self.login_with_open(
                app_id, 
                show_warning=show_warning, 
                async_=async_, 
                **request_kwargs, 
            )
            check_response(resp)
            data = resp["data"]
            if replace is False:
                inst: P115OpenClient = P115OpenClient(data["access_token"], data["refresh_token"])
            else:
                if replace is True:
                    inst = self
                else:
                    inst = replace
                inst.refresh_token = data["refresh_token"]
                inst.access_token  = data["access_token"]
            inst.app_id = app_id
            return inst
        return run_gen_step(gen_step, async_)

    @overload
    @classmethod
    def login_bind_app(
        cls, 
        /, 
        uid: str, 
        app: str = "alipaymini", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> Self:
        ...
    @overload
    @classmethod
    def login_bind_app(
        cls, 
        /, 
        uid: str, 
        app: str = "alipaymini", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, Self]:
        ...
    @classmethod
    def login_bind_app(
        cls, 
        /, 
        uid: str, 
        app: str = "alipaymini", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> Self | Coroutine[Any, Any, Self]:
        """获取绑定到某个设备的 cookies

        .. hint::
            同一个设备可以有多个 cookies 同时在线

            其实只要你不主动去执行检查，这些 cookies 可以同时生效，只是看起来像“黑户”

        :param uid: 登录二维码的 uid
        :param app: 待绑定的设备名称
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 新的实例

        -----

        :设备列表如下:

        +-------+----------+------------+----------------------+
        | No.   | ssoent   | app        | description          |
        +=======+==========+============+======================+
        | 01    | A1       | web        | 115生活_网页端       |
        +-------+----------+------------+----------------------+
        | --    | A1       | desktop    | 115浏览器            |
        +-------+----------+------------+----------------------+
        | --    | A2       | ?          | 未知: android        |
        +-------+----------+------------+----------------------+
        | --    | A3       | ?          | 未知: ios            |
        +-------+----------+------------+----------------------+
        | --    | A4       | ?          | 未知: ipad           |
        +-------+----------+------------+----------------------+
        | --    | B1       | ?          | 未知: android        |
        +-------+----------+------------+----------------------+
        | 02    | D1       | ios        | 115生活_苹果端       |
        +-------+----------+------------+----------------------+
        | 03    | D2       | bios       | 未知: ios            |
        +-------+----------+------------+----------------------+
        | 04    | D3       | 115ios     | 115_苹果端           |
        +-------+----------+------------+----------------------+
        | 05    | F1       | android    | 115生活_安卓端       |
        +-------+----------+------------+----------------------+
        | 06    | F2       | bandroid   | 未知: android        |
        +-------+----------+------------+----------------------+
        | 07    | F3       | 115android | 115_安卓端           |
        +-------+----------+------------+----------------------+
        | 08    | H1       | ipad       | 115生活_苹果平板端   |
        +-------+----------+------------+----------------------+
        | 09    | H2       | bipad      | 未知: ipad           |
        +-------+----------+------------+----------------------+
        | 10    | H3       | 115ipad    | 115_苹果平板端       |
        +-------+----------+------------+----------------------+
        | 11    | I1       | tv         | 115生活_安卓电视端   |
        +-------+----------+------------+----------------------+
        | 12    | I2       | apple_tv   | 115生活_苹果电视端   |
        +-------+----------+------------+----------------------+
        | 13    | M1       | qandriod   | 115管理_安卓端       |
        +-------+----------+------------+----------------------+
        | 14    | N1       | qios       | 115管理_苹果端       |
        +-------+----------+------------+----------------------+
        | 15    | O1       | qipad      | 115管理_苹果平板端   |
        +-------+----------+------------+----------------------+
        | 16    | P1       | os_windows | 115生活_Windows端    |
        +-------+----------+------------+----------------------+
        | 17    | P2       | os_mac     | 115生活_macOS端      |
        +-------+----------+------------+----------------------+
        | 18    | P3       | os_linux   | 115生活_Linux端      |
        +-------+----------+------------+----------------------+
        | 19    | R1       | wechatmini | 115生活_微信小程序端 |
        +-------+----------+------------+----------------------+
        | 20    | R2       | alipaymini | 115生活_支付宝小程序 |
        +-------+----------+------------+----------------------+
        | 21    | S1       | harmony    | 115_鸿蒙端           |
        +-------+----------+------------+----------------------+
        """
        def gen_step():
            resp = yield cls.login_qrcode_scan_result(
                uid, 
                app=app, 
                async_=async_, 
                **request_kwargs, 
            )
            check_response(resp)
            cookies = resp["data"]["cookie"]
            return cls(cookies)
        return run_gen_step(gen_step, async_)

    ########## Activity API ##########

    @overload
    def act_xys_adopt(
        self, 
        payload: dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def act_xys_adopt(
        self, 
        payload: dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def act_xys_adopt(
        self, 
        payload: dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """采纳助愿

        POST https://act.115.com/api/1.0/{app}/1.0/act2024xys/adopt

        :payload:
            - did: str 💡 许愿的 id
            - aid: int | str 💡 助愿的 id
            - to_cid: int = <default> 💡 助愿中的分享链接转存到你的网盘中目录的 id
        """
        api = complete_url(f"/api/1.0/{app}/1.0/act2024xys/adopt", base_url=base_url)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def act_xys_aid_desire(
        self, 
        payload: dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def act_xys_aid_desire(
        self, 
        payload: dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def act_xys_aid_desire(
        self, 
        payload: dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """创建助愿（如果提供 file_ids，则会创建一个分享链接）

        POST https://act.115.com/api/1.0/{app}/1.0/act2024xys/aid_desire

        :payload:
            - id: str 💡 许愿 id
            - content: str 💡 助愿文本，不少于 5 个字，不超过 500 个字
            - images: int | str = <default> 💡 图片文件在你的网盘的 id，多个用逗号 "," 隔开
            - file_ids: int | str = <default> 💡 文件在你的网盘的 id，多个用逗号 "," 隔开
        """
        api = complete_url(f"/api/1.0/{app}/1.0/act2024xys/aid_desire", base_url=base_url)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def act_xys_aid_desire_del(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def act_xys_aid_desire_del(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def act_xys_aid_desire_del(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """删除助愿

        POST https://act.115.com/api/1.0/{app}/1.0/act2024xys/del_aid_desire

        :payload:
            - ids: int | str 💡 助愿的 id，多个用逗号 "," 隔开
        """
        api = complete_url(f"/api/1.0/{app}/1.0/act2024xys/del_aid_desire", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"ids": payload}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def act_xys_desire_aid_list(
        self, 
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def act_xys_desire_aid_list(
        self, 
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def act_xys_desire_aid_list(
        self, 
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取许愿的助愿列表

        GET https://act.115.com/api/1.0/{app}/1.0/act2024xys/desire_aid_list

        :payload:
            - id: str         💡 许愿的 id
            - start: int = 0  💡 开始索引
            - page: int = 1   💡 第几页
            - limit: int = 10 💡 分页大小
            - sort: int | str = <default> 💡 排序
        """
        api = complete_url(f"/api/1.0/{app}/1.0/act2024xys/desire_aid_list", base_url=base_url)
        if isinstance(payload, str):
            payload = {"id": payload}
        payload = {"start": 0, "page": 1, "limit": 10, **payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def act_xys_get_act_info(
        self, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def act_xys_get_act_info(
        self, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def act_xys_get_act_info(
        self, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取许愿树活动的信息

        GET https://act.115.com/api/1.0/{app}/1.0/act2024xys/get_act_info
        """
        api = complete_url(f"/api/1.0/{app}/1.0/act2024xys/get_act_info", base_url=base_url)
        return self.request(url=api, async_=async_, **request_kwargs)

    @overload
    def act_xys_get_desire_info(
        self, 
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def act_xys_get_desire_info(
        self, 
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def act_xys_get_desire_info(
        self, 
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取的许愿信息

        GET https://act.115.com/api/1.0/{app}/1.0/act2024xys/get_desire_info

        :payload:
            - id: str 💡 许愿的 id
        """
        api = complete_url(f"/api/1.0/{app}/1.0/act2024xys/get_desire_info", base_url=base_url)
        if isinstance(payload, str):
            payload = {"id": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def act_xys_home_list(
        self, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def act_xys_home_list(
        self, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def act_xys_home_list(
        self, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """首页的许愿树（随机刷新 15 条）

        GET https://act.115.com/api/1.0/{app}/1.0/act2024xys/home_list
        """
        api = complete_url(f"/api/1.0/{app}/1.0/act2024xys/home_list", base_url=base_url)
        return self.request(url=api, async_=async_, **request_kwargs)

    @overload
    def act_xys_my_aid_desire(
        self, 
        payload: int | str | dict = 0, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def act_xys_my_aid_desire(
        self, 
        payload: int | str | dict = 0, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def act_xys_my_aid_desire(
        self, 
        payload: int | str | dict = 0, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """我的助愿列表

        GET https://act.115.com/api/1.0/{app}/1.0/act2024xys/my_aid_desire

        :payload:
            - type: 0 | 1 | 2 = 0 💡 类型

                - 0: 全部
                - 1: 进行中
                - 2: 已实现

            - start: int = 0  💡 开始索引
            - page: int = 1   💡 第几页
            - limit: int = 10 💡 分页大小
        """
        api = complete_url(f"/api/1.0/{app}/1.0/act2024xys/my_aid_desire", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"type": payload}
        payload = {"type": 0, "start": 0, "page": 1, "limit": 10, **payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def act_xys_my_desire(
        self, 
        payload: int | str | dict = 0, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def act_xys_my_desire(
        self, 
        payload: int | str | dict = 0, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def act_xys_my_desire(
        self, 
        payload: int | str | dict = 0, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """我的许愿列表

        GET https://act.115.com/api/1.0/{app}/1.0/act2024xys/my_desire

        :payload:
            - type: 0 | 1 | 2 = 0 💡 类型

                - 0: 全部
                - 1: 进行中
                - 2: 已实现

            - start: int = 0  💡 开始索引
            - page: int = 1   💡 第几页
            - limit: int = 10 💡 分页大小
        """
        api = complete_url(f"/api/1.0/{app}/1.0/act2024xys/my_desire", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"type": payload}
        payload = {"type": 0, "start": 0, "page": 1, "limit": 10, **payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def act_xys_wish(
        self, 
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def act_xys_wish(
        self, 
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def act_xys_wish(
        self, 
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """创建许愿

        POST https://act.115.com/api/1.0/{app}/1.0/act2024xys/wish

        :payload:
            - content: str 💡 许愿文本，不少于 5 个字，不超过 500 个字
            - rewardSpace: int = 5 💡 奖励容量，单位是 GB
            - images: int | str = <default> 💡 图片文件在你的网盘的 id，多个用逗号 "," 隔开
        """
        api = complete_url(f"/api/1.0/{app}/1.0/act2024xys/wish", base_url=base_url)
        if isinstance(payload, str):
            payload = {"content": payload}
        payload.setdefault("rewardSpace", 5)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def act_xys_wish_del(
        self, 
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def act_xys_wish_del(
        self, 
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def act_xys_wish_del(
        self, 
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://act.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """删除许愿

        POST https://act.115.com/api/1.0/{app}/1.0/act2024xys/del_wish

        :payload:
            - ids: str 💡 许愿的 id，多个用逗号 "," 隔开
        """
        api = complete_url(f"/api/1.0/{app}/1.0/act2024xys/del_wish", base_url=base_url)
        if isinstance(payload, str):
            payload = {"ids": payload}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    ########## Captcha System API ##########

    @overload
    def captcha_all(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://captchaapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> bytes:
        ...
    @overload
    def captcha_all(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://captchaapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, bytes]:
        ...
    def captcha_all(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://captchaapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> bytes | Coroutine[Any, Any, bytes]:
        """返回一张包含 10 个汉字的图片，包含验证码中 4 个汉字（有相应的编号，从 0 到 9，计数按照从左到右，从上到下的顺序）

        GET https://captchaapi.115.com/?ct=index&ac=code&t=all
        """
        api = complete_url(base_url=base_url, query={"ct": "index", "ac": "code", "t": "all"})
        request_kwargs.setdefault("parse", False)
        return self.request(url=api, async_=async_, **request_kwargs)

    @overload
    def captcha_code(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://captchaapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> bytes:
        ...
    @overload
    def captcha_code(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://captchaapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, bytes]:
        ...
    def captcha_code(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://captchaapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> bytes | Coroutine[Any, Any, bytes]:
        """更新验证码，并获取图片数据（含 4 个汉字）

        GET https://captchaapi.115.com/?ct=index&ac=code
        """
        api = complete_url(base_url=base_url, query={"ct": "index", "ac": "code"})
        request_kwargs.setdefault("parse", False)
        return self.request(url=api, async_=async_, **request_kwargs)

    @overload
    def captcha_sign(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://captchaapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def captcha_sign(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://captchaapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def captcha_sign(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://captchaapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取验证码的签名字符串

        GET https://captchaapi.115.com/?ac=code&t=sign
        """
        api = complete_url(base_url=base_url, query={"ac": "code", "t": "sign"})
        return self.request(url=api, async_=async_, **request_kwargs)

    @overload
    def captcha_single(
        self, 
        payload: dict | int = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://captchaapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> bytes:
        ...
    @overload
    def captcha_single(
        self, 
        payload: dict | int = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://captchaapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, bytes]:
        ...
    def captcha_single(
        self, 
        payload: dict | int = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://captchaapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> bytes | Coroutine[Any, Any, bytes]:
        """10 个汉字单独的图片，包含验证码中 4 个汉字，编号从 0 到 9

        GET https://captchaapi.115.com/?ct=index&ac=code&t=single

        :payload:
            - id: int = 0
        """
        api = complete_url(base_url=base_url, query={"ct": "index", "ac": "code", "t": "single"})
        if not isinstance(payload, dict):
            payload = {"id": payload}
        request_kwargs.setdefault("parse", False)
        return self.request(url=api, async_=async_, **request_kwargs)

    @overload
    def captcha_verify(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def captcha_verify(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def captcha_verify(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """提交验证码

        POST https://webapi.115.com/user/captcha

        :payload:
            - code: int | str 💡 从 0 到 9 中选取 4 个数字的一种排列
            - sign: str = <default>     💡 来自 ``captcha_sign`` 接口的响应
            - ac: str = "security_code" 💡 默认就行，不要自行决定
            - type: str = "web"         💡 默认就行，不要自行决定
            - ctype: str = "web"        💡 需要和 type 相同
            - client: str = "web"       💡 需要和 type 相同
        """
        api = complete_url("/user/captcha", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"code": payload}
        payload = {"ac": "security_code", "type": "web", "ctype": "web", "client": "web", **payload}
        def gen_step():
            if "sign" not in payload:
                resp = yield self.captcha_sign(async_=async_)
                payload["sign"] = resp["sign"]
            return self.request(
                url=api, 
                method="POST", 
                data=payload, 
                async_=async_, 
                **request_kwargs, 
            )
        return run_gen_step(gen_step, async_)

    ########## Cloud Download API ##########

    @overload
    def _clouddownload_web_request(
        self, 
        payload: dict = {}, 
        /, 
        action: str = "", 
        method: str = "POST", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def _clouddownload_web_request(
        self, 
        payload: dict = {}, 
        /, 
        action: str = "", 
        method: str = "POST", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def _clouddownload_web_request(
        self, 
        payload: dict = {}, 
        /, 
        action: str = "", 
        method: str = "POST", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        api = complete_url("/web/", base_url=base_url)
        if action:
            payload["ac"] = action
        if method.upper() == "POST":
            request_kwargs["data"] = payload
        else:
            request_kwargs["params"] = payload
        return self.request(
            url=api, 
            method=method, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def _clouddownload_request(
        self, 
        payload: dict = {}, 
        /, 
        action: str = "", 
        method: str = "POST", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def _clouddownload_request(
        self, 
        payload: dict = {}, 
        /, 
        action: str = "", 
        method: str = "POST", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def _clouddownload_request(
        self, 
        payload: dict = {}, 
        /, 
        action: str = "", 
        method: str = "POST", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        api = complete_url("/", base_url=base_url)
        if action:
            payload["ac"] = action
        if method.upper() == "POST":
            request_kwargs["data"] = payload
            request_kwargs.setdefault("ecdh_encrypt", True)
        else:
            request_kwargs["params"] = payload
        return self.request(
            url=api, 
            method=method, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def _clouddownload_lixianssp_request(
        self, 
        payload: dict = {}, 
        /, 
        action: str = "", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def _clouddownload_lixianssp_request(
        self, 
        payload: dict = {}, 
        /, 
        action: str = "", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def _clouddownload_lixianssp_request(
        self, 
        payload: dict = {}, 
        /, 
        action: str = "", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        api = complete_url("/lixianssp/", base_url=base_url)
        request_kwargs["method"] = "POST"
        for k, v in payload.items():
            payload[k] = str(v)
        if action:
            payload["ac"] = action
        app_ver = payload.setdefault("app_ver", _app_version)
        request_kwargs["headers"] = {
            **(request_kwargs.get("headers") or {}), 
            "user-agent": f"Mozilla/5.0 115disk/{app_ver} 115Browser/{app_ver} 115wangpan_android/{app_ver}", 
        }
        request_kwargs["ecdh_encrypt"] = False
        def parse(_, content: bytes, /) -> dict:
            json = json_maybe_decrypt_loads(content)
            if data := json.get("data"):
                try:
                    json["data"] = json_loads(rsa_decrypt(data))
                except Exception:
                    pass
            return json
        request_kwargs.setdefault("parse", parse)
        return self.request(
            url=api, 
            data={"data": rsa_encrypt(dumps(payload)).decode("ascii")}, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def clouddownload_request(
        self, 
        payload: dict = {}, 
        /, 
        action: str = "", 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_request(
        self, 
        payload: dict = {}, 
        /, 
        action: str = "", 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_request(
        self, 
        payload: dict = {}, 
        /, 
        action: str = "", 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        match type:
            case "web":
                call: Callable = self._clouddownload_web_request
            case "ssp":
                call = self._clouddownload_lixianssp_request
            case _:
                call = self._clouddownload_request
        return call(
            payload, 
            action, 
            method=method, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def clouddownload_downpath(
        self, 
        payload: dict | int = 10, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_downpath(
        self, 
        payload: dict | int = 10, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_downpath(
        self, 
        payload: dict | int = 10, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取当前默认的云下载到的目录信息（可能有多个）

        GET https://webapi.115.com/offine/downpath

        :payload:
            - limit: int = 1150
        """
        api = complete_url("/offine/downpath", base_url=base_url)
        if isinstance(payload, int):
            payload = {"limit": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def clouddownload_downpath_set(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_downpath_set(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_downpath_set(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """设置默认选择的云下载到的目录信息

        POST https://webapi.115.com/offine/downpath

        :payload:
            - file_id: int | str 💡 目录 id
        """
        api = complete_url("/offine/downpath", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"file_id": payload}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def clouddownload_get_id(
        self, 
        payload: dict | int = 1, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_get_id(
        self, 
        payload: dict | int = 1, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_get_id(
        self, 
        payload: dict | int = 1, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取和云下载有关的目录 id

        GET https://clouddownload.115.com/?ac=get_id

        .. note::
            调用此接口后，如果相关目录不存在，则会自动创建。
            响应数据里，"cid" 对应的是上传的种子文件的保存目录，"dest_cid" 是云下载的目录

        :payload:
            - torrent: int = 1
        """
        if isinstance(payload, int):
            payload = {"torrent": payload}
        return self.clouddownload_request(
            payload, 
            "get_id", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload # type: ignore
    def clouddownload_quota_info(
        self, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_quota_info(
        self, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_quota_info(
        self, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取当前云下载配额信息（简略）

        GET https://clouddownload.115.com/?ac=get_quota_info
        """
        return self.clouddownload_request(
            ac="get_quota_info", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def clouddownload_quota_package_array(
        self, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_quota_package_array(
        self, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_quota_package_array(
        self, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取当前云下载配额信息（详细）

        GET https://clouddownload.115.com/?ac=get_quota_package_array
        """
        return self.clouddownload_request(
            ac="get_quota_package_array", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def clouddownload_quota_package_info(
        self, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_quota_package_info(
        self, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_quota_package_info(
        self, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取当前云下载配额信息（详细）

        GET https://clouddownload.115.com/?ac=get_quota_package_info
        """
        return self.clouddownload_request(
            ac="get_quota_package_info", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def clouddownload_sign(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_sign(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_sign(
        self, 
        /, 
        base_url: str | Callable[[], str] = "https://115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取 sign 和 time 字段（各个添加任务的接口需要），以及其它信息

        GET https://115.com/?ct=clouddownload&ac=space
        """
        api = complete_url(base_url=base_url, query={"ct": "clouddownload", "ac": "space"})
        return self.request(url=api, async_=async_, **request_kwargs)

    @overload
    def clouddownload_sign_app(
        self, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_sign_app(
        self, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_sign_app(
        self, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取 sign 和 time 字段（各个添加任务的接口需要）

        GET https://proapi.115.com/{app}/files/offlinesign
        """
        api = complete_url("/files/offlinesign", base_url=base_url, app=app)
        return self.request(url=api, async_=async_, **request_kwargs)

    @overload
    def clouddownload_task(
        self, 
        payload: str | dict, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task(
        self, 
        payload: str | dict, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task(
        self, 
        payload: str | dict, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取云下载任务信息

        GET https://clouddownload.115.com/?ac=get_user_task

        :payload:
            - info_hash: str
        """
        if isinstance(payload, str):
            payload = {"info_hash": payload}
        return self.clouddownload_request(
            payload, 
            "get_user_task", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload # type: ignore
    def clouddownload_task_add_bt(
        self, 
        payload: str | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "ssp", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_add_bt(
        self, 
        payload: str | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "ssp", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_add_bt(
        self, 
        payload: str | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "ssp", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """添加一个种子作为云下载任务

        POST https://clouddownload.115.com/lixianssp/?ac=add_task_bt

        .. note::
            ``client.clouddownload_task_add_bt(info_hash)`` 相当于 ``client.clouddownload_task_add_url(f"magnet:?xt=urn:btih:{info_hash}")``

            但此接口的优势是允许选择要下载的文件

        :payload:
            - info_hash: str 💡 种子文件的 info_hash
            - wanted: str = <default> 💡 选择文件进行下载（是数字索引，从 0 开始计数，用 "," 分隔）
            - savepath: str = <default> 💡 保存到 ``wp_path_id`` 对应目录下的相对路径
            - wp_path_id: int | str = <default> 💡 保存到目录的 id
        """
        if isinstance(payload, str):
            payload = {"info_hash": payload}
        return self.clouddownload_request(
            payload, 
            "add_task_bt", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def clouddownload_task_add_url(
        self, 
        payload: str | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "ssp", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_add_url(
        self, 
        payload: str | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "ssp", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_add_url(
        self, 
        payload: str | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "ssp", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """添加一个云下载任务

        POST https://clouddownload.115.com/lixianssp/?ac=add_task_url

        :payload:
            - url: str 💡 链接，支持HTTP、HTTPS、FTP、磁力链和电驴链接
            - savepath: str = <default> 💡 保存到目录下的相对路径
            - wp_path_id: int | str = <default> 💡 保存到目录的 id
        """
        if isinstance(payload, str):
            payload = {"url": payload}
        return self.clouddownload_request(
            payload, 
            "add_task_url", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload # type: ignore
    def clouddownload_task_add_urls(
        self, 
        payload: str | Iterable[str] | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "ssp", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_add_urls(
        self, 
        payload: str | Iterable[str] | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "ssp", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_add_urls(
        self, 
        payload: str | Iterable[str] | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "ssp", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """添加一组云下载任务

        POST https://clouddownload.115.com/lixianssp/?ac=add_task_urls

        :payload:
            - url: str    💡 链接，支持HTTP、HTTPS、FTP、磁力链和电驴链接
            - url[0]: str 💡 链接，支持HTTP、HTTPS、FTP、磁力链和电驴链接
            - url[1]: str
            - ...
            - savepath: str = <default> 💡 保存到目录下的相对路径
            - wp_path_id: int | str = <default> 💡 保存到目录的 id
        """
        if isinstance(payload, str):
            payload = payload.strip("\n").split("\n")
        if not isinstance(payload, dict):
            payload = {f"url[{i}]": url for i, url in enumerate(payload) if url}
            if not payload:
                raise ValueError("no ``url`` specified")
        return self.clouddownload_request(
            payload, 
            "add_task_urls", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload # type: ignore
    def clouddownload_task_clear(
        self, 
        payload: int | dict = 0, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_clear(
        self, 
        payload: int | dict = 0, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_clear(
        self, 
        payload: int | dict = 0, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """清空云下载任务列表

        POST https://clouddownload.115.com/?ac=task_clear

        :payload:
            - flag: int = 0 💡 标识，用于对应某种情况

                - 0: 已完成
                - 1: 全部
                - 2: 已失败
                - 3: 进行中
                - 4: 已完成+删除源文件
                - 5: 全部+删除源文件
        """
        if isinstance(payload, int):
            payload = {"flag": payload}
        return self.clouddownload_request(
            payload, 
            "task_clear", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def clouddownload_task_cnt(
        self, 
        payload: dict | int = 0, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_cnt(
        self, 
        payload: dict | int = 0, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_cnt(
        self, 
        payload: dict | int = 0, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取当前正在运行的云下载任务数

        GET https://clouddownload.115.com/?ac=get_task_cnt

        :payload:
            - flag: int = 0
        """
        if isinstance(payload, int):
            payload = {"flag": payload}
        return self.clouddownload_request(
            payload, 
            "get_task_cnt", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def clouddownload_task_count(
        self, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_count(
        self, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_count(
        self, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取当前各种类型任务的计数

        GET https://clouddownload.115.com/?ac=task_count
        """
        return self.clouddownload_request(
            action="task_count", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload # type: ignore
    def clouddownload_task_del(
        self, 
        payload: str | Iterable[str] | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_del(
        self, 
        payload: str | Iterable[str] | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_del(
        self, 
        payload: str | Iterable[str] | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """删除一组云下载任务（无论是否已经完成）

        POST https://clouddownload.115.com/?ac=task_del

        :payload:
            - hash[0]: str
            - hash[1]: str
            - ...
            - flag: 0 | 1 = <default> 💡 是否删除源文件
        """
        if isinstance(payload, str):
            payload = {"hash[0]": payload}
        elif not isinstance(payload, dict):
            payload = {f"hash[{i}]": hash for i, hash in enumerate(payload)}
            if not payload:
                raise ValueError("no ``hash`` (info_hash) specified")
        return self.clouddownload_request(
            payload, 
            "task_del", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload # type: ignore
    def clouddownload_task_list(
        self, 
        payload: int | dict = 1, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_list(
        self, 
        payload: int | dict = 1, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_list(
        self, 
        payload: int | dict = 1, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取当前的云下载任务列表

        GET https://clouddownload.115.com/?ac=task_lists

        :payload:
            - page: int = 1
            - page_size: int = 30
            - stat: int = 0 💡 已知：<=0:全部 1-9:已失败 10-11:已完成 12:进行中 >=13:同10
        """
        if isinstance(payload, int):
            payload = {"page": payload}
        payload.setdefault("page_size", 30)
        return self.clouddownload_request(
            payload, 
            "task_lists", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def clouddownload_task_pause(
        self, 
        payload: str | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_pause(
        self, 
        payload: str | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_pause(
        self, 
        payload: str | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """暂停云下载任务

        POST https://clouddownload.115.com/?ac=pause_task

        :payload:
            - info_hash: str 💡 待重试任务的 info_hash
        """
        if isinstance(payload, str):
            payload = {"info_hash": payload}
        return self.clouddownload_request(
            payload, 
            "pause_task", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def clouddownload_task_restart(
        self, 
        payload: str | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_restart(
        self, 
        payload: str | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_restart(
        self, 
        payload: str | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """重试云下载任务

        POST https://clouddownload.115.com/?ac=restart

        :payload:
            - info_hash: str 💡 待重试任务的 info_hash
        """
        if isinstance(payload, str):
            payload = {"info_hash": payload}
        return self.clouddownload_request(
            payload, 
            "restart", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def clouddownload_task_resume(
        self, 
        payload: str | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_task_resume(
        self, 
        payload: str | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_task_resume(
        self, 
        payload: str | dict, 
        /, 
        method: str = "POST", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """恢复（被暂停的）云下载任务

        POST https://clouddownload.115.com/?ac=resume_task

        :payload:
            - info_hash: str 💡 待重试任务的 info_hash
        """
        if isinstance(payload, str):
            payload = {"info_hash": payload}
        return self.clouddownload_request(
            payload, 
            "resume_task", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload # type: ignore
    def clouddownload_torrent(
        self, 
        payload: str | dict, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def clouddownload_torrent(
        self, 
        payload: str | dict, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def clouddownload_torrent(
        self, 
        payload: str | dict, 
        /, 
        method: str = "GET", 
        type: Literal["", "web", "ssp"] = "web", 
        base_url: str | Callable[[], str] = "https://clouddownload.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """查看种子的文件列表等信息

        GET https://clouddownload.115.com/?ac=torrent

        :payload:
            - sha1: str
        """
        if isinstance(payload, str):
            payload = {"sha1": payload}
        return self.clouddownload_request(
            payload, 
            "torrent", 
            method=method, 
            type=type, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    ########## Diary API ##########

    @overload
    def diary_add(
        self, 
        payload: str | dict | list, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def diary_add(
        self, 
        payload: str | dict | list, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def diary_add(
        self, 
        payload: str | dict | list, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """新建日记

        POST https://life.115.com/api/1.0/{app}/1.0/diary/add

        :payload:
            - form[content]: str 💡 内容
            - form[subject]: int | str = <default> 💡 标题
            - form[user_time]: int | float = <default> 💡 时间戳，单位是秒
            - form[weather]: int = <default> 💡 天气
            - form[mood]: int = <default> 💡 心情
            - form[moods]: int | str = <default> 💡 心情，多个用逗号 "," 隔开
            - form[tags][]: str = <default> 💡 标签
            - ...
            - form[tags][0]: str = <default> 💡 标签
            - ...
            - form[index_image] = <default> 💡 封面图片链接
            - form[address]: str = <default>           💡 地点
            - form[location]: str = <default>          💡 地名
            - form[longitude]: float | str = <default> 💡 经度
            - form[latitude]: float | str = <default>  💡 纬度
            - form[mid]: str = <default>               💡 位置编码
            - form[maps]: list[dict] = <default>       💡 多个地图位置
            - form[maps][0][address]: str = <default>
            - form[maps][0][location]: str = <default>
            - form[maps][0][latitude]: float | str = <default>
            - form[maps][0][longitude] float | str = <default>
            - form[maps][0][mid]: str = <default>
            - ...
        """
        api = complete_url(f"/api/1.0/{app}/1.0/diary/add", base_url)
        now = int(time())
        if isinstance(payload, str):
            payload = {"form[content]": payload, "form[user_time]": now}
        elif isinstance(payload, dict):
            payload = dict(expand_payload(payload, prefix="form", enum_seq=True))
            payload.setdefault("form[user_time]", now)
        elif isinstance(payload, list):
            payload = [("form[user_time]", now), *expand_payload(payload, prefix="form")]
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def diary_del(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def diary_del(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def diary_del(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """删除日记

        POST https://life.115.com/api/1.0/{app}/1.0/diary/delete

        :payload:
            - diary_id: int | str 💡 日记 id
        """
        api = complete_url(f"/api/1.0/{app}/1.0/diary/delete", base_url)
        if isinstance(payload, (int, str)):
            payload = {"diary_id": payload}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def diary_detail(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def diary_detail(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def diary_detail(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取日记详情

        GET https://life.115.com/api/1.0/{app}/1.0/diary/detail

        :payload:
            - diary_id: int | str 💡 日记 id
            - format: str = html
        """
        api = complete_url(f"/api/1.0/{app}/1.0/diary/detail", base_url)
        if isinstance(payload, (int, str)):
            payload = {"diary_id": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def diary_detail2(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def diary_detail2(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def diary_detail2(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取日记详情

        GET https://life.115.com/api/1.0/{app}/1.0/life/diarydetail

        :payload:
            - diary_id: int | str 💡 日记 id
            - format: str = html
        """
        api = complete_url(f"/api/1.0/{app}/1.0/life/diarydetail", base_url)
        if isinstance(payload, (int, str)):
            payload = {"diary_id": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def diary_edit(
        self, 
        payload: dict | list, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def diary_edit(
        self, 
        payload: dict | list, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def diary_edit(
        self, 
        payload: dict | list, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """修改日记

        POST https://life.115.com/api/1.0/{app}/1.0/diary/edit

        :payload:
            - form[diary_id]: str 💡 日记 id
            - form[content]: str = <default> 💡 内容
            - form[subject]: int | str = <default> 💡 标题
            - form[user_time]: int | float = <default> 💡 时间戳，单位是秒
            - form[weather]: int = <default> 💡 天气
            - form[mood]: int = <default> 💡 心情
            - form[moods]: int | str = <default> 💡 心情，多个用逗号 "," 隔开
            - form[tags][]: str = <default> 💡 标签
            - ...
            - form[tags][0]: str = <default> 💡 标签
            - ...
            - form[index_image] = <default> 💡 封面图片链接
            - form[address]: str = <default>           💡 地点
            - form[location]: str = <default>          💡 地名
            - form[longitude]: float | str = <default> 💡 经度
            - form[latitude]: float | str = <default>  💡 纬度
            - form[mid]: str = <default>               💡 位置编码
            - form[maps]: list[dict] = <default>       💡 多个地图位置
            - form[maps][0][address]: str = <default>
            - form[maps][0][location]: str = <default>
            - form[maps][0][latitude]: float | str = <default>
            - form[maps][0][longitude] float | str = <default>
            - form[maps][0][mid]: str = <default>
            - ...
        """
        api = complete_url(f"/api/1.0/{app}/1.0/diary/edit", base_url)
        if isinstance(payload, dict):
            payload = dict(expand_payload(payload, prefix="form", enum_seq=True))
        elif isinstance(payload, list):
            payload = list(expand_payload(payload, prefix="form"))
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def diary_get_config(
        self, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def diary_get_config(
        self, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def diary_get_config(
        self, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取日记可选项（例如天气、心情等）的取值集合

        GET https://life.115.com/api/1.0/{app}/1.0/diary/get_diary_config
        """
        api = complete_url(f"/api/1.0/{app}/1.0/diary/get_diary_config", base_url)
        return self.request(url=api, async_=async_, **request_kwargs)

    @overload
    def diary_get_latest_tags(
        self, 
        payload: int | str | dict = {}, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def diary_get_latest_tags(
        self, 
        payload: int | str | dict = {}, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def diary_get_latest_tags(
        self, 
        payload: int | str | dict = {}, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取最近使用过的标签列表

        GET https://life.115.com/api/1.0/{app}/1.0/diary/getlatesttags

        :payload:
            - q: str = "" 💡 筛选关键词
            - color: 0 | 1 = <default>
            - limit: int = <default> 💡 最多返回数量，⚠️ 这个参数似乎无效
        """
        api = complete_url(f"/api/1.0/{app}/1.0/diary/getlatesttags", base_url)
        if isinstance(payload, int):
            payload = {"limit": payload}
        elif isinstance(payload, str):
            payload = {"q": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def diary_get_tag_color(
        self, 
        payload: str | list | tuple | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def diary_get_tag_color(
        self, 
        payload: str | list | tuple | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def diary_get_tag_color(
        self, 
        payload: str | list | tuple | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取标签的颜色

        POST https://life.115.com/api/1.0/{app}/1.0/diary/gettagcolor

        :payload:
            - tags: str 💡 标签文本
            - tags[]: str
            - ...
            - tags[0]: str
            - tags[1]: str
            - ...
        """
        api = complete_url(f"/api/1.0/{app}/1.0/diary/gettagcolor", base_url)
        if not isinstance(payload, dict):
            if isinstance(payload, (list, tuple)):
                payload = [t if isinstance(t, (list, tuple)) else ("tags[]", str(t)) for t in payload]
            else:
                payload = {"tags": str(payload)}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def diary_list(
        self, 
        payload: int | dict = 0, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def diary_list(
        self, 
        payload: int | dict = 0, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def diary_list(
        self, 
        payload: int | dict = 0, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取日记列表，此接口是对 ``life_glist`` 的封装

        :payload:
            - start: int = 0 💡 开始索引，从 0 开始
            - limit: int = <default> 💡 分页大小
            - only_public: 0 | 1 = <default>
            - msg_note: 0 | 1 = <default>
            - option: 0 | 1 = <default>
        """
        if isinstance(payload, int):
            payload = {"start": payload}
        else:
            payload = dict(payload)
        payload.setdefault("type", 5)
        return self.life_glist(payload, app=app, base_url=base_url, async_=async_, **request_kwargs)

    @overload
    def diary_search(
        self, 
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def diary_search(
        self, 
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def diary_search(
        self, 
        payload: str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """搜索日记

        GET https://life.115.com/api/1.0/{app}/1.0/diary/search

        :payload:
            - q: str 💡 关键词
            - start: int = 0 💡 开始索引，从 0 开始
            - limit: int = <default> 💡 分页大小
            - display_list: 0 | 1 = <default>
        """
        api = complete_url(f"/api/1.0/{app}/1.0/diary/search", base_url)
        if isinstance(payload, str):
            payload = {"q": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def diary_settag(
        self, 
        payload: dict | list, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def diary_settag(
        self, 
        payload: dict | list, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def diary_settag(
        self, 
        payload: dict | list, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """设置日记标签

        POST https://life.115.com/api/1.0/{app}/1.0/diary/settag

        :payload:
            - diary_id: int | str 💡 日记 id
            - tags: str
            - tags[]: str
            - ...
            - tags[0]: str
            - tags[1]: str
            - ...
        """
        api = complete_url(f"/api/1.0/{app}/1.0/diary/settag", base_url)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def diary_settop(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def diary_settop(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def diary_settop(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "web", 
        base_url: str | Callable[[], str] = "https://life.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """切换日记的置顶状态，此接口是对 ``life_set_top`` 的封装

        .. attention::
            这个接口会自动切换日记的置顶状态，但不支持手动指定是否置顶，只是在置顶和不置顶间来回切换。

        :payload:
            - relation_id: int | str 💡 日记 id
        """
        if isinstance(payload, (int, str)):
            payload = {"relation_id": payload}
        payload.setdefault("type", 5)
        return self.life_set_top(payload, app=app, base_url=base_url, async_=async_, **request_kwargs)

    ########## Download API ##########

    @overload
    def download_files(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def download_files(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def download_files(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取待下载的文件列表

        GET https://webapi.115.com/files/downfiles

        .. caution::
            不允许直接从根目录获取，因为根目录没有 ``pickcode``

        :payload:
            - pickcode: str 💡 提取码
            - page: int = 1 💡 第几页
            - per_page: int = 5000 💡 每页大小，目前最大为 5000
        """
        api = complete_url(f"/files/downfiles", base_url)
        if isinstance(payload, str):
            payload = {"pickcode": payload}
        payload = {"page": 1, "per_page": 5000, **payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def download_files_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "chrome", 
        version: str = "2.0", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def download_files_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "chrome", 
        version: str = "2.0", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def download_files_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "chrome", 
        version: str = "2.0", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取待下载的文件列表

        GET https://proapi.115.com/app/chrome/downfiles

        .. caution::
            不允许直接从根目录获取，因为根目录没有 ``pickcode``

        .. tip::
            如果 ``app`` 不是 "chrome"，那么会多一个字段 "sha1"，虽依然没有文件名，但有 "fs"（文件大小），搭配 "sha1" 可以用于检测文件重复

        :payload:
            - pickcode: str 💡 提取码
            - page: int = 1 💡 第几页
            - per_page: int = 5000 💡 每页大小，目前最大为 5000
        """
        if app in ("", "web", "desktop", "chrome"):
            api = complete_url(f"/app/{version}/chrome/downfiles", base_url)
        else:
            if app not in ("windows", "mac", "linux", "os_windows", "os_mac", "os_linux"):
                app = "os_windows"
            api = complete_url("/ufile/downfiles", base_url, app=app, version=version)
            request_kwargs.setdefault("ecdh_encrypt", True)
        if isinstance(payload, str):
            payload = {"pickcode": payload}
        payload = {"page": 1, "per_page": 5000, **payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def download_folders(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def download_folders(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def download_folders(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取待下载的目录列表

        GET https://webapi.115.com/files/downfolders

        .. caution::
            不允许直接从根目录获取，因为根目录没有 ``pickcode``

        :payload:
            - pickcode: str 💡 提取码
            - page: int = 1 💡 第几页
            - per_page: int = 5000 💡 每页大小，目前最大为 5000
        """
        api = complete_url(f"/files/downfolders", base_url)
        if isinstance(payload, str):
            payload = {"pickcode": payload}
        payload = {"page": 1, "per_page": 5000, **payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def download_folders_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "chrome", 
        version: str = "2.0", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def download_folders_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "chrome", 
        version: str = "2.0", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def download_folders_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "chrome", 
        version: str = "2.0", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取待下载的目录列表

        GET https://proapi.115.com/app/chrome/downfolders

        .. caution::
            不允许直接从根目录获取，因为根目录没有 ``pickcode``

        :payload:
            - pickcode: str 💡 提取码
            - page: int = 1 💡 第几页
            - per_page: int = 5000 💡 每页大小，目前最大为 5000
        """
        if app in ("", "web", "desktop", "chrome"):
            api = complete_url(f"/app/{version}/chrome/downfolders", base_url)
        else:
            if app not in ("windows", "mac", "linux", "os_windows", "os_mac", "os_linux"):
                app = "os_windows"
            api = complete_url("/ufile/downfolders", base_url, app=app, version=version)
            request_kwargs.setdefault("ecdh_encrypt", True)
        if isinstance(payload, str):
            payload = {"pickcode": payload}
        payload = {"page": 1, "per_page": 5000, **payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def download_downfolder_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "chrome", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def download_downfolder_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "chrome", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def download_downfolder_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "chrome", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取待下载的文件列表

        GET https://proapi.115.com/{app}/folder/downfolder

        .. caution::
            一次性拉完，当文件过多时，会报错

        :payload:
            - pickcode: str 💡 提取码
            - share_id: int | str 💡 共享 id
        """
        if app in ("", "web", "desktop", "chrome"):
            api = complete_url(f"/app/chrome/downfolder", base_url)
        else:
            api = complete_url("/folder/downfolder", base_url=base_url, app=app)
        if isinstance(payload, str):
            payload = {"pickcode": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def download_url(
        self, 
        pickcode: int | str, 
        /, 
        strict: bool = True, 
        user_agent: None | str = None, 
        app: str = "os_windows", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> P115URL:
        ...
    @overload
    def download_url(
        self, 
        pickcode: int | str, 
        /, 
        strict: bool = True, 
        user_agent: None | str = None, 
        app: str = "os_windows", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, P115URL]:
        ...
    def download_url(
        self, 
        pickcode: int | str, 
        /, 
        strict: bool = True, 
        user_agent: None | str = None, 
        app: str = "os_windows", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> P115URL | Coroutine[Any, Any, P115URL]:
        """获取文件的下载链接

        .. note::
            获取的直链中，部分查询参数的解释：

            - ``t``: 过期时间戳
            - ``u``: 用户 id
            - ``c``: 允许同时打开次数，如果为 0，则是无限次数
            - ``f``: 请求时要求携带请求头
                - 如果为空，则无要求
                - 如果为 1，则需要 user-agent（和请求直链时的一致）
                - 如果为 3，则需要 user-agent（和请求直链时的一致） 和 Cookie（由请求直链时的响应所返回的 Set-Cookie 响应头）

        :param pickcode: 提取码
        :param strict: 如果为 True，当目标是目录时，会抛出 IsADirectoryError 异常
        :param user_agent: 如果不为 None，则作为请求头 "user-agent" 的值
        :param app: 使用此设备的接口
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 下载链接
        """
        if app == "open":
            return self.download_url_open(
                pickcode, 
                strict=strict, 
                user_agent=user_agent, 
                async_=async_, 
                **request_kwargs, 
            )
        pickcode = self.to_pickcode(pickcode)
        def gen_step():
            if app in ("web2", "video"):
                resp = yield self.download_url_web2(
                    pickcode, 
                    user_agent=user_agent, 
                    async_=async_, 
                    **request_kwargs, 
                )
                resp["pickcode"] = pickcode
                resp["is_download"] = True
                check_response(resp)
                url = resp["url"]
                return P115URL(
                    url, 
                    id=self.to_id(pickcode), 
                    pickcode=pickcode, 
                    name=unquote(urlsplit(url).path.rsplit("/", 1)[-1]), 
                    is_dir=False, 
                    headers=resp["headers"], 
                )
            elif app in ("web", "desktop"):
                resp = yield self.download_url_web(
                    pickcode, 
                    user_agent=user_agent, 
                    async_=async_, 
                    **request_kwargs, 
                )
                resp["pickcode"] = pickcode
                resp["is_download"] = True
                try:
                    check_response(resp)
                except IsADirectoryError:
                    if strict:
                        raise
                return P115URL(
                    resp.get("file_url", ""), 
                    id=int(resp["file_id"]), 
                    pickcode=pickcode, 
                    name=resp["file_name"], 
                    size=int(resp["file_size"]), 
                    is_dir=not resp["state"], 
                    headers=resp["headers"], 
                )
            else:
                resp = yield self.download_url_app(
                    pickcode, 
                    user_agent=user_agent, 
                    app=app, 
                    async_=async_, 
                    **request_kwargs, 
                )
                resp["pickcode"] = pickcode
                resp["is_download"] = True
                data = resp.get("data")
                if not data:
                    resp["state"] = False
                    resp["errno"] = resp.get("errno") or 50015
                    resp.setdefault("message", "文件不存在、是目录或者不支持此操作")
                check_response(resp)
                if "url" in data:
                    url = data["url"]
                    return P115URL(
                        url, 
                        pickcode=pickcode, 
                        name=unquote(urlsplit(url).path.rsplit("/", 1)[-1]), 
                        is_dir=False, 
                        headers=resp["headers"], 
                    )
                for fid, info in data.items():
                    url = info["url"]
                    if strict and not url:
                        throw(
                            errno.EISDIR, 
                            f"{fid} is a directory, with response {resp}", 
                        )
                    return P115URL(
                        url["url"] if url else "", 
                        id=int(fid), 
                        pickcode=info["pick_code"], 
                        name=info["file_name"], 
                        size=int(info["file_size"]), 
                        sha1=info["sha1"], 
                        is_dir=not url, 
                        headers=resp["headers"], 
                    )
                throw(
                    errno.ENOENT, 
                    f"no such pickcode: {pickcode!r}, with response {resp}", 
                )
        return run_gen_step(gen_step, async_)

    @overload
    def download_urls(
        self, 
        pickcodes: int | str | Iterable[int | str], 
        /, 
        strict: bool = True, 
        user_agent: None | str = None, 
        app: str = "os_windows", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict[int, P115URL]:
        ...
    @overload
    def download_urls(
        self, 
        pickcodes: int | str | Iterable[int | str], 
        /, 
        strict: bool = True, 
        user_agent: None | str = None, 
        app: str = "os_windows", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict[int, P115URL]]:
        ...
    def download_urls(
        self, 
        pickcodes: int | str | Iterable[int | str], 
        /, 
        strict: bool = True, 
        user_agent: None | str = None, 
        app: str = "os_windows", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict[int, P115URL] | Coroutine[Any, Any, dict[int, P115URL]]:
        """批量获取文件的下载链接

        .. note::
            获取的直链中，部分查询参数的解释：

            - ``t``: 过期时间戳
            - ``u``: 用户 id
            - ``c``: 允许同时打开次数，如果为 0，则是无限次数
            - ``f``: 请求时要求携带请求头
                - 如果为空，则无要求
                - 如果为 1，则需要 user-agent（和请求直链时的一致）
                - 如果为 3，则需要 user-agent（和请求直链时的一致） 和 Cookie（由请求直链时的响应所返回的 Set-Cookie 响应头）

        :param pickcodes: 提取码，多个用逗号 "," 隔开
        :param strict: 如果为 True，当目标是目录时，会直接忽略
        :param user_agent: 如果不为 None，则作为请求头 "user-agent" 的值
        :param app: 使用此设备的接口，要么是 "open"，要么是 "chrome"（或者其他任何值）
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 一批下载链接
        """
        if app == "open":
            return self.download_urls_open(
                pickcodes, 
                strict=strict, 
                user_agent=user_agent, 
                async_=async_, 
                **request_kwargs, 
            )
        if isinstance(pickcodes, (int, str)):
            pickcodes = self.to_pickcode(pickcodes)
        else:
            pickcodes = ",".join(map(self.to_pickcode, pickcodes))
        def gen_step():
            resp = yield self.download_url_app(
                pickcodes, 
                user_agent=user_agent, 
                async_=async_, 
                **request_kwargs, 
            )
            resp["pickcode"] = pickcodes
            resp["is_download"] = True
            data = resp.get("data")
            if not data:
                resp["state"] = False
                resp["errno"] = resp.get("errno") or 50015
                resp.setdefault("message", "文件不存在、是目录或者不支持此操作")
            urls: dict[int, P115URL] = {}
            if resp.get("errno") != 50003:
                check_response(resp)
                for fid, info in data.items():
                    url = info["url"]
                    if strict and not url:
                        continue
                    fid = int(fid)
                    urls[fid] = P115URL(
                        url["url"] if url else "", 
                        id=fid, 
                        pickcode=info["pick_code"], 
                        name=info["file_name"], 
                        size=int(info["file_size"]), 
                        sha1=info["sha1"], 
                        is_dir=not url, 
                        headers=resp["headers"], 
                    )
            return urls
        return run_gen_step(gen_step, async_)

    @overload
    def download_url_app(
        self, 
        payload: str | dict, 
        /, 
        user_agent: None | str = None, 
        app: str = "chrome", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def download_url_app(
        self, 
        payload: str | dict, 
        /, 
        user_agent: None | str = None, 
        app: str = "chrome", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def download_url_app(
        self, 
        payload: str | dict, 
        /, 
        user_agent: None | str = None, 
        app: str = "chrome", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取文件的下载链接

        POST https://proapi.115.com/app/chrome/downurl

        .. note::
            ``app`` 为 "chrome" 时，支持一次获取多个提取码对应的下载链接，但是每多一个提取码，大概多耗时 50 ms，猜测服务端也是逐个从某个服务获取下载链接的。

            如果 ``app`` 为 "chrome"，则仅支持 ``aid=1`` 的提取码获取下载链接（以前是不限制 aid 的，这样甚至可以获取已经删除的文件的下载链接）；否则，还支持 ``aid=12`` 的下载链接。

        .. attention::
            尽量不要尝试对已经删除的文件获取下载链接，不仅会失败，还容易触发风控

        :payload:
            - pickcode: str  💡 如果 ``app`` 为 "chrome"，则可以接受多个，多个用逗号 "," 隔开
            - pick_code: str 💡 如果不用 ``pickcode``，那就用 ``pick_code``
            - share_id: int | str = <default> 💡 共享 id
            - user_id: int = <default>
        """
        if app in ("", "chrome"):
            api = complete_url("/app/chrome/downurl", base_url)
            if isinstance(payload, str):
                payload = {"pickcode": payload}
            elif "pickcode" not in payload:
                payload["pickcode"] = payload["pick_code"]
        elif app in ("os_windows", "os_mac", "os_linux", "windows", "mac", "linux"):
            if not app.startswith("os_"):
                app = "os_" + app
            api = complete_url("/2.0/ufile/downurl", base_url=base_url, app=app)
            if isinstance(payload, str):
                payload = {"pickcode": payload}
            elif "pickcode" not in payload:
                payload["pickcode"] = payload["pick_code"]
            # NOTE: 提取码不可是 "f" 开头，但允许由任何特征值构造（由 0-9 和 a-z 这 36 个字符，构成的 4 位字符串，例如 "0000"），但限定必须是自己网盘的文件（不能获取别人的文件）
            payload["pickcode"] = ",".join(map(normalize_pickcode, payload["pickcode"].split(",")))
            request_kwargs.setdefault("ecdh_encrypt", True)
        else:
            api = complete_url("/2.0/ufile/download", base_url=base_url, app=app)
            if isinstance(payload, str):
                payload = {"pick_code": payload}
            elif "pick_code" not in payload:
                payload["pick_code"] = payload["pickcode"]
        payload.setdefault("user_id", self.user_id)
        headers = request_kwargs["headers"] = dict(request_kwargs.get("headers") or ())
        if user_agent is None:
            headers.setdefault("user-agent", "")
        else:
            headers["user-agent"] = user_agent
        def parse(_, content: bytes, /) -> dict:
            json = json_maybe_decrypt_loads(content)
            if json["state"] and (data := json.get("data")):
                json["data"] = json_loads(rsa_decrypt(data))
            json["headers"] = headers
            return json
        request_kwargs.setdefault("parse", parse)
        request_kwargs["data"] = {"data": rsa_encrypt(dumps(payload)).decode("ascii")}
        return self.request(
            url=api, 
            method="POST", 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def download_url_web(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        user_agent: None | str = None, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def download_url_web(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        user_agent: None | str = None, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def download_url_web(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        user_agent: None | str = None, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取文件的下载链接（网页版接口）

        GET https://webapi.115.com/files/download

        .. note::
            最大允许下载 200 MB 的文件，即使文件违规，或者 `aid=12`，也可以正常下载

        :payload:
            - pickcode: str
            - dl: int = 0 💡 如果不为 0，则需要从响应中提取 "file_url_302" 字段，得到一个链接（只能被访问一次，但无需 cookies），然后访问此链接，才能从中获得最终的下载链接
        """
        api = complete_url("/files/download", base_url=base_url)
        if not isinstance(payload, dict):
            payload = {"pickcode": self.to_pickcode(payload)}
        headers = request_kwargs["headers"] = dict(request_kwargs.get("headers") or ())
        if user_agent is None:
            headers.setdefault("user-agent", "")
        else:
            headers["user-agent"] = user_agent
        def parse(resp, content: bytes, /) -> dict:
            json = json_maybe_decrypt_loads(content)
            if "Set-Cookie" in resp.headers:
                if isinstance(resp.headers, Mapping):
                    match = CRE_SET_COOKIE.search(resp.headers["Set-Cookie"])
                    if match is not None:
                        headers["cookie"] = match[0]
                else:
                    for k, v in reversed(resp.headers.items()):
                        if k == "Set-Cookie" and CRE_SET_COOKIE.match(v) is not None:
                            headers["cookie"] = v
                            break
            json["headers"] = headers
            return json
        request_kwargs.setdefault("parse", parse)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def download_url_web2(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://115.com", 
        user_agent: None | str = None, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def download_url_web2(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://115.com", 
        user_agent: None | str = None, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def download_url_web2(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://115.com", 
        user_agent: None | str = None, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取文件的下载链接（网页版接口）

        GET https://115.com/?ct=download&ac=video

        .. note::
            最大允许下载 200 MB 的文件，即使文件已被删除，也可以正常下载

        :payload:
            - pickcode: str
        """
        api = complete_url(base_url=base_url, query={"ct": "download", "ac": "video"})
        if not isinstance(payload, dict):
            payload = {"pickcode": self.to_pickcode(payload)}
        headers = request_kwargs["headers"] = dict(request_kwargs.get("headers") or ())
        if user_agent is None:
            headers.setdefault("user-agent", "")
        else:
            headers["user-agent"] = user_agent
        def parse(resp, _: bytes, /) -> dict:
            if resp.status != 302:
                return {
                    "state": False, 
                    "errno": 31003, 
                    "message": "文件不存在、已删除、超过200MB或者是目录", 
                    "response": {"status": resp.status, "headers": dict(resp.headers)}, 
                }
            json = {"state": True, "url": resp.headers["location"]}
            if "Set-Cookie" in resp.headers:
                if isinstance(resp.headers, Mapping):
                    match = CRE_SET_COOKIE.search(resp.headers["Set-Cookie"])
                    if match is not None:
                        headers["cookie"] = match[0]
                else:
                    for k, v in reversed(resp.headers.items()):
                        if k == "Set-Cookie" and CRE_SET_COOKIE.match(v) is not None:
                            headers["cookie"] = v
                            break
            json["headers"] = headers
            return json
        request_kwargs.setdefault("parse", parse)
        request_kwargs.setdefault("follow_redirects", False)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    ########## Extraction API ##########

    @overload
    def extract_add_file(
        self, 
        payload: list | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_add_file(
        self, 
        payload: list | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_add_file(
        self, 
        payload: list | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """解压缩到某个目录，推荐直接用封装函数 `extract_file`

        POST https://webapi.115.com/files/add_extract_file

        .. caution::
            【解压到】任务不可并发、不可中止，空目录不会被导出，不会产生 life 操作事件。
            目录层级最多 25 级（不算文件节点），对于超出此限制的路径，会直接把最终的文件保存到第 25 级之下，如果因此造成同名，会在扩展名前加 (1)，数字逐次增加，以此类推。
            但如果文件名是类似 ".name" 的格式，会被视为 ".name.name" 处理，也就是名字翻倍，然后处理成 ".name(1).name"、".name(2).name"、... 这样累计下去，此时整个名字最多 510 字节（按 utf-8 编码算，即 255 * 2），此时扩展名不会变，只会截断当它充当文件名时尾部的一部分。
            名字里面如果有 <>" 这 3 个字符，则会被替换为下划线 _。
            文件名最多 75 个字符（不包括扩展名部分），超出部分会被截断。扩展名部分，不算前缀点号 . 时，最多 254 个字节（按 utf-8 编码算），但如果所有字符都相同的中文字，却最多只有 75 个（为什么呢？）。

        :payload:
            - pick_code: str
            - extract_file: str = ""
            - extract_dir: str = ""
            - extract_file[]: str
            - extract_file[]: str
            - ...
            - to_pid: int | str = 0
            - paths: str = "文件"
        """
        api = complete_url("/files/add_extract_file", base_url=base_url)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def extract_add_file_app(
        self, 
        payload: list | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_add_file_app(
        self, 
        payload: list | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_add_file_app(
        self, 
        payload: list | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """解压缩到某个目录，推荐直接用封装函数 `extract_file`

        POST https://proapi.115.com/{app}/2.0/ufile/add_extract_file

        .. caution::
            【解压到】任务不可并发、不可中止，空目录不会被导出，不会产生 life 操作事件。
            目录层级最多 25 级（不算文件节点），对于超出此限制的路径，会直接把最终的文件保存到第 25 级之下，如果因此造成同名，会在扩展名前加 (1)，数字逐次增加，以此类推。
            但如果文件名是类似 ".name" 的格式，会被视为 ".name.name" 处理，也就是名字翻倍，然后处理成 ".name(1).name"、".name(2).name"、... 这样累计下去，此时整个名字最多 510 字节（按 utf-8 编码算，即 255 * 2），此时扩展名不会变，只会截断当它充当文件名时尾部的一部分。
            名字里面如果有 <>" 这 3 个字符，则会被替换为下划线 _。
            文件名最多 75 个字符（不包括扩展名部分），超出部分会被截断。扩展名部分，不算前缀点号 . 时，最多 254 个字节（按 utf-8 编码算），但如果所有字符都相同的中文字，却最多只有 75 个（为什么呢？）。

        :payload:
            - pick_code: str
            - extract_file: str = ""
            - extract_dir: str = ""
            - extract_file[]: str
            - extract_file[]: str
            - ...
            - to_pid: int | str = 0
            - paths: str = "文件"
        """
        api = complete_url("/2.0/ufile/add_extract_file", base_url=base_url, app=app)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def extract_download_url(
        self, 
        /, 
        pickcode: str, 
        path: str, 
        user_agent: None | str = None, 
        app: str = "android", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> P115URL:
        ...
    @overload
    def extract_download_url(
        self, 
        /, 
        pickcode: str, 
        path: str, 
        user_agent: None | str = None, 
        app: str = "android", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, P115URL]:
        ...
    def extract_download_url(
        self, 
        /, 
        pickcode: str, 
        path: str, 
        user_agent: None | str = None, 
        app: str = "android", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> P115URL | Coroutine[Any, Any, P115URL]:
        """获取压缩包中文件的下载链接

        :param pickcode: 压缩包的提取码
        :param path: 文件在压缩包中的路径
        :param user_agent: 如果不为 None，则作为请求头 "user-agent" 的值
        :param async_: 是否异步
        :param request_kwargs: 其余请求参数

        :return: 下载链接
        """
        path = path.rstrip("/")
        def gen_step():
            payload = {"pick_code": pickcode, "full_name": path.lstrip("/")}
            if app in ("web", "desktop"):
                resp = yield self.extract_download_url_web(
                    payload, 
                    user_agent=user_agent, 
                    async_=async_, 
                    **request_kwargs, 
                )
            else:
                resp = yield self.extract_download_url_app(
                    payload, 
                    user_agent=user_agent, 
                    app=app, 
                    async_=async_, 
                    **request_kwargs, 
                )
            resp["payload"] = payload
            resp["is_download"] = True
            check_response(resp)
            data = resp.get("data")
            if not data:
                resp["state"] = False
                resp["errno"] = resp.get("errno") or 50015
                resp.setdefault("message", "文件不存在、是目录或者不支持此操作")
            url = quote(data["url"], safe=":/?&=%#")
            from posixpath import basename
            return P115URL(
                url, 
                name=basename(path), 
                path=path, 
                headers=resp["headers"], 
            )
        return run_gen_step(gen_step, async_)

    @overload
    def extract_download_url_app(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        user_agent: None | str = None, 
        app: str = "android", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_download_url_app(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        user_agent: None | str = None, 
        app: str = "android", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_download_url_app(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        user_agent: None | str = None, 
        app: str = "android", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取压缩包中文件的下载链接

        GET https://proapi.115.com/{app}/2.0/ufile/extract_down_file

        :payload:
            - pick_code: str
            - full_name: str
            - dl: int = <default>
        """
        api = complete_url("/2.0/ufile/extract_down_file", base_url=base_url, app=app)
        headers = request_kwargs["headers"] = dict(request_kwargs.get("headers") or ())
        if user_agent is None:
            headers.setdefault("user-agent", "")
        else:
            headers["user-agent"] = user_agent
        def parse(_, content: bytes, /) -> dict:
            json = json_maybe_decrypt_loads(content)
            json["headers"] = headers
            return json
        request_kwargs.setdefault("parse", parse)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def extract_download_url_web(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        user_agent: None | str = None, 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_download_url_web(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        user_agent: None | str = None, 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_download_url_web(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        user_agent: None | str = None, 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取压缩包中文件的下载链接

        GET https://webapi.115.com/files/extract_down_file

        :payload:
            - pick_code: str
            - full_name: str
        """
        api = complete_url("/files/extract_down_file", base_url=base_url)
        headers = request_kwargs["headers"] = dict(request_kwargs.get("headers") or ())
        if user_agent is None:
            headers.setdefault("user-agent", "")
        else:
            headers["user-agent"] = user_agent
        def parse(resp, content: bytes, /) -> dict:
            json = json_maybe_decrypt_loads(content)
            if "Set-Cookie" in resp.headers:
                if isinstance(resp.headers, Mapping):
                    match = CRE_SET_COOKIE.search(resp.headers["Set-Cookie"])
                    if match is not None:
                        headers["cookie"] = match[0]
                else:
                    for k, v in reversed(resp.headers.items()):
                        if k == "Set-Cookie" and CRE_SET_COOKIE.match(v) is not None:
                            headers["cookie"] = v
                            break
            json["headers"] = headers
            return json
        request_kwargs.setdefault("parse", parse)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def extract_file(
        self, 
        /, 
        pickcode: str, 
        files: str | Iterable[str] = "", 
        dirs: str | Iterable[str] = "", 
        dirname: str = "", 
        to_pid: int | str = 0, 
        app: str = "web", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_file(
        self, 
        /, 
        pickcode: str, 
        files: str | Iterable[str] = "", 
        dirs: str | Iterable[str] = "", 
        dirname: str = "", 
        to_pid: int | str = 0, 
        app: str = "web", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_file(
        self, 
        /, 
        pickcode: str, 
        files: str | Iterable[str] = "", 
        dirs: str | Iterable[str] = "", 
        dirname: str = "", 
        to_pid: int | str = 0, 
        app: str = "web", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """解压缩到某个目录，此方法是对 ``extract_add_file`` 的封装，推荐使用

        .. caution::
            【解压到】任务不可并发、不可中止，空目录不会被导出，不会产生 life 操作事件。
            目录层级最多 25 级（不算文件节点），对于超出此限制的路径，会直接把最终的文件保存到第 25 级之下，如果因此造成同名，会在扩展名前加 (1)，数字逐次增加，以此类推。
            但如果文件名是类似 ".name" 的格式，会被视为 ".name.name" 处理，也就是名字翻倍，然后处理成 ".name(1).name"、".name(2).name"、... 这样累计下去，此时整个名字最多 510 字节（按 utf-8 编码算，即 255 * 2），此时扩展名不会变，只会截断当它充当文件名时尾部的一部分。
            名字里面如果有 <>" 这 3 个字符，则会被替换为下划线 _。
            文件名最多 75 个字符（不包括扩展名部分），超出部分会被截断。扩展名部分，不算前缀点号 . 时，最多 254 个字节（按 utf-8 编码算），但如果所有字符都相同的中文字，却最多只有 75 个（为什么呢？）。

        .. note::
            一次解压任务，似乎最多 1 万个文件，而且会排除空目录。但你完全可以利用这一点，来批量创建一个目录结构。
            虽然分次也能解压极大的压缩包，但相应的云解压的耗时也会同步大量增加，处理起来也更为复杂，我建议每次最好限制在 1 万条叶子节点以下。

            1. 首先创建一个压缩包 zip 文件，把目录结构写进去，且为每个目录创建一个空文件，以避免被视为空目录（为了提高效率，请先把所有非叶子节点过滤掉）

                .. code:: python

                    from io import BytesIO
                    from zipfile import ZipFile

                    f = BytesIO()
                    with ZipFile(f, "w") as z:
                        for dir in dirs:
                            z.writestr(dir + "/.placeholder", "")

            2. 把所创建的 zip 压缩包上传到网盘

                .. code:: python

                    from p115client import check_response, P115Client

                    client = P115Client.from_path()
                
                    resp = client.upload_file_sample(f.getbuffer(), filename='a.zip')
                    check_response(resp)
                    pickcode = resp["data"]["pick_code"]

            3. 将压缩包云解压

                .. code:: python

                    client.extract_push(pickcode)
                    # 查看进度用：client.extract_push_progress(pickcode)

            4. 等待云解压完成后，把压缩包解压到网盘（耗时较长，且不可并发）

                .. code:: python

                    client.extract_file(pickcode)

            5. 解压完成后，把里面所有的 .placeholder 占位文件找出来删了，这里假设顶层目录 id 是 ``pid``

                .. code:: python

                    from p115client.tool import batch_delete, iter_download_files

                    empty_file_hash = "DA39A3EE5E6B4B0D3255BFEF95601890AFD80709"
                    files = (
                        info["pc"]
                        for info in iter_download_nodes(client, pid, app="android", get_raw=True) 
                        if info["sha1"] == empty_file_hash
                    )
                    batch_delete(client, files)

        :param pickcode: 压缩文件的提取码
        :param files:    待解压缩的文件路径（相对于 ``dirname``），如果以 "/" 结尾，则视为目录
        :param dirs:     待解压缩的文件路径（相对于 ``dirname``）
        :param dirname:  压缩包内路径，为空则是压缩包的根目录
        :param to_pid:   解压到网盘的目录 id
        :param app:      使用此设备的接口
        :param async_:   是否异步
        :param request_kwargs: 其它请求参数

        :return: 接口响应，会返回一个 "extract_id"，需要你去轮询获取进度
        """
        if app in ("", "web", "desktop", "aps"):
            extract_add_file: Callable = self.extract_add_file
        else:
            extract_add_file = self.extract_add_file_app
            request_kwargs["app"] = app
        dirname = dirname.strip("/")
        def gen_step():
            data = [
                ("pick_code", pickcode), 
                ("paths", "文件/" + dirname if dirname else "文件"), 
                ("to_pid", to_pid), 
            ]
            paths: list[str] = []
            add_path = paths.append
            if files:
                if isinstance(files, str):
                    if files.strip("/"):
                        add_path(files)
                else:
                    for p in files:
                        if p.strip("/"):
                            add_path(p)
            if dirs:
                if isinstance(dirs, str):
                    if dirs.strip("/"):
                        add_path(dirs + "/")
                else:
                    for p in dirs:
                        if p.strip("/"):
                            add_path(p + "/")
            if not paths:
                next_marker = ""
                extract_list = self.extract_list
                while True:
                    resp = yield extract_list(
                        pickcode=pickcode, 
                        path=dirname, 
                        next_marker=next_marker, 
                        async_=async_, 
                        **request_kwargs, 
                    )
                    check_response(resp)
                    for p in resp["data"]["list"]:
                        if p["file_category"]:
                            add_path(p["file_name"])
                        else:
                            add_path(p["file_name"] + "/")
                    if not (next_marker := resp["data"].get("next_marker")):
                        break
            data.extend(
                ("extract_dir[]" if path.endswith("/") else "extract_file[]", path.strip("/")) 
                for path in paths
            )
            return extract_add_file(data, async_=async_, **request_kwargs)
        return run_gen_step(gen_step, async_)

    @overload
    def extract_folders(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_folders(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_folders(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取压缩文件的文件列表（简略信息）

        GET https://webapi.115.com/files/extract_folders

        :payload:
            - pick_code: str 💡 压缩包文件的提取码
            - full_dir_name: str 💡 多个用逗号 "," 隔开
            - full_file_name: str = <default> 💡 多个用逗号 "," 隔开
        """
        api = complete_url("/files/extract_folders", base_url=base_url)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def extract_folders_app(
        self, 
        payload: dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_folders_app(
        self, 
        payload: dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_folders_app(
        self, 
        payload: dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取压缩文件的文件列表（简略信息）

        GET https://proapi.115.com/{app}/2.0/ufile/extract_folders

        :payload:
            - pick_code: str 💡 压缩包文件的提取码
            - full_dir_name: str 💡 多个用逗号 "," 隔开
            - full_file_name: str = <default> 💡 多个用逗号 "," 隔开
        """
        api = complete_url("/2.0/ufile/extract_folders", base_url=base_url, app=app)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def extract_folders_post(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_folders_post(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_folders_post(
        self, 
        payload: dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取压缩文件的文件列表是否可批量下载（最高支持1万的文件操作数量）

        POST https://webapi.115.com/files/extract_folders

        :payload:
            - pick_code: str 💡 压缩包文件的提取码
            - full_dir_name: str 💡 多个用逗号 "," 隔开
            - full_file_name: str = <default> 💡 多个用逗号 "," 隔开
        """
        api = complete_url("/files/extract_folders", base_url=base_url)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def extract_folders_post_app(
        self, 
        payload: dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_folders_post_app(
        self, 
        payload: dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_folders_post_app(
        self, 
        payload: dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取压缩文件的文件列表是否可批量下载（最高支持1万的文件操作数量）

        POST https://proapi.115.com/{app}/2.0/ufile/extract_folders

        :payload:
            - pick_code: str 💡 压缩包文件的提取码
            - full_dir_name: str 💡 多个用逗号 "," 隔开
            - full_file_name: str = <default> 💡 多个用逗号 "," 隔开
        """
        api = complete_url("/2.0/ufile/extract_folders", base_url=base_url, app=app)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def extract_info(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_info(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_info(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取压缩文件的文件列表，推荐直接用封装函数 `extract_list`

        GET https://webapi.115.com/files/extract_info

        :payload:
            - pick_code: str
            - file_name: str = "" 💡 在压缩包中的相对路径
            - next_marker: str = ""
            - page_count: int | str = 999 💡 分页大小，介于 1-999
            - paths: str = "文件" 💡 省略即可
        """
        api = complete_url("/files/extract_info", base_url=base_url)
        if isinstance(payload, str):
            payload = {"pick_code": payload}
        payload = {"paths": "文件", "page_count": 999, "next_marker": "", "file_name": "", **payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def extract_info_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_info_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_info_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取压缩文件的文件列表，推荐直接用封装函数 `extract_list_app`

        GET https://proapi.115.com/{app}/2.0/ufile/extract_info

        :payload:
            - pick_code: str
            - file_name: str = "" 💡 在压缩包中的相对路径
            - next_marker: str = ""
            - page_count: int | str = 999 💡 分页大小，介于 1-999
            - paths: str = "文件" 💡 省略即可
        """
        api = complete_url("/2.0/ufile/extract_info", base_url=base_url, app=app)
        if isinstance(payload, str):
            payload = {"pick_code": payload}
        payload = {"paths": "文件", "page_count": 999, "next_marker": "", "file_name": "", **payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def extract_list(
        self, 
        /, 
        pickcode: str, 
        path: str = "", 
        next_marker: str = "", 
        page_count: int = 999, 
        app: str = "web", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_list(
        self, 
        /, 
        pickcode: str, 
        path: str = "", 
        next_marker: str = "", 
        page_count: int = 999, 
        app: str = "web", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_list(
        self, 
        /, 
        pickcode: str, 
        path: str = "", 
        next_marker: str = "", 
        page_count: int = 999, 
        app: str = "web", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取压缩文件的文件列表，此方法是对 ``extract_info`` 的封装，推荐使用

        :param pickcode: 压缩文件的提取码
        :param path: 压缩包内（目录）路径，为空则是压缩包的根目录
        :param next_marker: 翻页标记，用来获取下一页
        :param page_count: 这一页有多少条数据，范围在 ``[1, 999]``
        :param app: 使用此设备的接口
        :param async_: 是否异步
        :param request_kwargs: 其它请求参数

        :return: 接口响应
        """
        if not 1 <= page_count <= 999:
            page_count = 999
        payload = {
            "pick_code": pickcode, 
            "file_name": path.strip("/"), 
            "paths": "文件", 
            "next_marker": next_marker, 
            "page_count": page_count, 
        }
        if app in ("", "web", "desktop", "aps"):
            extract_info: Callable = self.extract_info
        else:
            extract_info = self.extract_info_app
        return extract_info(
            payload, 
            app=app, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def extract_progress(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_progress(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_progress(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取 解压缩到目录 任务的进度

        GET https://webapi.115.com/files/add_extract_file

        :payload:
            - extract_id: str
        """
        api = complete_url("/files/add_extract_file", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"extract_id": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def extract_progress_app(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_progress_app(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_progress_app(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取 解压缩到目录 任务的进度

        GET https://proapi.115.com/{app}/2.0/ufile/add_extract_file

        :payload:
            - extract_id: str
        """
        api = complete_url("/2.0/ufile/add_extract_file", base_url=base_url, app=app)
        if isinstance(payload, (int, str)):
            payload = {"extract_id": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def extract_push(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_push(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_push(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """推送一个解压缩任务给服务器，完成后，就可以查看压缩包的文件列表了

        .. warning::
            只能云解压 20GB 以内文件，不支持云解压分卷压缩包，只支持 .zip、.rar 和 .7z 等

        POST https://webapi.115.com/files/push_extract

        :payload:
            - pick_code: str
            - secret: str = "" 💡 解压密码
        """
        api = complete_url("/files/push_extract", base_url=base_url)
        if isinstance(payload, str):
            payload = {"pick_code": payload}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def extract_push_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_push_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_push_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """推送一个解压缩任务给服务器，完成后，就可以查看压缩包的文件列表了

        .. warning::
            只能云解压 20GB 以内文件，不支持云解压分卷压缩包，只支持 .zip、.rar 和 .7z 等

        POST https://proapi.115.com/{app}/2.0/ufile/push_extract

        :payload:
            - pick_code: str
            - secret: str = "" 💡 解压密码
        """
        api = complete_url("/2.0/ufile/push_extract", base_url=base_url, app=app)
        if isinstance(payload, str):
            payload = {"pick_code": payload}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def extract_push_progress(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_push_progress(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_push_progress(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """查询解压缩任务的进度

        GET https://webapi.115.com/files/push_extract

        :payload:
            - pick_code: str
        """
        api = complete_url("/files/push_extract", base_url=base_url)
        if isinstance(payload, str):
            payload = {"pick_code": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def extract_push_progress_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def extract_push_progress_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def extract_push_progress_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """查询解压缩任务的进度

        GET https://proapi.115.com/{app}/2.0/ufile/push_extract

        :payload:
            - pick_code: str
        """
        api = complete_url("/2.0/ufile/push_extract", base_url=base_url, app=app)
        if isinstance(payload, str):
            payload = {"pick_code": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    ########## File System API ##########

    @overload
    def fs_batch_edit(
        self, 
        payload: list | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_batch_edit(
        self, 
        payload: list | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_batch_edit(
        self, 
        payload: list | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """批量设置文件或目录（显示时长等）

        POST https://webapi.115.com/files/batch_edit

        :payload:
            - show_play_long[{fid}]: 0 | 1 = 1 💡 设置或取消显示时长
        """
        api = complete_url("/files/batch_edit", base_url=base_url)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_batch_edit_app(
        self, 
        payload: list | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_batch_edit_app(
        self, 
        payload: list | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_batch_edit_app(
        self, 
        payload: list | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """批量设置文件或目录（显示时长等）

        POST https://proapi.115.com/{app}/files/batch_edit

        :payload:
            - show_play_long[{fid}]: 0 | 1 = 1 💡 设置或取消显示时长
        """
        api = complete_url("/files/batch_edit", base_url=base_url, app=app)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_category_get(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_category_get(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_category_get(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """显示属性，可获取文件或目录的统计信息（提示：但得不到根目录的统计信息，所以 cid 为 0 时无意义）

        GET https://webapi.115.com/category/get

        .. caution::
            尝试获取目录的信息时，会去计算目录中文件和目录的数量、总文件大小 等信息，可能会消耗大量时间，但短时间内再次查询同一目录，耗时可能会大大减小

        :payload:
            - cid: int | str
            - fid: int | str 💡 ``cid`` 和 ``fid`` 至少需要提供一个
            - aid: int = 1 💡 area_id

                - 0: 会被视为 1
                - 1: 正常文件
                - 2: <unknown>
                - 3: <unknown>
                - 4: <unknown>
                - 5: <unknown>
                - 7: 回收站文件
                - 9: <unknown>
                - 12: 瞬间文件
                - 15: <unknown>
                - 120: 彻底删除文件、简历附件
                - <其它>: 会被视为 0

            - status: 0 | 1 = <default> 💡 如果为 1，那么文件已被删除，会返回错误
        """
        api = complete_url("/category/get", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"cid": payload}
        payload.setdefault("aid", 1)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_category_get_app(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_category_get_app(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_category_get_app(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """显示属性，可获取文件或目录的统计信息（提示：但得不到根目录的统计信息，所以 cid 为 0 时无意义）

        GET https://proapi.115.com/{app}/2.0/category/get

        .. caution::
            尝试获取目录的信息时，会去计算目录中文件和目录的数量、总文件大小 等信息，可能会消耗大量时间，但短时间内再次查询同一目录，耗时可能会大大减小

        :payload:
            - cid: int | str
            - aid: int = 1 💡 area_id

                - 0: 会被视为 1
                - 1: 正常文件
                - 2: <unknown>
                - 3: <unknown>
                - 4: <unknown>
                - 5: <unknown>
                - 7: 回收站文件
                - 9: <unknown>
                - 12: 瞬间文件
                - 15: <unknown>
                - 120: 彻底删除文件、简历附件
                - <其它>: 会被视为 0
        """
        api = complete_url("/2.0/category/get", base_url=base_url, app=app)
        if isinstance(payload, (int, str)):
            payload = {"cid": payload}
        payload.setdefault("aid", 1)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    fs_info = fs_category_get # type: ignore
    fs_info_app = fs_category_get_app

    @overload
    def fs_category_shortcut(
        self, 
        payload: int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_category_shortcut(
        self, 
        payload: int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_category_shortcut(
        self, 
        payload: int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """快捷入口列表（罗列所有的快捷入口）

        GET https://webapi.115.com/category/shortcut

        :payload:
            - offset: int = 0
            - limit: int = 1150
        """
        api = complete_url("/category/shortcut", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"offset": payload}
        payload = {"limit": 1150, "offset": 0, **payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_category_shortcut_set(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        set: bool = True, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_category_shortcut_set(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        set: bool = True, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_category_shortcut_set(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        set: bool = True, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """把一个目录设置或取消为快捷入口（快捷入口需要是目录）

        POST https://webapi.115.com/category/shortcut

        :payload:
            - file_id: int | str 目录 id，多个用逗号 "," 隔开
            - op: "add" | "delete" | "top" = "add" 操作代码

                - "add":    添加
                - "delete": 删除
                - "top":    置顶
        """
        api = complete_url("/category/shortcut", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"file_id": payload, "op": ("delete", "add")[set]}
        elif not isinstance(payload, dict):
            payload = {"file_id": ",".join(map(str, payload)), "op": ("delete", "add")[set]}
        else:
            payload = {"op": ("delete", "add")[set], **payload}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_copy(
        self, 
        payload: int | str | dict | Iterable[int | str], 
        /, 
        pid: int | str = 0, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_copy(
        self, 
        payload: int | str | dict | Iterable[int | str], 
        /, 
        pid: int | str = 0, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_copy(
        self, 
        payload: int | str | dict | Iterable[int | str], 
        /, 
        pid: int | str = 0, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """复制文件或目录

        POST https://webapi.115.com/files/copy

        .. caution::
            ⚠️ 请不要并发执行，限制在 5 万个文件和目录以内

        :payload:
            - fid: int | str 💡 文件或目录 id，只接受单个 id
            - fid[]: int | str
            - ...
            - fid[0]: int | str
            - fid[1]: int | str
            - ...
            - pid: int | str = 0 💡 目标目录 id
        """
        api = complete_url("/files/copy", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"fid": payload}
        elif not isinstance(payload, dict):
            payload = {f"fid[{i}]": fid for i, fid in enumerate(payload)}
        payload.setdefault("pid", pid)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_copy_app(
        self, 
        payload: int | str | dict | Iterable[int | str], 
        /, 
        pid: int | str = 0, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_copy_app(
        self, 
        payload: int | str | dict | Iterable[int | str], 
        /, 
        pid: int | str = 0, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_copy_app(
        self, 
        payload: int | str | dict | Iterable[int | str], 
        /, 
        pid: int | str = 0, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """复制文件或目录

        POST https://proapi.115.com/{app}/files/copy

        .. caution::
            ⚠️ 请不要并发执行，限制在 5 万个文件和目录以内

        :payload:
            - fid: int | str 💡 文件或目录的 id，多个用逗号 "," 隔开
            - pid: int | str = 0 💡 目标目录 id
        """
        api = complete_url("/files/copy", base_url=base_url, app=app)
        if isinstance(payload, (int, str)):
            payload = {"fid": payload}
        elif not isinstance(payload, dict):
            payload = {"fid": ",".join(map(str, payload))}
        cast(dict, payload).setdefault("pid", pid)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_cover_set(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        fid_cover: int | str = 0, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_cover_set(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        fid_cover: int | str = 0, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_cover_set(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        fid_cover: int | str = 0, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """修改封面，可以设置目录的封面，此接口是对 ``fs_edit`` 的封装
        """
        return self._fs_edit_set(
            payload, 
            "fid_cover", 
            default=fid_cover, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def fs_cover_set_app(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        fid_cover: int | str, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_cover_set_app(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        fid_cover: int | str, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_cover_set_app(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        fid_cover: int | str = 0, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """修改封面，可以设置目录的封面，此接口是对 ``fs_files_update_app`` 的封装
        """
        return self._fs_edit_set_app(
            payload, 
            "fid_cover", 
            default=fid_cover, 
            app=app, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def fs_delete(
        self, 
        payload: int | str | dict | Iterable[int | str], 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_delete(
        self, 
        payload: int | str | dict | Iterable[int | str], 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_delete(
        self, 
        payload: int | str | dict | Iterable[int | str], 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """删除文件或目录

        POST https://webapi.115.com/rb/delete

        .. caution::
            ⚠️ 请不要并发执行，但不限制文件数

        .. caution::
            删除和（从回收站）还原是互斥的，同时最多只允许执行一个操作

        :payload:
            - fid: int | str 💡 文件或目录的 id，多个用逗号 "," 隔开
            - fid[]: int | str
            - ...
            - fid[0]: int | str
            - fid[1]: int | str
            - ...
            - ignore_warn: 0 | 1 = <default>
            - from: int = <default>
            - pid: int = <default>
        """
        api = complete_url("/rb/delete", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"fid": payload}
        elif not isinstance(payload, dict):
            payload = {f"fid[{i}]": fid for i, fid in enumerate(payload)}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_delete_app(
        self, 
        payload: int | str | dict | Iterable[int | str], 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_delete_app(
        self, 
        payload: int | str | dict | Iterable[int | str], 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_delete_app(
        self, 
        payload: int | str | dict | Iterable[int | str], 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """删除文件或目录

        POST https://proapi.115.com/{app}/rb/delete

        .. caution::
            ⚠️ 请不要并发执行，限制在 5 万个文件和目录以内

        .. caution::
            删除和（从回收站）还原是互斥的，同时最多只允许执行一个操作

        .. caution::
            有超过 5 万个文件和文件夹时，不能直接执行删除。如果删除的只是文件，那么在接口响应时，涉及的文件，已经删除完毕；但如果是目录，那么接口响应时，后台可能还在执行，而删除是不可并发的，因此下一个删除任务执行失败时，只需要反复重试即可

        .. note::
            此接口还能删除 ``aid=12`` 下的文件，且不会经过回收站（``aid=7``），而是彻底删除（``aid=120``）

            .. code:: python

                from itertools import batched
                from p115client import P115Client
                client = P115Client.from_path()

                while True:
                    fids = [info["fid"] for info in client.fs_files({"aid": 12, "limit": 1150, "show_dir": 0})["data"]]
                    if not fids:
                        break
                    client.fs_delete_app(fids)

        :payload:
            - file_ids: int | str 💡 文件或目录的 id，多个用逗号 "," 隔开
            - user_id: int | str = <default> 💡 用户 id
        """
        api = complete_url("/rb/delete", base_url=base_url, app=app)
        if isinstance(payload, (int, str)):
            payload = {"file_ids": payload, "user_id": self.user_id}
        elif isinstance(payload, dict):
            payload = dict(payload, user_id=self.user_id)
        else:
            payload = {"file_ids": ",".join(map(str, payload)), "user_id": self.user_id}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_desc(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_desc(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_desc(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取文件或目录的备注

        GET https://webapi.115.com/files/desc

        :payload:
            - file_id: int | str
            - field: str = <default> 💡 可取示例值："pass"
            - compat: 0 | 1 = 1
            - new_html: 0 | 1 = <default>
        """
        api = complete_url("/files/desc", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"file_id": payload}
        payload = {"compat": 1, **payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_desc_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_desc_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_desc_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取文件或目录的备注

        GET https://proapi.115.com/{app}/files/desc

        :payload:
            - file_id: int | str
            - field: str = <default> 💡 可取示例值："pass"
            - compat: 0 | 1 = 1
            - new_html: 0 | 1 = <default>
        """
        api = complete_url("/files/desc", base_url=base_url, app=app)
        if isinstance(payload, (int, str)):
            payload = {"file_id": payload}
        payload = {"compat": 1, **payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_desc_set(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        desc: str = "", 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_desc_set(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        desc: str = "", 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_desc_set(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        desc: str = "", 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """为文件或目录设置备注，最多允许 65535 个字节 (64 KB 以内)，此接口是对 ``fs_edit`` 的封装

        .. hint::
            修改文件备注会更新文件的更新时间，即使什么也没改或者改为空字符串
        """
        return self._fs_edit_set(
            payload, 
            "file_desc", 
            default=desc, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def fs_desc_set_app(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        desc: str = "", 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_desc_set_app(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        desc: str = "", 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_desc_set_app(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        desc: str = "", 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """为文件或目录设置备注，最多允许 65535 个字节 (64 KB 以内)，此接口是对 ``fs_files_update_app`` 的封装

        .. hint::
            修改文件备注会更新文件的更新时间，即使什么也没改或者改为空字符串
        """
        return self._fs_edit_set_app(
            payload, 
            "file_desc", 
            desc, 
            app=app, 
            base_url=base_url, 
            async_=async_, 
            **request_kwargs, 
        )

    @overload
    def fs_dir_getid(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_dir_getid(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_dir_getid(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """由路径获取对应的 id（但只能获取目录，不能获取文件）

        GET https://webapi.115.com/files/getid

        :payload:
            - path: str
        """
        if isinstance(payload, str):
            payload = {"path": payload}
        if callable(base_url):
            base_url = base_url()
        if "://f.115.com" in base_url or "://n.115.com" in base_url:
            api = complete_url("/files/getid", base_url=base_url, query=payload)
            return self.request(url=api, async_=async_, **request_kwargs)
        api = complete_url("/files/getid", base_url=base_url)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_dir_getid2(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_dir_getid2(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_dir_getid2(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """由路径获取对应的 id（但只能获取目录，不能获取文件）

        GET https://webapi.115.com/files/get_path_id

        :payload:
            - path: str
            - parent_id: int = 0
            - is_create: 0 | 1 = 0 💡 当目录不存在时，是否创建
        """
        if isinstance(payload, str):
            payload = {"path": payload}
        if callable(base_url):
            base_url = base_url()
        if "://f.115.com" in base_url or "://n.115.com" in base_url:
            api = complete_url("/files/get_path_id", base_url=base_url, query=payload)
            return self.request(url=api, async_=async_, **request_kwargs)
        api = complete_url("/files/get_path_id", base_url=base_url)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_dir_getid_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_dir_getid_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_dir_getid_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """由路径获取对应的 id（但只能获取目录，不能获取文件）

        GET https://proapi.115.com/{app}/files/getid

        :payload:
            - path: str
        """
        api = complete_url("/files/getid", base_url=base_url, app=app)
        if isinstance(payload, str):
            payload = {"path": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_document(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_document(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_document(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取文档的信息和下载链接

        GET https://webapi.115.com/files/document

        .. note::
            即使文件格式不正确或者是一个目录，也可返回一些信息（包括 parent_id）

        :payload:
            - pickcode: str
        """
        api = complete_url("/files/document", base_url=base_url)
        if isinstance(payload, str):
            payload = {"pickcode": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_document_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_document_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_document_app(
        self, 
        payload: str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取文档的信息和下载链接

        GET https://proapi.115.com/{app}/files/document

        .. note::
            即使文件格式不正确或者是一个目录，也可返回一些信息（包括 parent_id）

        :payload:
            - pickcode: str
        """
        api = complete_url("/files/document", base_url=base_url, app=app)
        if isinstance(payload, str):
            payload = {"pickcode": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_edit(
        self, 
        payload: list | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_edit(
        self, 
        payload: list | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_edit(
        self, 
        payload: list | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """设置文件或目录（备注、标签、封面等）

        POST https://webapi.115.com/files/edit

        .. caution::
            web 接口有缺陷，经常会出现遗漏，所以尽量用 app 版接口 ``fs_files_update_app``

        :payload:
            - fid: int | str
            - fid[]: int | str
            - ...
            - file_desc: str = <default> 💡 可以用 html
            - file_label: int | str = <default> 💡 标签 id，多个用逗号 "," 隔开
            - fid_cover: int | str = <default>  💡 封面图片的文件 id，多个用逗号 "," 隔开，如果要删除，值设为 0 即可
            - show_play_long: 0 | 1 = <default> 💡 文件名称显示时长
            - ...
        """
        api = complete_url("/files/edit", base_url=base_url)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def _fs_edit_set(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        attr: str, 
        default: Any = "", 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def _fs_edit_set(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        attr: str, 
        default: Any = "", 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def _fs_edit_set(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        attr: str, 
        default: Any = "", 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """批量设置文件或目录（备注、标签、封面等），此接口是对 ``fs_edit`` 的封装
        """
        if isinstance(payload, (int, str)):
            payload = [("fid", payload), (attr, default)]
        elif isinstance(payload, list):
            if not any(a[0] == attr for a in payload):
                payload.append((attr, default))
        elif isinstance(payload, dict):
            payload.setdefault(attr, default)
        else:
            payload = [(f"fid[{i}]", fid) for i, fid in enumerate(payload, 1)]
            payload.append((attr, default))
        return self.fs_edit(payload, base_url=base_url, async_=async_, **request_kwargs)

    @overload
    def _fs_edit_set_app(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        attr: str, 
        default: Any = "", 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def _fs_edit_set_app(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        attr: str, 
        default: Any = "", 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def _fs_edit_set_app(
        self, 
        payload: int | str | Iterable[int | str] | list[tuple] | dict, 
        /, 
        attr: str, 
        default: Any = "", 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """批量设置文件或目录（备注、标签、封面等），此接口是对 ``fs_files_update_app`` 的封装
        """
        if isinstance(payload, (int, str)):
            payload = [("file_id", payload), (attr, default)]
        elif isinstance(payload, list):
            if not any(a[0] == attr for a in payload):
                payload.append((attr, default))
        elif isinstance(payload, dict):
            payload.setdefault(attr, default)
        else:
            payload = [(f"file_id[{i}]", fid) for i, fid in enumerate(payload, 1)]
            payload.append((attr, default))
        return self.fs_files_update_app(
            payload, 
            async_=async_, 
            app=app, 
            base_url=base_url, 
            **request_kwargs, 
        )

    @overload
    def fs_export_dir(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_export_dir(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_export_dir(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """导出目录树

        POST https://webapi.115.com/files/export_dir

        .. caution::
            【导出目录树】任务不可并发、不可中止，空目录不会被导出，输出的文件不会产生 life 操作事件

        :payload:
            - file_ids: int | str   💡 多个用逗号 "," 隔开
            - target: str = "U_1_0" 💡 导出目录树到这个目录
            - layer_limit: int = <default> 💡 层级深度，自然数
            - not_suffix: str = <default>
        """
        api = complete_url("/files/export_dir", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"file_ids": payload}
        payload.setdefault("target", "U_1_0")
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_export_dir_app(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_export_dir_app(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_export_dir_app(
        self, 
        payload: int | str | dict, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """导出目录树

        POST https://proapi.115.com/{app}/2.0/ufile/export_dir

        .. caution::
            【导出目录树】任务不可并发、不可中止，空目录不会被导出，输出的文件不会产生 life 操作事件

        :payload:
            - file_ids: int | str   💡 多个用逗号 "," 隔开
            - target: str = "U_1_0" 💡 导出目录树到这个目录
            - layer_limit: int = <default> 💡 层级深度，自然数
        """
        api = complete_url("/2.0/ufile/export_dir", base_url=base_url, app=app)
        if isinstance(payload, (int, str)):
            payload = {"file_ids": payload}
        payload.setdefault("target", "U_1_0")
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_export_dir_status(
        self, 
        payload: int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_export_dir_status(
        self, 
        payload: int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_export_dir_status(
        self, 
        payload: int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取导出目录树的完成情况

        GET https://webapi.115.com/files/export_dir

        :payload:
            - export_id: int | str = 0 💡 任务 id
        """
        api = complete_url("/files/export_dir", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"export_id": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_export_dir_status_app(
        self, 
        payload: int | str | dict = 0, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_export_dir_status_app(
        self, 
        payload: int | str | dict = 0, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_export_dir_status_app(
        self, 
        payload: int | str | dict = 0, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取导出目录树的完成情况

        GET https://proapi.115.com/{app}/2.0/ufile/export_dir

        :payload:
            - export_id: int | str = 0 💡 任务 id
        """
        api = complete_url("/2.0/ufile/export_dir", base_url=base_url, app=app)
        if isinstance(payload, (int, str)):
            payload = {"export_id": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_file(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_file(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_file(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取文件或目录的基本信息

        GET https://webapi.115.com/files/get_info

        .. caution::
            仅当文件的 aid 是 1（网盘文件）、12（瞬间文件） 或 120（永久删除文件） 时，才能用此接口获取信息，否则请用 ``client.fs_file_skim`` 或 ``client.fs_supervision`` 获取信息（只能获取比较简略的信息）。

            特别的，文件被移入回收站后，就不能用此接口获取信息了，除非将其还原或永久删除。

        :payload:
            - file_id: int | str 💡 文件或目录的 id，不能为 0，只能传 1 个 id，如果有多个只采用第一个
        """
        api = complete_url("/files/get_info", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"file_id": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_file_skim(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        method: str = "GET", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_file_skim(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        method: str = "GET", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_file_skim(
        self, 
        payload: int | str | Iterable[int | str] | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        method: str = "GET", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取文件或目录的简略信息

        GET https://webapi.115.com/files/file

        .. note::
            如果需要查询的 id 特别多，请指定 `method="POST"`

        :payload:
            - file_id: int | str 💡 文件或目录的 id，不能为 0，多个用逗号 "," 隔开
        """
        api = complete_url("/files/file", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"file_id": payload}
        elif not isinstance(payload, dict):
            payload = {"file_id": ",".join(map(str, payload))}
        if method.upper() == "POST":
            request_kwargs["data"] = payload
        else:
            request_kwargs["params"] = payload
        return self.request(url=api, method=method, async_=async_, **request_kwargs)

    @overload
    def fs_files(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取目录中的文件列表和基本信息

        GET https://webapi.115.com/files

        .. attention::
            此接口被风控，此域名下的大量接口都会被风控，但重新登录可能恢复（多次登录必定不能恢复，即使更换设备）

        .. hint::
            指定如下条件中任一，且 cur = 0 （默认），即可遍历搜索所在目录树

            1. cid=0 且 star=1
            2. suffix 为非空的字符串
            3. type 为正整数
            4. show_dir=0 且 cur=0（或不指定 cur）

        .. hint::
            如果不指定或者指定的 cid 不存在，则会视为 cid=0 进行处理

            当指定 ``natsort=1`` 时，如果里面的数量较少时，可仅统计某个目录内的文件或目录总数，而不返回具体的文件信息

        .. hint::
            当一个 cookies 被另一个更新的登录所失效，并不意味着这个 cookies 就直接不可用了。

            如果你使用的是 ``proapi`` 下的接口，则会让你重新登录。但是 `webapi`、`aps` 等之下的接口，却依然可以正常使用。具体哪些失效，哪些还正常，请自行试验总结。这就意味着可以设计一种同一设备多 cookies 做池的分流策略。

        .. hint::
            对于普通的文件系统，我们只允许任何一个目录中不可有相同的名字，但是 115 网盘中却可能有重复：

            - 目录和文件同名：文件和目录同名在 115 中不算是一个冲突
            - 相同的目录名：转存可以导致同一目录下有多个相同名字的目录
            - 相同的文件名：转存、云下载和上传等，可以导致同一目录下有多个相同名字的文件

        .. hint::
            如果文件或目录被置顶，会在整个文件列表的最前面

            在根目录下且 ``fc_mix=0`` 且是特殊名字 ("最近接收", "手机相册", "云下载", "我的时光记录")（即 ``sys_dir``），会在整个文件列表的最前面但在置顶之后，这时可从返回信息的 "sys_count" 字段知道数目

        .. note::
            当 ``type=1`` 时，``suffix_type`` 的取值的含义：

                - (不填): 全部
                - 1: 文字（word，即 doc 和 docx 等）
                - 2: 表格（excel，即 xls 和 xlsx 等）
                - 3: 演示（ppt，即 ppt 和 pptx 等）
                - 4: pdf
                - 5: txt
                - 6: xmind
                - 7: 其它

        .. caution::
            ``fields`` 字段并不以返回的文件列表所有的字段名为准，而是要用其对应的完整名字，例如

            - "file_id": 对应 "cid"
            - "parent_id": 对应 "pid"
            - "area_id": 对应 "aid"
            - "file_name": 对应 "n"

            如果其中有一个是 "cid"，则可能只返回这一个字段，而不管你又指定了其它多少字段。
            由于这个参数的行为太过古怪且难猜，所以不建议使用，如果非要使用，建议换用 ``P115client.fs_files_app()``。

        :payload:
            - cid: int | str = 0 💡 目录 id，对应 parent_id
            - limit: int = 32 💡 分页大小，目前最大值是 1,150，以前是没限制的
            - offset: int = 0 💡 分页开始的索引，索引从 0 开始计算

            - aid: int = 1 💡 area_id

                - 0: 会被视为 1
                - 1: 正常文件
                - 2: <unknown>
                - 3: <unknown>
                - 4: <unknown>
                - 5: <unknown>
                - 7: 回收站文件
                - 9: <unknown>
                - 12: 瞬间文件
                - 15: <unknown>
                - 120: 彻底删除文件、简历附件
                - <其它>: 会被视为 0

            - asc: 0 | 1 = <default> 💡 是否升序排列。0:降序 1:升序
            - count_folders: 0 | 1 = 1 💡 统计文件数和目录数，好像也可以写成 ``countfolders``
            - cur: 0 | 1 = <default> 💡 是否只搜索当前目录
            - custom_order: 0 | 1 = <default> 💡 启用自定义排序，如果指定了 "asc"、"fc_mix"、"o" 中其一，则此参数会被自动设置为 1

                - 0: 使用记忆排序（自定义排序失效） 
                - 1: 使用自定义排序（不使用记忆排序） 
                - 2: 自定义排序（非目录置顶）

            - fc_mix: 0 | 1 = <default> 💡 是否目录和文件混合，如果为 0 则目录在前（目录置顶）
            - fields: str = <default> 💡 筛选字段（⚠️ 不建议使用），多个用逗号 "," 隔开，如果存在字段无效，则文件列表为空（但有计数 "count"、"file_count" 和 "folder_count"）
            - hidden: 0 | 1 = <default>
            - is_q: 0 | 1 = <default> 💡 如果为 1，只显示文件
            - is_share: 0 | 1 = <default>
            - last_utime: int | str = <default>
            - min_size: int = 0 💡 最小的文件大小
            - max_size: int = 0 💡 最大的文件大小（含），<= 0 表示不限，因此并不能借此仅筛选出空文件
            - natsort: 0 | 1 = <default> 💡 是否执行自然排序(natural sorting) 💡 natural sorting
            - nf: 0 | 1 = <default> 💡 不要显示文件（即仅显示目录），但如果 show_dir=0，则此参数无效
            - o: str = <default> 💡 用某字段排序

                - "file_name": 文件名
                - "file_size": 文件大小
                - "file_type": 文件种类
                - "user_utime": 修改时间
                - "user_ptime": 创建时间
                - "user_otime": 上一次打开时间

            - oof_token: str = <default>
            - qid: int | str = <default>
            - r_all: 0 | 1 = <default>
            - record_open_time: 0 | 1 = 1 💡 是否要记录目录的打开时间
            - scid: int | str = <default>
            - show_dir: 0 | 1 = 1 💡 是否显示目录，好像也可以写成 showdir
            - snap: 0 | 1 = <default>
            - source: str = <default>
            - sys_dir: int | str = <default>
            - star: 0 | 1 = <default> 💡 是否星标文件
            - stdir: 0 | 1 = <default> 💡 筛选文件时，是否显示目录：1:展示 0:不展示
            - suffix: str = <default> 💡 后缀名（优先级高于 `type`）
            - suffix_type: int = <default>
            - type: int = <default> 💡 文件类型

                - 0: 全部（仅当前目录）
                - 1: 文档
                - 2: 图片
                - 3: 音频
                - 4: 视频
                - 5: 压缩包
                - 6: 软件/应用
                - 7: 书籍
                - 8-11: 大概相当于 1
                - 12: 文档+图片+视频，相当于 1、2、4
                - 13: ？？？，音频
                - 14: ？？？，文档
                - 15: 图片+视频，相当于 2、4
                - 16: 字幕
                - 17~98: 大概相当于 1
                - 99: 所有文件
                - >=100: 大概相当于 1
        """
        api = complete_url("/files", base_url=base_url)
        if payload is None:
            return self.request(url=api, async_=async_, **request_kwargs)
        if isinstance(payload, (int, str)):
            payload = {"cid": payload}
        payload = {
            "aid": 1, "count_folders": 1, "limit": 32, "offset": 0, 
            "record_open_time": 1, "show_dir": 1, "cid": 0, **payload, 
        }
        if payload.keys() & frozenset(("asc", "fc_mix", "o")):
            payload.setdefault("custom_order", 2)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_files_app(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        app: str = "android", 
        version: str = "2.0", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files_app(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        app: str = "android", 
        version: str = "2.0", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files_app(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        app: str = "android", 
        version: str = "2.0", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取目录中的文件列表和基本信息

        GET https://proapi.115.com/{app}/{version}/ufile/files

        .. hint::
            如果要遍历获取所有文件，需要指定 show_dir=0 且 cur=0（或不指定 cur），这个接口并没有 type=99 时获取所有文件的意义

        .. note::
            一旦此接口被风控，那么同一域名下，所有路径尾部是 /files 的接口都被风控，但是如果你没有携带任何参数，竟然可以避免风控（至少可以用来获得一下文件总数，以及最近的 20 个创建的文件）。
            另外，proapi 之下，指定接口的版本为 2.0，可能会被服务器后台专门的处理，而其它版本（乃至于不指定版本），也往往被视为等同的，由此可以分为两类：2.0 版本和其它版本。

        .. attention::
            此接口存在一些潜在的问题。假如我上传了一个扩展名特别长的文件，越出了这个接口的能力范围，就会直接报错，例如：

            .. code:: python

                client.upload_file_sample(b"", filename="a."+"a"*300)
                # NOTE: 因而下面的请求将不会成功
                client.fs_files_app()

        .. caution::
            这个接口有些问题：

                1. 当 custom_order=1 时，如果设定 limit=1 可能会报错
                2. fc_mix 无论怎么设置，都和 fc_mix=0 的效果相同（即目录总是置顶），设置为 custom_order=2 也没用

        .. hint::
            置顶无效，但可以知道是否置顶了。

            在根目录下且 fc_mix=0 且是特殊名字 ("最近接收", "手机相册", "云下载", "我的时光记录")，会在整个文件列表的最前面，这时可从返回信息的 "sys_count" 字段知道数目

        .. tip::
            这个接口的 ``fields`` 参数可以筛选字段，例如，当你只需要文件 id 时，只需要指定 ``client.fs_files_app({"fields": "fid"})``

        :payload:
            - cid: int | str = 0 💡 目录 id，对应 parent_id
            - limit: int = 32 💡 分页大小，并没有上限，请预估返回的字节数自行调整规模，7,000 是大约安全了，如果用 ``fields`` 筛选了字段，可以增到 10,000，如果报 500 响应，则要适当减小些（或者多次尝试，偶有成功）
            - offset: int = 0 💡 分页开始的索引，索引从 0 开始计算

            - aid: int = 1 💡 area_id

                - 0: 会被视为 1
                - 1: 正常文件
                - 2: <unknown>
                - 3: <unknown>
                - 4: <unknown>
                - 5: <unknown>
                - 7: 回收站文件
                - 9: <unknown>
                - 12: 瞬间文件
                - 15: <unknown>
                - 120: 彻底删除文件、简历附件
                - <其它>: 会被视为 0

            - asc: 0 | 1 = <default> 💡 是否升序排列。0:降序 1:升序
            - count_folders: 0 | 1 = 1 💡 统计文件数和目录数
            - cur: 0 | 1 = <default>   💡 是否只显示当前目录
            - custom_order: 0 | 1 | 2 = <default> 💡 是否使用记忆排序。如果指定了 "asc"、"fc_mix"、"o" 中其一，则此参数会被自动设置为 2

                - 0: 使用记忆排序（自定义排序失效） 
                - 1: 使用自定义排序（不使用记忆排序） 
                - 2: 自定义排序（非目录置顶）

            - fc_mix: 0 | 1 = <default> 💡 是否目录和文件混合，如果为 0 则目录在前（目录置顶）
            - fields: str = <default> 💡 筛选字段，多个用逗号 "," 隔开，如果所有字段都无效，则返回全部
            - for: str = <default> 💡 文件格式，例如 "doc"
            - is_q: 0 | 1 = <default>
            - is_share: 0 | 1 = <default>
            - min_size: int = 0 💡 最小的文件大小
            - max_size: int = 0 💡 最大的文件大小（含），<= 0 表示不限，因此并不能借此仅筛选出空文件
            - natsort: 0 | 1 = <default> 💡 是否执行自然排序(natural sorting)
            - nf: 0 | 1 = <default> 💡 不要显示文件（即仅显示目录），但如果 show_dir=0，则此参数无效
            - o: str = <default> 💡 用某字段排序

                - "file_name": 文件名
                - "file_size": 文件大小
                - "file_type": 文件种类
                - "user_etime": 事件时间（无效，效果相当于 "user_utime"）
                - "user_utime": 修改时间
                - "user_ptime": 创建时间（无效，效果相当于 "user_utime"）
                - "user_otime": 上一次打开时间（无效，效果相当于 "user_utime"）

            - r_all: 0 | 1 = <default>
            - record_open_time: 0 | 1 = 1 💡 是否要记录目录的打开时间
            - scid: int | str = <default>
            - show_dir: 0 | 1 = 1 💡 是否显示目录
            - snap: 0 | 1 = <default>
            - source: str = <default>
            - sys_dir: int | str = <default> 💡 系统目录编号，0:最近接收 1:手机相册 2:云下载 3:我的时光记录 4,10,20,21,22,30,40,50,60,70:(未知)
            - star: 0 | 1 = <default> 💡 是否星标文件
            - stdir: 0 | 1 = <default> 💡 筛选文件时，是否显示目录：1:展示 0:不展示
            - suffix: str = <default> 💡 后缀名（优先级高于 `type`）
            - type: int = <default> 💡 文件类型

                - 0: 全部（仅当前目录）
                - 1: 文档
                - 2: 图片
                - 3: 音频
                - 4: 视频
                - 5: 压缩包
                - 6: 软件/应用
                - 7: 书籍
                - 8-11: 大概相当于 1
                - 12: 文档+图片+视频，相当于 1、2、4
                - 13: ？？？，音频
                - 14: 大概相当于 1
                - 15: 图片+视频，相当于 2、4
                - >= 16: 相当于 8
        """
        api = complete_url("/ufile/files", base_url=base_url, app=app, version=version)
        if payload is None:
            return self.request(url=api, async_=async_, **request_kwargs)
        if isinstance(payload, (int, str)):
            payload = {"cid": payload}
        payload = {
            "aid": 1, "count_folders": 1, "limit": 32, "offset": 0, 
            "record_open_time": 1, "show_dir": 1, "cid": 0, **payload, 
        }
        if payload.keys() & frozenset(("asc", "fc_mix", "o")):
            payload.setdefault("custom_order", 2)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_files_app2(
        self, 
        payload: None| int | str | dict = 0, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files_app2(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files_app2(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取目录中的文件列表和基本信息

        GET https://proapi.115.com/{app}/files

        .. hint::x
            如果要遍历获取所有文件，需要指定 show_dir=0 且 cur=0（或不指定 cur），这个接口并没有 type=99 时获取所有文件的意义

        .. caution::
            这个接口有些问题：

                1. 当 custom_order=1 时，如果设定 limit=1 可能会报错
                2. fc_mix 无论怎么设置，都和 fc_mix=0 的效果相同（即目录总是置顶），设置为 custom_order=2 也没用

        :payload:
            - cid: int | str = 0 💡 目录 id，对应 parent_id
            - limit: int = 32 💡 分页大小，最大值不一定，看数据量，7,000 应该总是安全的，10,000 有可能报错，但有时也可以 20,000 而成功
            - offset: int = 0 💡 分页开始的索引，索引从 0 开始计算

            - aid: int = 1 💡 area_id

                - 0: 会被视为 1
                - 1: 正常文件
                - 2: <unknown>
                - 3: <unknown>
                - 4: <unknown>
                - 5: <unknown>
                - 7: 回收站文件
                - 9: <unknown>
                - 12: 瞬间文件
                - 15: <unknown>
                - 120: 彻底删除文件、简历附件
                - <其它>: 会被视为 0

            - asc: 0 | 1 = <default> 💡 是否升序排列。0:降序 1:升序
            - count_folders: 0 | 1 = 1 💡 统计文件数和目录数
            - cur: 0 | 1 = <default> 💡 是否只搜索当前目录
            - custom_order: 0 | 1 | 2 = <default> 💡 启用自定义排序，如果指定了 "asc"、"fc_mix"、"o" 中其一，则此参数会被自动设置为 2

                - 0: 使用记忆排序（自定义排序失效） 
                - 1: 使用自定义排序（不使用记忆排序） 
                - 2: 自定义排序（非目录置顶）
 
            - fc_mix: 0 | 1 = <default> 💡 是否目录和文件混合，如果为 0 则目录在前（目录置顶）
            - for: str = <default> 💡 文件格式，例如 "doc"
            - hide_data: str = <default> 💡 是否返回文件数据
            - is_q: 0 | 1 = <default>
            - is_share: 0 | 1 = <default>
            - min_size: int = 0 💡 （⚠️ 似乎不可用）最小的文件大小
            - max_size: int = 0 💡 （⚠️ 似乎不可用）最大的文件大小（含），<= 0 表示不限，因此并不能借此仅筛选出空文件
            - natsort: 0 | 1 = <default> 💡 是否执行自然排序(natural sorting)
            - nf: 0 | 1 = <default> 💡 不要显示文件（即仅显示目录），但如果 show_dir=0，则此参数无效
            - o: str = <default> 💡 用某字段排序

                - "file_name": 文件名
                - "file_size": 文件大小
                - "file_type": 文件种类
                - "user_etime": 事件时间（无效，效果相当于 "user_utime"）
                - "user_utime": 修改时间
                - "user_ptime": 创建时间（无效，效果相当于 "user_utime"）
                - "user_otime": 上一次打开时间（无效，效果相当于 "user_utime"）

            - r_all: 0 | 1 = <default>
            - record_open_time: 0 | 1 = 1 💡 是否要记录目录的打开时间
            - scid: int | str = <default>
            - show_dir: 0 | 1 = 1 💡 是否显示目录
            - snap: 0 | 1 = <default>
            - source: str = <default>
            - sys_dir: int | str = <default>
            - star: 0 | 1 = <default> 💡 是否星标文件
            - stdir: 0 | 1 = <default> 💡 筛选文件时，是否显示目录：1:展示 0:不展示
            - suffix: str = <default> 💡 后缀名（优先级高于 `type`）
            - type: int = <default> 💡 文件类型

                - 0: 全部（仅当前目录）
                - 1: 文档
                - 2: 图片
                - 3: 音频
                - 4: 视频
                - 5: 压缩包
                - 6: 软件/应用
                - 7: 书籍
                - 8-11: 大概相当于 1
                - 12: 文档+图片+视频，相当于 1、2、4
                - 13: ？？？，音频
                - 14: 大概相当于 1
                - 15: 图片+视频，相当于 2、4
                - >= 16: 相当于 8
        """
        api = complete_url("/files", base_url=base_url, app=app, force_app=("wechatmini", "alipaymini"))
        if payload is None:
            return self.request(url=api, async_=async_, **request_kwargs)
        if isinstance(payload, (int, str)):
            payload = {"cid": payload}
        payload = {
            "aid": 1, "count_folders": 1, "limit": 32, "offset": 0, 
            "record_open_time": 1, "show_dir": 1, "cid": 0, **payload, 
        }
        if payload.keys() & frozenset(("asc", "fc_mix", "o")):
            payload.setdefault("custom_order", 2)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_files_aps(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://aps.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files_aps(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://aps.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files_aps(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://aps.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取目录中的文件列表和基本信息

        GET https://aps.115.com/natsort/files.php

        .. tip::
            这个接口的响应速度极快，在文件数特别多时，是 ``P115Client.fs_files`` 的数倍甚至数十倍的速度，更无论 ``P115Client.fs_files_app`` 了

        .. caution::
            这个函数最多获取任何一种排序条件下的前 ``1200 + n`` 条数据（``n >= 0`` 是一个潜在的不定限制）。

            ``o`` 参数无效，效果只等于 "file_name"，而 ``fc_mix`` 和 ``asc`` 可用。

            当 ``offset >= 1200 + n``，则相当于 ``offset=0&fc_mix=1``，即从头开始，且置顶项不会置顶

        .. hint::
            从技术上来讲最多可分别获取 ``(1200 + n) * 2`` 个文件和目录，即你可以通过改变顺序（asc 取 0 或者 1），来最多获取两倍于数量上限的不同条目，然后通过指定 ``show_dir=0&cur=1`` 和 ``show_dir=1&nf=1`` 来分别只获取文件或目录。

            不过对于文件，如果利用 ``type``、 ``suffix``、``min_size``、``max_size`` 等参数进行筛选，则可以获得更多，甚至可以是全部。

            注意：如果有置顶的条目，置顶条目总是出现，因此可能会使获取到的不同条目总数变少。

        :payload:
            - cid: int | str = 0 💡 目录 id，对应 parent_id
            - limit: int = 32 💡 分页大小，最大值是 1,200
            - offset: int = 0 💡 分页开始的索引，索引从 0 开始计算

            - aid: int = 1 💡 area_id

                - 0: 会被视为 1
                - 1: 正常文件
                - 2: <unknown>
                - 3: <unknown>
                - 4: <unknown>
                - 5: <unknown>
                - 7: 回收站文件
                - 9: <unknown>
                - 12: 瞬间文件
                - 15: <unknown>
                - 120: 彻底删除文件、简历附件
                - <其它>: 会被视为 0

            - asc: 0 | 1 = <default> 💡 是否升序排列。0:降序 1:升序
            - count_folders: 0 | 1 = 1 💡 统计文件数和目录数
            - cur: 0 | 1 = <default> 💡 是否只搜索当前目录
            - custom_order: 0 | 1 = <default> 💡 启用自定义排序，如果指定了 "asc"、"fc_mix" 中其一，则此参数会被自动设置为 1

                - 0: 使用记忆排序（自定义排序失效） 
                - 1: 使用自定义排序（不使用记忆排序） 
                - 2: 自定义排序（非目录置顶）

            - fc_mix: 0 | 1 = <default> 💡 是否目录和文件混合，如果为 0 则目录在前（目录置顶）
            - is_asc: 0 | 1 = <default>
            - min_size: int = 0 💡 最小的文件大小
            - max_size: int = 0 💡 最大的文件大小（含），<= 0 表示不限，因此并不能借此仅筛选出空文件
            - natsort: 0 | 1 = <default>
            - order: str = <default>
            - r_all: 0 | 1 = <default>
            - record_open_time: 0 | 1 = 1 💡 是否要记录目录的打开时间
            - scid: int | str = <default>
            - show_dir: 0 | 1 = 1 💡 是否显示目录
            - snap: 0 | 1 = <default>
            - source: str = <default>
            - sys_dir: int | str = <default>
            - star: 0 | 1 = <default> 💡 是否星标文件
            - stdir: 0 | 1 = <default> 💡 筛选文件时，是否显示目录：1:展示 0:不展示
            - suffix: str = <default> 💡 后缀名（优先级高于 `type`）
            - type: int = <default> 💡 文件类型

                - 0: 全部（仅当前目录）
                - 1: 文档
                - 2: 图片
                - 3: 音频
                - 4: 视频
                - 5: 压缩包
                - 6: 软件/应用
                - 7: 书籍
                - 8-12: 大概相当于 1
                - 13: ？？？，音频
                - 14-15: 大概相当于 1
                - 16: 字幕
                - 17~98: 大概相当于 1
                - 99: 所有文件
                - >=100: 大概相当于 1
        """
        api = complete_url("/natsort/files.php", base_url=base_url)
        if payload is None:
            return self.request(url=api, async_=async_, **request_kwargs)
        if isinstance(payload, (int, str)):
            payload = {"cid": payload}
        payload = {
            "aid": 1, "count_folders": 1, "limit": 32, "offset": 0, 
            "record_open_time": 1, "show_dir": 1, "cid": 0, **payload, 
        }
        if payload.keys() & frozenset(("asc", "fc_mix")):
            payload.setdefault("custom_order", 2)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_files_blank_document(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files_blank_document(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files_blank_document(
        self, 
        payload: str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """新建空白 office 文件

        POST https://webapi.115.com/files/blank_document

        :payload:
            - file_name: str      💡 文件名，不含后缀
            - pid: int | str = 0  💡 目录 id，对应 parent_id
            - type: 1 | 2 | 3 = 1 💡 1:Word文档(.docx) 2:Excel表格(.xlsx) 3:PPT文稿(.pptx)
        """
        api = complete_url("/files/blank_document", base_url=base_url)
        if isinstance(payload, str):
            payload = {"file_name": payload}
        payload = {"pid": 0, "type": 1, **payload}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_files_cover(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files_cover(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files_cover(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """查看是否有封面

        GET https://webapi.115.com/files/cover

        :payload:
            - file_id: int | str 💡 文件或目录 id
            - folder_as_file: 0 | 1 = <default>
        """
        api = complete_url("/files/cover", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"file_id": payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_files_cover_set(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files_cover_set(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files_cover_set(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """是否生成封面

        POST https://webapi.115.com/files/cover

        :payload:
            - file_id: int | str 💡 文件或目录 id，多个用逗号 "," 隔开
            - show: 0 | 1 = 1
        """
        api = complete_url("/files/cover", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"file_id": payload}
        payload.setdefault("show", 1)
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_files_image(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files_image(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files_image(
        self, 
        payload: int | str | dict, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取目录中的图片列表和基本信息

        GET https://webapi.115.com/files/imglist

        .. danger::
            这个函数大概是有 bug 的，不推荐使用，请用 ``fs_files_media`` 代替

        .. attention::
            只能获取直属于 ``cid`` 所在目录的图片，不会遍历整个目录树

        :payload:
            - cid: int | str     💡 目录 id
            - file_id: int | str 💡 不能是 0，可以是任何一个有效的 id（必须在自己网盘中，哪怕已经被删除，此必需参数只为应付检查）
            - limit: int = 32 💡 最多返回数量
            - offset: int = 0 💡 索引偏移，索引从 0 开始计算
            - is_asc: 0 | 1 = <default> 💡 是否升序排列
            - next: 0 | 1 = <default>
            - order: str = <default> 💡 用某字段排序

                - 文件名："file_name"
                - 文件大小："file_size"
                - 文件种类："file_type"
                - 修改时间："user_utime"
                - 创建时间："user_ptime"
                - 上一次打开时间："user_otime"
        """
        api = complete_url("/files/imglist", base_url=base_url)
        if isinstance(payload, (int, str)):
            payload = {"cid": payload}
        payload = {"limit": 32, "offset": 0, "cid": 0, **payload}
        if cid := payload.get("cid"):
            payload.setdefault("file_id", cid)
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_files_image_app(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files_image_app(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files_image_app(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取目录中的图片列表和基本信息

        GET https://proapi.115.com/{app}/files/imglist

        :payload:
            - cid: int | str = 0 💡 目录 id，对应 parent_id
            - limit: int = 32    💡 一页大小，建议控制在 <= 9000，不然会报错
            - offset: int = 0    💡 索引偏移，索引从 0 开始计算

            - aid: int = 1 💡 area_id

                - 0: 会被视为 1
                - 1: 正常文件
                - 2: <unknown>
                - 3: <unknown>
                - 4: <unknown>
                - 5: <unknown>
                - 7: 回收站文件
                - 9: <unknown>
                - 12: 瞬间文件
                - 15: <unknown>
                - 120: 彻底删除文件、简历附件
                - <其它>: 会被视为 0

            - asc: 0 | 1 = <default> 💡 是否升序排列
            - cur: 0 | 1 = 1 💡 只罗列当前目录
            - o: str = <default> 💡 用某字段排序

                - 文件名："file_name"
                - 文件大小："file_size"
                - 文件种类："file_type"
                - 修改时间："user_utime"
                - 创建时间："user_ptime"
                - 上一次打开时间："user_otime"
        """
        api = complete_url("/files/imglist", base_url=base_url, app=app)
        if payload is None:
            return self.request(url=api, async_=async_, **request_kwargs)
        if isinstance(payload, (int, str)):
            payload = {"cid": payload}
        payload = {"limit": 32, "offset": 0, "aid": 1, "cid": 0, "cur": 1, **payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_files_media(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files_media(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files_media(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取目录中的文件列表和基本信息（不含目录）

        GET https://webapi.115.com/files/medialist

        .. attention::
            有个 bug，当 ``cid=0&cur=1`` 时，似乎总是拉到空的文件列表，此时建议换成 ``client.fs_files({"cid": 0, "cur": 1, "show_dir": 0})``

        :payload:
            - cid: int | str = 0 💡 目录 id，对应 parent_id
            - limit: int = 32    💡 一页大小，建议控制在 <= 9000，不然会报错
            - offset: int = 0    💡 索引偏移，索引从 0 开始计算

            - aid: int = 1 💡 area_id

                - 0: 会被视为 1
                - 1: 正常文件
                - 2: <unknown>
                - 3: <unknown>
                - 4: <unknown>
                - 5: <unknown>
                - 7: 回收站文件
                - 9: <unknown>
                - 12: 瞬间文件
                - 15: <unknown>
                - 120: 彻底删除文件、简历附件
                - <其它>: 会被视为 0

            - asc: 0 | 1 = <default> 💡 是否升序排列
            - cur: 0 | 1 = 1 💡 只罗列当前目录
            - o: str = <default> 💡 用某字段排序

                - 文件名："file_name"
                - 文件大小："file_size"
                - 文件种类："file_type"
                - 修改时间："user_utime"
                - 创建时间："user_ptime"
                - 上一次打开时间："user_otime"

            - type: int = -1 💡 文件类型（不传则视为 4）

                - <0: 全部文件（不含目录），响应中的 "type" 为 0
                - 0: 视为 4，响应中的 "type" 为 4
                - 1: 文档
                - 2: 图片
                - 3: 音频
                - 4: 视频
                - 5: 压缩包
                - 6: 软件/应用
                - 7: 书籍
                - 8-11: 大概相当于 1
                - 12: 文档+图片+视频，相当于 1、2、4
                - 13: ？？？，音频
                - 14: ？？？，文档
                - 15: 图片+视频，相当于 2、4
                - 16: 字幕
                - 17~98: 大概相当于 1
                - 99: 所有文件
                - >=100: 大概相当于 1
        """
        api = complete_url("/files/medialist", base_url=base_url)
        if payload is None:
            return self.request(url=api, async_=async_, **request_kwargs)
        if not isinstance(payload, dict):
            payload = {"cid": payload}
        payload = {"limit": 32, "offset": 0, "aid": 1, "cid": 0, "cur": 1, "type": -1, **payload}
        if payload["type"] == 99:
            payload["type"] = -1
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_files_media_app(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files_media_app(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files_media_app(
        self, 
        payload: None | int | str | dict = 0, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取目录中的文件列表和基本信息（不含目录）

        GET https://proapi.115.com/{app}/files/medialist

        :payload:
            - cid: int | str = 0 💡 目录 id，对应 parent_id
            - limit: int = 32    💡 一页大小，建议控制在 <= 9000，不然会报错
            - offset: int = 0    💡 索引偏移，索引从 0 开始计算

            - aid: int = 1 💡 area_id

                - 0: 会被视为 1
                - 1: 正常文件
                - 2: <unknown>
                - 3: <unknown>
                - 4: <unknown>
                - 5: <unknown>
                - 7: 回收站文件
                - 9: <unknown>
                - 12: 瞬间文件
                - 15: <unknown>
                - 120: 彻底删除文件、简历附件
                - <其它>: 会被视为 0

            - asc: 0 | 1 = <default> 💡 是否升序排列
            - cur: 0 | 1 = 1 💡 只罗列当前目录
            - o: str = <default> 💡 用某字段排序

                - 文件名："file_name"
                - 文件大小："file_size"
                - 文件种类："file_type"
                - 修改时间："user_utime"
                - 创建时间："user_ptime"
                - 上一次打开时间："user_otime"

            - type: int =  💡 文件类型（不传则视为 4）

                - <0: 全部文件（不含目录），响应中的 "type" 为 0
                - 0: 视为 2，因为响应中的 "type" 为 2
                - 1: 文档
                - 2: 图片
                - 3: 音频
                - 4: 视频
                - 5: 压缩包
                - 6: 软件/应用
                - 7: 书籍
                - 8-11: 大概相当于 1
                - 12: 文档+图片+视频，相当于 1、2、4
                - 13: ？？？，音频
                - 14: 大概相当于 1
                - 15: 图片+视频，相当于 2、4
                - >= 16: 相当于 8
        """
        api = complete_url("/files/medialist", base_url=base_url, app=app)
        if payload is None:
            return self.request(url=api, async_=async_, **request_kwargs)
        if isinstance(payload, (int, str)):
            payload = {"cid": payload}
        payload = {"limit": 32, "offset": 0, "aid": 1, "cid": 0, "cur": 1, "type": -1, **payload}
        if payload["type"] == 99:
            payload["type"] = -1
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_files_recent_docs(
        self, 
        payload: int | dict = 1000, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files_recent_docs(
        self, 
        payload: int | dict = 1000, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files_recent_docs(
        self, 
        payload: int | dict = 1000, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取最近上传的文档

        POST https://webapi.115.com/files/recent

        .. todo::
            这个接口可能支持其它参数，但目前暂未搞清楚

        :payload:
            - limit: int
        """
        api = complete_url("/files/recent", base_url=base_url)
        if isinstance(payload, int):
            payload = {"limit": payload}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_files_recent_docs_app(
        self, 
        payload: int | dict = 1000, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files_recent_docs_app(
        self, 
        payload: int | dict = 1000, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files_recent_docs_app(
        self, 
        payload: int | dict = 1000, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取最近上传的文档

        POST https://proapi.115.com/{app}/2.0/ufile/recent

        .. todo::
            这个接口可能支持其它参数，但目前暂未搞清楚

        :payload:
            - limit: int
        """
        api = complete_url("/2.0/ufile/recent", base_url=base_url, app=app)
        if isinstance(payload, int):
            payload = {"limit": payload}
        return self.request(url=api, method="POST", data=payload, async_=async_, **request_kwargs)

    @overload
    def fs_files_second_type(
        self, 
        payload: Literal[1, 2, 3, 4, 5, 6, 7] | dict = 1, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files_second_type(
        self, 
        payload: Literal[1, 2, 3, 4, 5, 6, 7] | dict = 1, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files_second_type(
        self, 
        payload: Literal[1, 2, 3, 4, 5, 6, 7] | dict = 1, 
        /, 
        base_url: str | Callable[[], str] = "https://webapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取目录中某个文件类型的扩展名的（去重）列表

        GET https://webapi.115.com/files/get_second_type

        :payload:
            - cid: int | str = 0 💡 目录 id，对应 parent_id
            - type: int = 1 💡 文件类型

                - 1: 文档
                - 2: 图片
                - 3: 音频
                - 4: 视频
                - 5: 压缩包
                - 6: 软件/应用
                - 7: 书籍

            - file_label: int | str = <default> 💡 标签 id，多个用逗号 "," 隔开
        """
        api = complete_url("/files/get_second_type", base_url=base_url)
        if isinstance(payload, int):
            payload = {"type": payload}
        payload = {"cid": 0, "type": 1, **payload}
        return self.request(url=api, params=payload, async_=async_, **request_kwargs)

    @overload
    def fs_files_second_type_app(
        self, 
        payload: Literal[1, 2, 3, 4, 5, 6, 7] | dict = 1, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False] = False, 
        **request_kwargs, 
    ) -> dict:
        ...
    @overload
    def fs_files_second_type_app(
        self, 
        payload: Literal[1, 2, 3, 4, 5, 6, 7] | dict = 1, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[True], 
        **request_kwargs, 
    ) -> Coroutine[Any, Any, dict]:
        ...
    def fs_files_second_type_app(
        self, 
        payload: Literal[1, 2, 3, 4, 5, 6, 7] | dict = 1, 
        /, 
        app: str = "android", 
        base_url: str | Callable[[], str] = "https://proapi.115.com", 
        *, 
        async_: Literal[False, True] = False, 
        **request_kwargs, 
    ) -> dict | Coroutine[Any, Any, dict]:
        """获取目录中某个文件类型的扩展名的（去重）列表

        GET https://proapi.115.com/{app}/2.0/ufile/get_second_type

        :payload:
            - cid: int | str = 0 💡 目录 id，对应 parent_id
            - type: int = 1 💡 文件类型

                - 1: 文档
                - 2: 图片
                - 3: 音频
                - 4: 视频
                - 5: 压缩包
                - 6: 软件/应用
                - 7: 书籍

            - file_label: int | str = <default> 💡 标签 id，多个用逗号 "," 隔开
        """
        api = complete_url("/2.0/ufile/get_second_type", base_url=base_url, app=app)
        if isinstance(payload, int):
            payload = {"type": payload}
        payload = {"cid": 0, "type": 1, **payload}
        return self.request(url=api, pa
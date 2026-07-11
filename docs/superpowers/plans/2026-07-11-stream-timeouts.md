# Stream Timeouts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为所有 HTTP 流式代理请求增加覆盖响应头和首个响应体块的首字节期限，以及首块后的流空闲超时，同时保持现有重试、Key 健康和资源清理语义。

**Architecture:** `RouterConfig` 提供两个正数超时字段。`proxy_handler.py` 在每次流式上游发送前创建绝对截止时间，`proxy_support.py` 用其限制响应头等待并把超时转换为现有重试逻辑已处理的 `httpx.ReadTimeout`；响应建立后，`streaming.py` 的共享字节迭代器用同一截止时间读取首块，再以空闲超时读取后续块。三个协议生成器继续复用 `StreamLifecycle`，下游流建立后的超时只关闭当前流，不重放请求。

**Tech Stack:** Python 3.12、`asyncio.timeout()`、httpx、FastAPI/Starlette、pytest、Rich TUI。

---

### Task 1: 配置字段与校验

**Files:**
- Modify: `auto_model_key_router/config.py`
- Modify: `router-config.example.json`
- Test: `tests/test_config_service.py`

- [ ] **Step 1: 写缺省、自定义和非法值测试**

在 `tests/test_config_service.py` 添加：

```python
def test_stream_timeouts_have_defaults_and_accept_custom_values() -> None:
    defaults = RouterConfig.from_dict(config_data())
    custom_data = config_data()
    custom_data["stream_first_byte_timeout"] = 12.5
    custom_data["stream_idle_timeout"] = 34.5

    custom = RouterConfig.from_dict(custom_data)

    assert defaults.stream_first_byte_timeout == 90
    assert defaults.stream_idle_timeout == 180
    assert custom.stream_first_byte_timeout == 12.5
    assert custom.stream_idle_timeout == 34.5


@pytest.mark.parametrize(
    ("field", "value"),
    [
        ("stream_first_byte_timeout", 0),
        ("stream_first_byte_timeout", -1),
        ("stream_idle_timeout", 0),
        ("stream_idle_timeout", -1),
    ],
)
def test_stream_timeouts_must_be_positive(field: str, value: float) -> None:
    data = config_data()
    data[field] = value

    with pytest.raises(ValueError, match=field):
        RouterConfig.from_dict(data)
```

- [ ] **Step 2: 运行测试并确认失败**

Run: `python -m pytest tests\test_config_service.py -k "stream_timeout" -v`

Expected: FAIL，`RouterConfig` 没有 `stream_first_byte_timeout` / `stream_idle_timeout`，非法值也未被拒绝。

- [ ] **Step 3: 添加最小配置实现**

在 `empty_config_dict()` 的 `request_timeout` 后加入：

```python
"stream_first_byte_timeout": 90,
"stream_idle_timeout": 180,
```

在 `RouterConfig` 的所有无默认字段之后、`providers` 之前加入带默认值字段，避免修改现有直接构造调用：

```python
stream_first_byte_timeout: float = 90
stream_idle_timeout: float = 180
```

在 `RouterConfig.from_dict()` 构造参数中加入：

```python
stream_first_byte_timeout=float(raw.get("stream_first_byte_timeout", 90)),
stream_idle_timeout=float(raw.get("stream_idle_timeout", 180)),
```

在 `validate()` 的提前返回之前加入：

```python
if self.stream_first_byte_timeout <= 0:
    raise ValueError("stream_first_byte_timeout 必须大于 0")
if self.stream_idle_timeout <= 0:
    raise ValueError("stream_idle_timeout 必须大于 0")
```

在 `router-config.example.json` 的 `request_timeout` 后加入两个默认字段。

- [ ] **Step 4: 运行配置测试并确认通过**

Run: `python -m pytest tests\test_config_service.py -v`

Expected: PASS。

- [ ] **Step 5: 检查并提交配置变更**

Run: `git diff --check`

```bash
git add auto_model_key_router/config.py router-config.example.json tests/test_config_service.py
git commit -m "feat(config): 添加流式分段超时配置"
```

### Task 2: 共享流超时字节迭代器

**Files:**
- Modify: `auto_model_key_router/streaming.py`
- Create: `tests/test_streaming.py`

- [ ] **Step 1: 写首块、首块超时和空闲超时测试**

创建 `tests/test_streaming.py`：

```python
from __future__ import annotations

import asyncio

import pytest

from auto_model_key_router.streaming import iter_stream_bytes


async def delayed_bytes(delays_and_chunks: tuple[tuple[float, bytes], ...]):
    for delay, chunk in delays_and_chunks:
        await asyncio.sleep(delay)
        yield chunk


def test_iter_stream_bytes_yields_timely_chunks() -> None:
    async def consume() -> list[bytes]:
        loop = asyncio.get_running_loop()
        return [
            chunk
            async for chunk in iter_stream_bytes(
                delayed_bytes(((0, b"first"), (0, b"second"))),
                first_byte_deadline=loop.time() + 0.1,
                idle_timeout=0.1,
            )
        ]

    assert asyncio.run(consume()) == [b"first", b"second"]


@pytest.mark.parametrize(
    "delays_and_chunks,first_timeout,idle_timeout",
    [
        (((0.05, b"first"),), 0.01, 0.1),
        (((0, b"first"), (0.05, b"second")), 0.1, 0.01),
    ],
)
def test_iter_stream_bytes_times_out_by_stage(
    delays_and_chunks: tuple[tuple[float, bytes], ...],
    first_timeout: float,
    idle_timeout: float,
) -> None:
    async def consume() -> None:
        loop = asyncio.get_running_loop()
        async for _ in iter_stream_bytes(
            delayed_bytes(delays_and_chunks),
            first_byte_deadline=loop.time() + first_timeout,
            idle_timeout=idle_timeout,
        ):
            pass

    with pytest.raises(TimeoutError):
        asyncio.run(consume())
```

- [ ] **Step 2: 运行测试并确认导入失败**

Run: `python -m pytest tests\test_streaming.py -v`

Expected: collection ERROR，无法从 `streaming.py` 导入 `iter_stream_bytes`。

- [ ] **Step 3: 实现标准库异步迭代器**

在 `auto_model_key_router/streaming.py` 导入 `asyncio` 和 `AsyncIterator`，并添加：

```python
async def iter_stream_bytes(
    chunks: AsyncIterator[bytes],
    *,
    first_byte_deadline: float,
    idle_timeout: float,
) -> AsyncIterator[bytes]:
    iterator = aiter(chunks)
    loop = asyncio.get_running_loop()
    try:
        async with asyncio.timeout(max(0, first_byte_deadline - loop.time())):
            first = await anext(iterator)
    except StopAsyncIteration:
        return
    yield first

    while True:
        try:
            async with asyncio.timeout(idle_timeout):
                chunk = await anext(iterator)
        except StopAsyncIteration:
            return
        yield chunk
```

- [ ] **Step 4: 运行聚焦测试并确认通过**

Run: `python -m pytest tests\test_streaming.py -v`

Expected: PASS，耗时明显低于 1 秒。

- [ ] **Step 5: 检查并提交共享迭代器**

Run: `git diff --check`

```bash
git add auto_model_key_router/streaming.py tests/test_streaming.py
git commit -m "feat(proxy): 限制流式首字节与空闲等待"
```

### Task 3: 响应头期限与三协议接入

**Files:**
- Modify: `auto_model_key_router/proxy_support.py`
- Modify: `auto_model_key_router/proxy_handler.py`
- Modify: `tests/test_app.py`

- [ ] **Step 1: 让测试配置可覆盖短超时**

给 `tests/test_app.py::make_config` 增加参数并传给 `RouterConfig`：

```python
stream_first_byte_timeout: float = 90,
stream_idle_timeout: float = 180,
```

```python
stream_first_byte_timeout=stream_first_byte_timeout,
stream_idle_timeout=stream_idle_timeout,
```

- [ ] **Step 2: 写响应头前超时切 Key 测试**

添加一个异步 `MockTransport` handler：第一把 Key 等待 `0.05` 秒，第二把 Key 立即返回完整 SSE。使用 `stream_first_byte_timeout=0.01` 和两把 Key 发出流式请求，断言状态码 `200`、正文来自第二把 Key、Authorization 调用顺序为 `sk-1` 后 `sk-2`。这证明超时发生在下游响应建立前且进入现有 `httpx.RequestError` 重试路径。

```python
def test_stream_response_header_timeout_retries_next_key() -> None:
    async def handler(request: httpx.Request) -> httpx.Response:
        calls.append(request.headers["authorization"])
        if request.headers["authorization"] == "Bearer sk-1":
            await anyio.sleep(0.05)
        return httpx.Response(200, content=b"data: [DONE]\n\n")

    # 使用 make_config(..., keys=(key_1, key_2), stream_first_byte_timeout=0.01)
    # 通过 run_client POST /v1/chat/completions，stream=True。
    assert response.status_code == 200
    assert response.text == "data: [DONE]\n\n"
    assert calls == ["Bearer sk-1", "Bearer sk-2"]
```

- [ ] **Step 3: 写三个协议的响应体超时测试**

添加 `HangingStream(httpx.AsyncByteStream)`：首块按需立即产生，之后等待 `0.05` 秒再产生第二块。分别请求 `/v1/chat/completions`、`/v1/messages`、`/v1/responses`，设置 `stream_idle_timeout=0.01`。断言每条路径只调用一把 Key、响应在超时后结束，并从 metrics snapshot / key pool 现有查询接口确认失败被记录且 Key 已释放。另写首块等待 `0.05` 秒、`stream_first_byte_timeout=0.01` 的响应体测试，确认已建立下游响应后不会调用第二把 Key。

```python
class HangingStream(httpx.AsyncByteStream):
    def __init__(self, *, delay_first: bool = False) -> None:
        self.delay_first = delay_first

    async def __aiter__(self):
        if self.delay_first:
            await anyio.sleep(0.05)
        yield b'data: {"choices":[{"delta":{"content":"one"}}]}\n\n'
        await anyio.sleep(0.05)
        yield b"data: [DONE]\n\n"
```

- [ ] **Step 4: 运行新测试并确认失败或挂起保护生效**

Run: `python -m pytest tests\test_app.py -k "stream and timeout" -v`

Expected: FAIL；响应头超时尚未转换为可重试错误，响应体也尚未走共享迭代器。测试自身应在 1 秒内结束；若挂起说明测试没有使用毫秒级保护，先修测试。

- [ ] **Step 5: 在发送阶段应用绝对截止时间**

在 `proxy_support.py` 给 `_send_upstream` 新增可选 `first_byte_deadline: float | None = None`。先构造一次 `upstream_request`，流式截止时间存在时用剩余秒数包住 `client.send()`，并把内置 `TimeoutError` 转成现有调用者会捕获的 `httpx.ReadTimeout`：

```python
try:
    if first_byte_deadline is None:
        response = await client.send(upstream_request, stream=True)
    else:
        remaining = max(0, first_byte_deadline - asyncio.get_running_loop().time())
        async with asyncio.timeout(remaining):
            response = await client.send(upstream_request, stream=True)
except TimeoutError as exc:
    raise httpx.ReadTimeout(
        "timed out waiting for upstream response headers",
        request=upstream_request,
    ) from exc
```

保持 `_upstream_timeout()` 的 `read=None`，让响应体超时只由共享迭代器负责。

- [ ] **Step 6: 在每次真实流式发送前创建期限**

在 `_execute_attempt()` 使用事件循环时钟：

```python
first_byte_deadline = (
    asyncio.get_running_loop().time() + runtime.config.stream_first_byte_timeout
    if context.is_stream
    else None
)
```

把它传给 `_send_upstream()`。原生端点回退或工具过滤重试真正发出新请求前重新计算该变量，保证最终交给流生成器的是当前响应对应的期限。非流式请求继续传 `None`。

- [ ] **Step 7: 把共享迭代器接入三个生成器**

从 `streaming.py` 导入 `iter_stream_bytes`。给 `_streaming_response()`、`_stream_upstream()`、`_stream_anthropic_messages()` 和 `_stream_responses()` 增加 `first_byte_deadline: float` 与 `idle_timeout: float` 参数；在响应构造处传入当前期限和 `runtime.config.stream_idle_timeout`。三个函数都把：

```python
async for chunk in response.aiter_bytes():
```

替换为：

```python
async for chunk in iter_stream_bytes(
    response.aiter_bytes(),
    first_byte_deadline=first_byte_deadline,
    idle_timeout=idle_timeout,
):
```

保留现有 `except Exception` 和 `finally: await lifecycle.finish(...)`，不要捕获 `BaseException`，因此客户端取消语义不变；超时发生在已建立的 `StreamingResponse` 内时只由当前生成器处理，不回到 `_execute_attempt()` 重试。

- [ ] **Step 8: 更新直接调用生成器的现有测试**

`tests/test_app.py` 中直接调用 `_stream_upstream()` / `_stream_anthropic_messages()` 的错误日志测试传入：

```python
asyncio.get_running_loop().time() + 1,
1,
```

并在测试文件导入 `asyncio`。把旧测试 `test_stream_request_disables_upstream_read_timeout` 重命名为 `test_stream_request_keeps_httpx_body_read_timeout_disabled`，保留 `read is None` 断言，明确新语义不是恢复 httpx 单一读取超时。

- [ ] **Step 9: 运行流式聚焦测试并确认通过**

Run: `python -m pytest tests\test_app.py -k "timeout or stream" -v`

Expected: PASS；响应头前会切 Key，三协议响应体超时均结束当前流且不重放。

- [ ] **Step 10: 检查并提交协议接入**

Run: `git diff --check`

```bash
git add auto_model_key_router/proxy_support.py auto_model_key_router/proxy_handler.py tests/test_app.py
git commit -m "feat(proxy): 在协议流中应用分段超时"
```

### Task 4: TUI 超时配置入口

**Files:**
- Modify: `auto_model_key_router/config_editor.py`
- Modify: `auto_model_key_router/dashboard.py`
- Modify: `tests/test_tui.py`

- [ ] **Step 1: 写统一编辑三个超时值的交互测试**

在 `tests/test_tui.py` 使用已有 monkeypatch 模式，依次让 `prompt_text` 返回 `"75"`、`"95"`、`"185"`，调用新函数 `set_timeouts_interactively()`，断言保存后的三个字段分别为 `75.0`、`95.0`、`185.0`。再参数化非数字、零和负数，断言返回红色错误页且文件不变。

```python
def test_set_timeouts_interactively_updates_all_timeouts(tmp_path, monkeypatch) -> None:
    answers = iter(("75", "95", "185"))
    monkeypatch.setattr(config_editor, "prompt_text", lambda *args, **kwargs: next(answers))
    monkeypatch.setattr(config_editor, "restart_service_after_config_change", lambda *args: Text("reloaded"))

    result = config_editor.set_timeouts_interactively(config_path)

    saved = json.loads(config_path.read_text(encoding="utf-8"))
    assert saved["request_timeout"] == 75.0
    assert saved["stream_first_byte_timeout"] == 95.0
    assert saved["stream_idle_timeout"] == 185.0
    assert "已更新超时配置" in render_plain(result)
```

- [ ] **Step 2: 运行测试并确认失败**

Run: `python -m pytest tests\test_tui.py -k "set_timeouts" -v`

Expected: FAIL，`config_editor` 尚无 `set_timeouts_interactively`。

- [ ] **Step 3: 实现一个最小超时配置动作**

在 `config_editor.py` 添加一个函数，读取当前 `request_timeout` / `stream_first_byte_timeout` / `stream_idle_timeout`，用三个现有 `prompt_text()` 数字输入依次编辑，统一 `float()` 转换并拒绝任一非正数，然后一次 `commit_config_data()` 保存并复用 `restart_service_after_config_change()`。不要为三个字段创建三个页面或新组件。

```python
def set_timeouts_interactively(path: Path) -> Any:
    data = load_config_data(path)
    old_config = RouterConfig.from_dict(data)
    fields = (
        ("request_timeout", "普通请求超时（秒）", old_config.request_timeout),
        ("stream_first_byte_timeout", "流式首字节超时（秒）", old_config.stream_first_byte_timeout),
        ("stream_idle_timeout", "流式空闲超时（秒）", old_config.stream_idle_timeout),
    )
    values: dict[str, float] = {}
    for field, label, current in fields:
        try:
            value = float(prompt_text("超时配置", label, default=str(current)).strip())
        except ValueError:
            return section_panel("[red]超时必须是数字。[/red]", "超时配置", "red")
        if value <= 0:
            return section_panel("[red]超时必须大于 0。[/red]", "超时配置", "red")
        values[field] = value
    data.update(values)
    new_config = commit_config_data(path, data, old_config).new_config
    return Group(
        section_panel("已更新超时配置。", "超时配置", "green"),
        restart_service_after_config_change(path, old_config, new_config),
    )
```

- [ ] **Step 4: 接入现有 CLI 设置菜单**

在 `dashboard.py` 导入该函数，把单个 `("4", "超时配置")` 入口插入 `SETTINGS_OPTIONS`，后续配置迁移和版本更新顺延。`manage_cli_settings_interactively()` 对应分支调用一次该函数并用现有 `show_result_page()` 展示；这是一页统一设置，不拆分三个字段。

- [ ] **Step 5: 运行 TUI 测试并确认通过**

Run: `python -m pytest tests\test_tui.py -v`

Expected: PASS。

- [ ] **Step 6: 检查并提交 TUI 行为**

Run: `git diff --check`

```bash
git add auto_model_key_router/config_editor.py auto_model_key_router/dashboard.py tests/test_tui.py
git commit -m "feat(tui): 支持配置流式分段超时"
```

### Task 5: 用户文档与最终验证

**Files:**
- Modify: `README.md`
- Modify: `docs/USAGE.md`

- [ ] **Step 1: 更新配置示例和语义说明**

在 README 和 USAGE 的 `request_timeout` 配置示例后加入：

```json
"stream_first_byte_timeout": 90,
"stream_idle_timeout": 180,
```

说明 `stream_first_byte_timeout` 从发起流式上游请求起覆盖响应头与第一块响应体；`stream_idle_timeout` 只控制首块后相邻块间隔；两者必须大于 0；响应头前超时可按现有策略切 Key，响应建立后只结束当前流，不自动重放。不要声称 Key 冷却状态持久化。

- [ ] **Step 2: 检查文档关键词和格式**

Run: `rg -n "stream_first_byte_timeout|stream_idle_timeout|响应头|重放" README.md docs/USAGE.md`

Expected: 两份文档均包含配置字段，至少一处完整描述响应头覆盖和不重放语义。

Run: `git diff --check`

Expected: 无输出，退出码 0。

- [ ] **Step 3: 提交文档**

```bash
git add README.md docs/USAGE.md
git commit -m "docs(proxy): 补充流式超时配置说明"
```

- [ ] **Step 4: 运行完整测试集**

Run: `python -m pytest`

Expected: 全部测试 PASS，无 warning 导致失败。

- [ ] **Step 5: 检查最终仓库状态与提交边界**

Run: `git diff --check`

Run: `git status --short`

Expected: 两条命令均无输出。

Run: `git log -7 --oneline --decorate`

Expected: 依次可见设计、计划、配置、共享迭代器、协议接入、TUI、文档的独立 Conventional Commits，且未改写 `5dbddd5`。

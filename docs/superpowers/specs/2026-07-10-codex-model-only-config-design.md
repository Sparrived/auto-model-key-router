# Codex 模型配置最小化更新设计

## 背景

AMKR 的“一键配置 → Codex”当前除模型调用配置外，还会写入网络访问、响应存储、WSL 确认和功能开关，并会用仅包含 `OPENAI_API_KEY` 的对象重写 `auth.json`。这可能改变用户与模型路由无关的 Codex 设置。

## 目标

一键配置 Codex 时，仅更新 AMKR 接管模型调用所必需的字段，保留其他配置、注释、自定义 Provider 字段和鉴权字段。现有备份、状态检测与精确回滚行为保持不变。

## 更新范围

- `config.toml` 顶层只更新 `model_provider`、`model`、`review_model`、`model_reasoning_effort`。
- `[model_providers.OpenAI]` 只更新 `name`、`base_url`、`wire_api`、`requires_openai_auth`。
- `auth.json` 只更新 `OPENAI_API_KEY`。

## 保留范围

- 不再主动写入 `disable_response_storage`、`network_access`、`windows_wsl_setup_acknowledged`、`features.goals`。
- 保留其他 TOML 字段、表、注释、格式和 OpenAI Provider 自定义字段。
- 保留 `auth.json` 中除 `OPENAI_API_KEY` 外的所有字段。
- 旧版本已写入的非模型字段不主动删除。

## 实现策略

继续使用 `tomlkit`，但不替换整个 `model_providers.OpenAI` 表；不存在时创建，存在时验证为表并逐项更新白名单字段。读取现有 `auth.json` 并只赋值 `OPENAI_API_KEY`；无效 JSON 或非对象结构报错且不覆盖原文件。

## 测试与验收

- 先添加失败测试，覆盖无关字段和自定义字段保留。
- 验证模型路由字段与本地鉴权 Key 正确更新。
- 验证新建最小配置、错误输入保护、重复应用和精确回滚。

## 非目标

- 不清理旧版本留下的非模型字段。
- 不迁移用户自定义 Provider。
- 不改变 Claude Code 行为、备份格式或回滚语义。

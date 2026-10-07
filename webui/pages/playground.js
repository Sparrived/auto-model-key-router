// 试验场：用于试用与测试配置的模型（支持文本对话与图像生成）。

import { h, mount, errorText, copyText, formatClockSeconds } from "../dom.js";
import { api, getKey, ApiError } from "../api.js";
import {
  card, cardHead, notice, badge, empty, loading, render, toast, buttonNode,
  input, select, segmented, dialog,
} from "../ui.js";
import { icon } from "../icons.js";
import { normalizeModelKinds, kindOf, kindLabel } from "../model-kinds.js";

const TABS = [
  { id: "chat", label: "文本对话", icon: "chat" },
  { id: "image", label: "图像生成", icon: "image" },
];

const CHAT_PRESETS = [
  { label: "自我介绍", prompt: "你好！请用简短的几句话介绍一下你自己和你的能力。" },
  { label: "快速排序", prompt: "请用 TypeScript 实现一个快速排序算法，并解释它的时间复杂度与空间复杂度。" },
  { label: "量子力学", prompt: "请用通俗易懂的语言，向一名初中生解释量子纠缠的基本原理。" },
  { label: "写一首诗", prompt: "请以「晨曦中的江南水乡」为主题，创作一首现代散文诗。" },
];

const IMAGE_PRESETS = [
  { label: "赛博朋克都市", prompt: "A futuristic cyberpunk city street at rainy night, glowing neon signs, reflective wet pavement, cinematic lighting, ultra-detailed 8k" },
  { label: "宇航员猫咪", prompt: "A cute fluffy cat wearing a detailed NASA astronaut helmet on the moon, Earth visible in the black background, photorealistic" },
  { label: "江南水乡水墨", prompt: "Traditional Chinese ink wash painting of Jiangnan water town, mist, gentle rain, old stone bridge and weeping willows" },
  { label: "吉卜力梦幻森林", prompt: "Enchanted glowing forest with magical creatures, soft morning sunlight through canopy, Ghibli anime style, vibrant pastel colors" },
];

const IMAGE_SIZES = [
  { value: "1024x1024", label: "1024 × 1024 (正方形 1:1)" },
  { value: "1024x1792", label: "1024 × 1792 (手机竖屏 9:16)" },
  { value: "1792x1024", label: "1792 × 1024 (桌面横屏 16:9)" },
  { value: "512x512", label: "512 × 512 (小图 1:1)" },
];

const state = {
  activeTab: "chat",
  loading: true,
  error: null,
  workspaces: [],
  selectedWorkspace: "",
  models: [],
  modelKinds: null,

  // 文本对话状态
  chat: {
    selectedModel: "",
    systemPrompt: "",
    showSystemPrompt: false,
    temperature: 0.7,
    topP: 1.0,
    maxTokens: "",
    isStream: true,
    messages: [], // { id, role, content, reasoning, images: [], duration, ttft, usage, rawPayload, rawResponse, error }
    input: "",
    attachedImages: [], // base64 data URLs
    streaming: false,
    abortController: null,
    showSettings: true,
    expandedThinking: new Set(),
  },

  // 图像生成状态
  image: {
    selectedModel: "",
    prompt: "",
    size: "1024x1024",
    n: 1,
    quality: "standard",
    format: "url",
    generating: false,
    abortController: null,
    results: [], // { url, b64_json, revised_prompt }
    history: [], // { id, model, prompt, size, images: [], duration, timestamp, rawPayload, rawResponse }
  },

  // 检查载荷 / 图片大图模态框
  inspectPayload: null, // { title, request, response, latency }
  previewImage: null, // image URL / base64
};

let host = null;
let currentCtx = null;

// —— 数据拉取 ——

async function load() {
  state.error = null;
  try {
    const [modelsRes, kindsDoc, workspacesRes] = await Promise.all([
      api.v1Models(state.selectedWorkspace).catch(async () => {
        // 若 /v1/models 不可用，回退尝试 /api/models 与 /api/unified-model
        const fallback = await api.models();
        const items = (fallback?.models || []).map((m) => ({ id: m.id }));
        return { data: items };
      }),
      api.models().then((res) => {
        const names = (res?.models || []).map((m) => m.id);
        return api.modelKinds(names).catch(() => null);
      }).catch(() => null),
      api.workspaces().catch(() => ({ workspaces: [] })),
    ]);

    state.workspaces = (workspacesRes?.workspaces || []).map((w) => w.name || w.workspace).filter(Boolean);
    state.modelKinds = normalizeModelKinds(kindsDoc);

    const modelList = (modelsRes?.data || []).map((item) => String(item.id || "")).filter(Boolean);
    state.models = [...new Set(modelList)];

    // 默认选取文本模型与图像模型
    pickDefaultModels();
  } catch (err) {
    state.error = errorText(err);
  }
}

function pickDefaultModels() {
  if (!state.models.length) return;

  // 挑对话模型：优先 primary 为 text 或带 unified-model 的模型
  if (!state.chat.selectedModel || !state.models.includes(state.chat.selectedModel)) {
    const textModel = state.models.find((m) => m === "unified-model")
      || state.models.find((m) => kindOf(state.modelKinds, m) === "text")
      || state.models[0];
    state.chat.selectedModel = textModel || "";
  }

  // 挑图像模型：优先 primary 为 image 的模型
  if (!state.image.selectedModel || !state.models.includes(state.image.selectedModel)) {
    const imgModel = state.models.find((m) => kindOf(state.modelKinds, m) === "image")
      || state.models.find((m) => m.toLowerCase().includes("image") || m.toLowerCase().includes("dall") || m.toLowerCase().includes("flux"))
      || state.models.find((m) => m === "unified-model")
      || state.models[0];
    state.image.selectedModel = imgModel || "";
  }
}

// —— 简易安全 Markdown 渲染器 ——

function escapeHtml(text) {
  return String(text || "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#039;");
}

function renderMarkdown(rawText) {
  if (!rawText) return h("span.empty-content", "(空回复)");

  const container = h("div.md-content");
  const lines = String(rawText).split("\n");
  let inCodeBlock = false;
  let codeLang = "";
  let codeBuffer = [];
  let textBuffer = [];

  const flushTextBuffer = () => {
    if (!textBuffer.length) return;
    const blockText = textBuffer.join("\n");
    textBuffer = [];

    // 处理段落、引用、列表等
    const paras = blockText.split(/\n\s*\n/);
    for (const p of paras) {
      const trimmed = p.trim();
      if (!trimmed) continue;

      if (trimmed.startsWith("### ")) {
        container.append(h("h4", inlineMarkdown(trimmed.slice(4))));
      } else if (trimmed.startsWith("## ")) {
        container.append(h("h3", inlineMarkdown(trimmed.slice(3))));
      } else if (trimmed.startsWith("# ")) {
        container.append(h("h2", inlineMarkdown(trimmed.slice(2))));
      } else if (trimmed.startsWith("> ")) {
        const quoteText = trimmed.split("\n").map((l) => l.replace(/^>\s?/, "")).join("\n");
        container.append(h("blockquote", inlineMarkdown(quoteText)));
      } else if (trimmed.startsWith("- ") || trimmed.startsWith("* ") || /^\d+\.\s/.test(trimmed)) {
        const isNum = /^\d+\.\s/.test(trimmed);
        const listTag = isNum ? "ol" : "ul";
        const items = trimmed.split("\n").map((l) => l.replace(/^(\s*[-*]|\s*\d+\.)\s*/, "")).filter(Boolean);
        const listEl = h(listTag);
        for (const it of items) listEl.append(h("li", inlineMarkdown(it)));
        container.append(listEl);
      } else {
        const pEl = h("p");
        pEl.innerHTML = inlineMarkdownHtml(p);
        container.append(pEl);
      }
    }
  };

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.trim().startsWith("```")) {
      if (!inCodeBlock) {
        flushTextBuffer();
        inCodeBlock = true;
        codeLang = line.trim().slice(3).trim();
        codeBuffer = [];
      } else {
        inCodeBlock = false;
        const codeText = codeBuffer.join("\n");
        codeBuffer = [];
        container.append(buildCodeBlock(codeText, codeLang));
      }
      continue;
    }

    if (inCodeBlock) {
      codeBuffer.push(line);
    } else {
      textBuffer.push(line);
    }
  }

  if (inCodeBlock && codeBuffer.length) {
    container.append(buildCodeBlock(codeBuffer.join("\n"), codeLang));
  }
  flushTextBuffer();

  return container;
}

function buildCodeBlock(code, lang) {
  const codeEl = h("code", code);
  const copyBtn = buttonNode("复制", {
    small: true,
    variant: "text",
    iconName: "copy",
    onClick: async () => {
      try {
        await copyText(code);
        toast("代码已复制");
      } catch {
        toast("复制失败", "error");
      }
    },
  });

  return h("div.code-block",
    h("div.code-header",
      h("span.code-lang", lang || "plaintext"),
      copyBtn,
    ),
    h("pre", codeEl),
  );
}

function inlineMarkdown(text) {
  const span = h("span");
  span.innerHTML = inlineMarkdownHtml(text);
  return span;
}

function inlineMarkdownHtml(text) {
  let escaped = escapeHtml(text);
  // 行内代码 `code`
  escaped = escaped.replace(/`([^`]+)`/g, '<code class="mono-inline">$1</code>');
  // 粗体 **bold**
  escaped = escaped.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  // 斜体 *italic*
  escaped = escaped.replace(/(^|[^*])\*([^*]+)\*([^*]|$)/g, "$1<em>$2</em>$3");
  // 换行
  escaped = escaped.replace(/\n/g, "<br>");
  return escaped;
}

// —— 对话发送与流式处理 ——

async function handleSendChat() {
  const content = state.chat.input.trim();
  const images = [...state.chat.attachedImages];
  if (!content && !images.length) return;
  if (!state.chat.selectedModel) {
    toast("请先选择要测试的模型", "warn");
    return;
  }
  if (state.chat.streaming) return;

  const userMsgId = `user_${Date.now()}`;
  const assistantMsgId = `asst_${Date.now()}`;

  // 构造用户消息
  state.chat.messages.push({
    id: userMsgId,
    role: "user",
    content,
    images,
  });

  // 准备助手消息占位
  const assistantMsg = {
    id: assistantMsgId,
    role: "assistant",
    content: "",
    reasoning: "",
    streaming: true,
    duration: 0,
    ttft: null,
    usage: null,
    error: null,
    rawPayload: null,
    rawResponse: null,
  };
  state.chat.messages.push(assistantMsg);

  // 清空输入框与附加图片
  state.chat.input = "";
  state.chat.attachedImages = [];
  state.chat.streaming = true;

  // 自动展开正在思考的消息
  state.chat.expandedThinking.add(assistantMsgId);

  // 构建入站 messages 历史
  const apiMessages = [];
  if (state.chat.systemPrompt.trim()) {
    apiMessages.push({ role: "system", content: state.chat.systemPrompt.trim() });
  }

  for (const msg of state.chat.messages) {
    if (msg.id === assistantMsgId) continue;
    if (msg.role === "user") {
      if (msg.images && msg.images.length) {
        const parts = [];
        if (msg.content) parts.push({ type: "text", text: msg.content });
        for (const imgUrl of msg.images) {
          parts.push({ type: "image_url", image_url: { url: imgUrl } });
        }
        apiMessages.push({ role: "user", content: parts });
      } else {
        apiMessages.push({ role: "user", content: msg.content });
      }
    } else if (msg.role === "assistant" && !msg.error) {
      apiMessages.push({ role: "assistant", content: msg.content });
    }
  }

  const payload = {
    model: state.chat.selectedModel,
    messages: apiMessages,
    stream: state.chat.isStream,
    temperature: state.chat.temperature,
    top_p: state.chat.topP,
  };
  if (state.chat.maxTokens && !Number.isNaN(Number(state.chat.maxTokens))) {
    payload.max_tokens = Number(state.chat.maxTokens);
  }
  assistantMsg.rawPayload = payload;

  const controller = new AbortController();
  state.chat.abortController = controller;
  draw();

  try {
    const res = await api.chatCompletions({
      body: payload,
      workspace: state.selectedWorkspace || undefined,
      signal: controller.signal,
      onChunk: ({ content: currContent, reasoning: currReasoning, usage: currUsage, raw }) => {
        assistantMsg.content = currContent;
        if (currReasoning) assistantMsg.reasoning = currReasoning;
        if (currUsage) assistantMsg.usage = currUsage;
        assistantMsg.rawResponse = raw;
        // 增量刷新当前消息
        updateMessageDOM(assistantMsgId);
      },
    });

    assistantMsg.content = res.message.content;
    assistantMsg.reasoning = res.message.reasoning_content;
    assistantMsg.duration = res.duration;
    assistantMsg.ttft = res.ttft;
    assistantMsg.usage = res.usage;
    assistantMsg.rawResponse = res.raw;
    assistantMsg.streaming = false;
  } catch (err) {
    if (controller.signal.aborted) {
      assistantMsg.streaming = false;
    } else {
      assistantMsg.streaming = false;
      assistantMsg.error = errorText(err);
      toast(`调用失败: ${assistantMsg.error}`, "error");
    }
  } finally {
    state.chat.streaming = false;
    state.chat.abortController = null;
    draw();
    scrollToBottom();
  }
}

function handleStopChat() {
  if (state.chat.abortController) {
    state.chat.abortController.abort();
    state.chat.streaming = false;
    state.chat.abortController = null;
    draw();
  }
}

function handleClearChat() {
  if (state.chat.streaming) return;
  state.chat.messages = [];
  draw();
}

// —— 图像生成处理 ——

async function handleGenerateImage() {
  const prompt = state.image.prompt.trim();
  if (!prompt) {
    toast("请输入图像描述提示词", "warn");
    return;
  }
  if (!state.image.selectedModel) {
    toast("请先选择图像模型", "warn");
    return;
  }
  if (state.image.generating) return;

  state.image.generating = true;
  state.image.results = [];
  const controller = new AbortController();
  state.image.abortController = controller;

  const payload = {
    model: state.image.selectedModel,
    prompt,
    n: state.image.n,
    size: state.image.size,
    quality: state.image.quality,
    response_format: state.image.format,
  };

  draw();

  try {
    const res = await api.imageGenerations({
      body: payload,
      workspace: state.selectedWorkspace || undefined,
      signal: controller.signal,
    });

    const items = res.data || [];
    state.image.results = items;

    // 加入历史记录
    const record = {
      id: `img_${Date.now()}`,
      model: state.image.selectedModel,
      prompt,
      size: state.image.size,
      images: items,
      duration: res.duration,
      timestamp: new Date().toISOString(),
      rawPayload: payload,
      rawResponse: res.raw,
    };
    state.image.history.unshift(record);
    if (state.image.history.length > 20) state.image.history.pop();
    toast(`成功生成 ${items.length} 张图片（耗时 ${res.duration}ms）`, "good");
  } catch (err) {
    if (!controller.signal.aborted) {
      toast(`生成失败: ${errorText(err)}`, "error");
    }
  } finally {
    state.image.generating = false;
    state.image.abortController = null;
    draw();
  }
}

// —— 图片上传与粘贴处理 ——

function attachImageFile(file) {
  if (!file || !file.type.startsWith("image/")) {
    toast("请选择有效的图片文件", "warn");
    return;
  }
  if (file.size > 15 * 1024 * 1024) {
    toast("图片体积不能超过 15MB", "warn");
    return;
  }
  const reader = new FileReader();
  reader.onload = (e) => {
    state.chat.attachedImages.push(e.target.result);
    drawChatInputBar();
  };
  reader.readAsDataURL(file);
}

// —— 增量局部 DOM 更新 ——

function updateMessageDOM(msgId) {
  const node = host?.querySelector(`[data-msg-id="${msgId}"]`);
  if (!node) return;
  const msg = state.chat.messages.find((m) => m.id === msgId);
  if (!msg) return;

  const bodyEl = node.querySelector(".chat-msg-body");
  if (bodyEl) {
    mount(bodyEl, buildAssistantMsgBody(msg));
  }
}

function scrollToBottom() {
  const scrollEl = host?.querySelector(".chat-messages-scroll");
  if (scrollEl) {
    scrollEl.scrollTop = scrollEl.scrollHeight;
  }
}

// —— 视图构建 ——

function buildHeader() {
  return h("div.page-head",
    h("div",
      h("h1", "模型试验场"),
      h("p.sub", "试用并测试已配置的上游模型路由与统一模型，实时检验对话流式响应与图片生成效果。"),
    ),
    h("div.spacer"),
    // 工作空间切换下拉（如果有额外空间）
    state.workspaces.length ? h("div.pg-workspace-picker",
      h("span.pg-picker-label", "空间:"),
      select([
        { value: "", label: "默认空间 (default)" },
        ...state.workspaces.map((w) => ({ value: w, label: w })),
      ], {
        value: state.selectedWorkspace,
        onChange: async (e) => {
          state.selectedWorkspace = e.target.value;
          state.loading = true;
          draw();
          await load();
          state.loading = false;
          draw();
        },
      }),
    ) : null,
    // 标签页切换分段控件
    segmented(TABS, state.activeTab, (id) => {
      state.activeTab = id;
      draw();
    }),
  );
}

// —— 对话视图 (Chat Tab) ——

function buildChatView() {
  const modelOptions = state.models.map((m) => {
    const kind = kindOf(state.modelKinds, m);
    const label = kind && kind !== "unknown" ? `${m} (${kindLabel(kind)})` : m;
    return { value: m, label };
  });

  return h("div.playground-split",
    // 左侧主要区域：消息列表 + 输入框
    h("div.pg-chat-main",
      // 顶部模型栏
      h("div.pg-model-bar",
        h("div.pg-model-select-group",
          h("span.pg-label", "测试模型:"),
          modelOptions.length ? select(modelOptions, {
            value: state.chat.selectedModel,
            onChange: (e) => {
              state.chat.selectedModel = e.target.value;
              draw();
            },
          }) : h("span.muted", "无可用模型"),
          state.chat.selectedModel ? badge(
            state.chat.selectedModel === "unified-model" ? "统一模型" : kindLabel(kindOf(state.modelKinds, state.chat.selectedModel)),
            "muted",
          ) : null,
        ),
        h("div.spacer"),
        buttonNode(state.chat.showSettings ? "隐藏参数" : "调整参数", {
          small: true,
          variant: "text",
          iconName: "sliders",
          onClick: () => {
            state.chat.showSettings = !state.chat.showSettings;
            draw();
          },
        }),
        buttonNode("清空对话", {
          small: true,
          variant: "text",
          iconName: "trash",
          disabled: state.chat.streaming || !state.chat.messages.length,
          onClick: handleClearChat,
        }),
      ),
      // 消息滚动容器
      h("div.chat-messages-scroll",
        state.chat.messages.length ? h("div.chat-messages-list",
          state.chat.messages.map((msg) => buildMessageNode(msg)),
        ) : buildChatEmptyState(),
      ),
      // 底部输入栏
      buildChatInputBar(),
    ),
    // 右侧设置侧边栏
    state.chat.showSettings ? buildChatSettingsSidebar() : null,
  );
}

function buildChatEmptyState() {
  return h("div.pg-empty-chat",
    h("div.pg-empty-icon", icon("chat", { size: 40 })),
    h("h3", "开始试用文本模型"),
    h("p.muted", "选择模型并在下方输入消息，或点击以下快捷预设提示词快速测试："),
    h("div.preset-chips",
      CHAT_PRESETS.map((p) => h("button.chip", {
        type: "button",
        onClick: () => {
          state.chat.input = p.prompt;
          drawChatInputBar();
          const textarea = host?.querySelector(".chat-input-textarea");
          if (textarea) textarea.focus();
        },
      }, p.label)),
    ),
  );
}

function buildMessageNode(msg) {
  const isUser = msg.role === "user";
  const item = h(`div.chat-msg.${isUser ? "msg-user" : "msg-assistant"}`, {
    "data-msg-id": msg.id,
  },
    h("div.chat-msg-header",
      h("span.chat-msg-avatar", isUser ? icon("key", { size: 14 }) : icon("sparkles", { size: 14 })),
      h("span.chat-msg-author", isUser ? "你 (User)" : (state.chat.selectedModel || "助手")),
      h("div.spacer"),
      !isUser && msg.duration ? h("span.chat-msg-meta", `耗时 ${msg.duration}ms${msg.ttft ? ` · 首字 ${msg.ttft}ms` : ""}`) : null,
      !isUser && msg.usage?.total_tokens ? h("span.chat-msg-meta", `· ${msg.usage.total_tokens} tokens`) : null,
      // 载荷检查按钮
      msg.rawPayload ? buttonNode("", {
        small: true,
        variant: "text",
        title: "查看入站请求与原始响应",
        iconName: "eye",
        onClick: () => {
          state.inspectPayload = {
            title: `调用详情 · ${msg.role === "user" ? "用户提问" : "助手回复"}`,
            request: msg.rawPayload,
            response: msg.rawResponse,
            duration: msg.duration,
            usage: msg.usage,
          };
          draw();
        },
      }) : null,
      // 复制按钮
      buttonNode("", {
        small: true,
        variant: "text",
        title: "复制文本",
        iconName: "copy",
        onClick: async () => {
          try {
            await copyText(msg.content);
            toast("已复制消息内容");
          } catch {
            toast("复制失败", "error");
          }
        },
      }),
    ),
    h("div.chat-msg-body",
      isUser ? buildUserMsgBody(msg) : buildAssistantMsgBody(msg),
    ),
  );
  return item;
}

function buildUserMsgBody(msg) {
  const children = [];
  if (msg.images && msg.images.length) {
    children.push(h("div.chat-attached-gallery",
      msg.images.map((img) => h("img.chat-attached-thumb", {
        src: img,
        alt: "附带图片",
        onClick: () => {
          state.previewImage = img;
          draw();
        },
      })),
    ));
  }
  children.push(h("div.chat-user-text", msg.content || ""));
  return children;
}

function buildAssistantMsgBody(msg) {
  const children = [];

  // 如果有思考过程 (reasoning_content)，展示思考折叠块
  if (msg.reasoning) {
    const isExpanded = state.chat.expandedThinking.has(msg.id);
    const thinkingNode = h("div.thinking-block",
      h("button.thinking-header", {
        type: "button",
        onClick: () => {
          if (isExpanded) state.chat.expandedThinking.delete(msg.id);
          else state.chat.expandedThinking.add(msg.id);
          updateMessageDOM(msg.id);
        },
      },
        icon("sparkles", { size: 14 }),
        h("span", msg.streaming && !msg.content ? "正在深入思考中…" : "深度思考过程"),
        msg.streaming && !msg.content ? h("span.pulse-dot") : null,
        icon("chevron", { size: 14, class: isExpanded ? "icon-rotated" : "" }),
      ),
      isExpanded ? h("div.thinking-content", msg.reasoning) : null,
    );
    children.push(thinkingNode);
  }

  // 消息正文
  if (msg.content) {
    children.push(renderMarkdown(msg.content));
  } else if (msg.streaming && !msg.reasoning) {
    children.push(h("div.streaming-cursor", h("span.spinner-sm"), h("span.muted", "正在生成回复…")));
  }

  // 错误提示
  if (msg.error) {
    children.push(notice(`模型调用出错: ${msg.error}`, "error"));
  }

  return children;
}

function buildChatInputBar() {
  const hostBar = h("div.pg-chat-input-container");

  // 附加图片缩略图条
  if (state.chat.attachedImages.length) {
    const previewBar = h("div.attached-images-bar",
      state.chat.attachedImages.map((imgUrl, idx) => h("div.attached-thumb-wrap",
        h("img.attached-thumb", { src: imgUrl, alt: "附件图片" }),
        h("button.attached-remove-btn", {
          type: "button",
          title: "移除图片",
          onClick: () => {
            state.chat.attachedImages.splice(idx, 1);
            drawChatInputBar();
          },
        }, icon("close", { size: 12 })),
      )),
    );
    hostBar.append(previewBar);
  }

  const textarea = h("textarea.chat-input-textarea", {
    placeholder: "输入消息…（Enter 发送，Shift + Enter 换行，支持直接粘贴图片）",
    value: state.chat.input,
    rows: 2,
    onInput: (e) => {
      state.chat.input = e.target.value;
      // 自动调节高度
      e.target.style.height = "auto";
      e.target.style.height = `${Math.min(e.target.scrollHeight, 180)}px`;
    },
    onKeydown: (e) => {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        handleSendChat();
      }
    },
    onPaste: (e) => {
      const items = e.clipboardData?.items;
      if (items) {
        for (const item of items) {
          if (item.type.startsWith("image/")) {
            const file = item.getAsFile();
            if (file) attachImageFile(file);
          }
        }
      }
    },
  });

  // 隐藏的文件上传 input
  const fileInput = h("input", {
    type: "file",
    accept: "image/*",
    style: { display: "none" },
    onChange: (e) => {
      const file = e.target.files?.[0];
      if (file) attachImageFile(file);
      e.target.value = "";
    },
  });

  const uploadBtn = buttonNode("", {
    variant: "text",
    title: "上传图片进行视觉测试（Vision）",
    iconName: "image",
    onClick: () => fileInput.click(),
  });

  const sendBtn = state.chat.streaming
    ? buttonNode("停止", {
      variant: "danger",
      iconName: "stop",
      onClick: handleStopChat,
    })
    : buttonNode("发送", {
      variant: "primary",
      iconName: "send",
      disabled: !state.chat.input.trim() && !state.chat.attachedImages.length,
      onClick: handleSendChat,
    });

  const actionRow = h("div.chat-input-actions",
    uploadBtn,
    fileInput,
    h("span.chat-input-hint", "支持视觉模型多模态输入"),
    h("div.spacer"),
    sendBtn,
  );

  hostBar.append(textarea, actionRow);
  return hostBar;
}

function drawChatInputBar() {
  const container = host?.querySelector(".pg-chat-input-container");
  if (container) {
    const parent = container.parentElement;
    const newBar = buildChatInputBar();
    parent?.replaceChild(newBar, container);
  }
}

function buildChatSettingsSidebar() {
  return h("div.pg-sidebar",
    h("div.pg-sidebar-head",
      h("h3", "推理参数配置"),
    ),
    h("div.pg-sidebar-body",
      // 系统提示词
      h("div.pg-param-item",
        h("div.pg-param-label-row",
          h("span.pg-param-name", "系统提示词 (System Prompt)"),
        ),
        h("textarea.input.pg-textarea", {
          rows: 3,
          placeholder: "例如：你是一位资深的高级软件架构师，回答要求言简意赅…",
          value: state.chat.systemPrompt,
          onInput: (e) => { state.chat.systemPrompt = e.target.value; },
        }),
      ),
      // 流式传输开关
      h("div.pg-param-item",
        h("div.pg-param-label-row",
          h("span.pg-param-name", "流式输出 (SSE Stream)"),
          h("label.toggle-switch",
            h("input", {
              type: "checkbox",
              checked: state.chat.isStream,
              onChange: (e) => { state.chat.isStream = e.target.checked; },
            }),
            h("span.slider"),
          ),
        ),
        h("p.pg-param-hint", "开启后像打字机一样逐字实时返回结果，可显著降低首字延迟体验。"),
      ),
      // 温度 (Temperature)
      h("div.pg-param-item",
        h("div.pg-param-label-row",
          h("span.pg-param-name", "温度 (Temperature)"),
          h("span.mono.pg-param-val", String(state.chat.temperature)),
        ),
        h("input.slider-range", {
          type: "range",
          min: "0",
          max: "2",
          step: "0.1",
          value: String(state.chat.temperature),
          onInput: (e) => {
            state.chat.temperature = parseFloat(e.target.value);
            const valEl = e.target.parentElement.querySelector(".pg-param-val");
            if (valEl) valEl.textContent = String(state.chat.temperature);
          },
        }),
        h("p.pg-param-hint", "值越高回复越富创造性，值越低回复越严谨确定。"),
      ),
      // Top P
      h("div.pg-param-item",
        h("div.pg-param-label-row",
          h("span.pg-param-name", "核采样 (Top P)"),
          h("span.mono.pg-param-val", String(state.chat.topP)),
        ),
        h("input.slider-range", {
          type: "range",
          min: "0.1",
          max: "1.0",
          step: "0.05",
          value: String(state.chat.topP),
          onInput: (e) => {
            state.chat.topP = parseFloat(e.target.value);
            const valEl = e.target.parentElement.querySelector(".pg-param-val");
            if (valEl) valEl.textContent = String(state.chat.topP);
          },
        }),
      ),
      // 最大 Tokens
      h("div.pg-param-item",
        h("div.pg-param-label-row",
          h("span.pg-param-name", "最大回复 Tokens"),
        ),
        input({
          type: "number",
          placeholder: "留空为模型默认上限",
          value: state.chat.maxTokens,
          onInput: (e) => { state.chat.maxTokens = e.target.value; },
        }),
      ),
      // 快捷预设提示词
      h("div.pg-param-item",
        h("div.pg-param-label-row",
          h("span.pg-param-name", "常用测试预设"),
        ),
        h("div.preset-chips-col",
          CHAT_PRESETS.map((p) => h("button.btn.small.text.pg-preset-btn", {
            type: "button",
            onClick: () => {
              state.chat.input = p.prompt;
              drawChatInputBar();
              const textarea = host?.querySelector(".chat-input-textarea");
              if (textarea) textarea.focus();
            },
          }, p.label)),
        ),
      ),
    ),
  );
}

// —— 图像生成视图 (Image Tab) ——

function buildImageView() {
  const imageModels = state.models.map((m) => {
    const kind = kindOf(state.modelKinds, m);
    const label = kind && kind !== "unknown" ? `${m} (${kindLabel(kind)})` : m;
    return { value: m, label };
  });

  return h("div.playground-split",
    // 左侧：图像生成参数与输入
    h("div.pg-sidebar.pg-image-controls",
      h("div.pg-sidebar-head",
        h("h3", "图像生成参数"),
      ),
      h("div.pg-sidebar-body",
        // 模型选择
        h("div.pg-param-item",
          h("div.pg-param-label-row", h("span.pg-param-name", "图像模型:")),
          imageModels.length ? select(imageModels, {
            value: state.image.selectedModel,
            onChange: (e) => { state.image.selectedModel = e.target.value; },
          }) : h("span.muted", "无可用模型"),
          state.image.selectedModel ? badge(kindLabel(kindOf(state.modelKinds, state.image.selectedModel)), "muted") : null,
        ),
        // 提示词
        h("div.pg-param-item",
          h("div.pg-param-label-row", h("span.pg-param-name", "图像提示词 (Prompt):")),
          h("textarea.input.pg-textarea", {
            rows: 4,
            placeholder: "描述你希望生成的图片内容、风格、光影与细节…",
            value: state.image.prompt,
            onInput: (e) => { state.image.prompt = e.target.value; },
          }),
        ),
        // 快捷预设
        h("div.pg-param-item",
          h("div.pg-param-label-row", h("span.pg-param-name", "预设示例灵感:")),
          h("div.preset-chips",
            IMAGE_PRESETS.map((p) => h("button.chip", {
              type: "button",
              onClick: () => {
                state.image.prompt = p.prompt;
                draw();
              },
            }, p.label)),
          ),
        ),
        // 尺寸
        h("div.pg-param-item",
          h("div.pg-param-label-row", h("span.pg-param-name", "图片分辨率 (Size):")),
          select(IMAGE_SIZES, {
            value: state.image.size,
            onChange: (e) => { state.image.size = e.target.value; },
          }),
        ),
        // 数量与质量并排
        h("div.form-grid-2",
          h("div.pg-param-item",
            h("div.pg-param-label-row", h("span.pg-param-name", "生成张数:")),
            select([
              { value: "1", label: "1 张" },
              { value: "2", label: "2 张" },
              { value: "4", label: "4 张" },
            ], {
              value: String(state.image.n),
              onChange: (e) => { state.image.n = parseInt(e.target.value, 10); },
            }),
          ),
          h("div.pg-param-item",
            h("div.pg-param-label-row", h("span.pg-param-name", "输出格式:")),
            select([
              { value: "url", label: "图片 URL" },
              { value: "b64_json", label: "Base64 数据" },
            ], {
              value: state.image.format,
              onChange: (e) => { state.image.format = e.target.value; },
            }),
          ),
        ),
        // 生成按钮
        h("div.pg-generate-action",
          buttonNode(state.image.generating ? "正在生成中…" : "立即生成图片", {
            variant: "primary",
            iconName: state.image.generating ? "sparkles" : "image",
            disabled: state.image.generating || !state.image.prompt.trim(),
            onClick: handleGenerateImage,
          }),
        ),
      ),
    ),
    // 右侧：生成结果画廊与历史记录
    h("div.pg-chat-main.pg-image-main",
      h("div.pg-image-display-area",
        state.image.generating ? buildImageGeneratingState()
          : state.image.results.length ? buildImageResultsGallery(state.image.results)
          : buildImageEmptyState(),
      ),
      // 历史生成记录
      state.image.history.length ? buildImageHistorySection() : null,
    ),
  );
}

function buildImageGeneratingState() {
  return h("div.pg-empty-chat",
    h("span.spinner-lg"),
    h("h3", "正在绘制图像中…"),
    h("p.muted", "AI 图像模型通常需要 5~20 秒进行扩散渲染，请稍候。"),
  );
}

function buildImageEmptyState() {
  return h("div.pg-empty-chat",
    h("div.pg-empty-icon", icon("image", { size: 40 })),
    h("h3", "试用图像生成模型"),
    h("p.muted", "在左侧选择模型、输入画面描述词并点击「立即生成图片」，生成结果将在此展示。"),
  );
}

function buildImageResultsGallery(results) {
  return h("div.image-gallery-container",
    h("div.gallery-head",
      h("h3", "本次生成结果"),
      badge(`${results.length} 张图片`, "muted"),
    ),
    h("div.image-cards-grid",
      results.map((item, idx) => buildSingleImageCard(item, idx)),
    ),
  );
}

function buildSingleImageCard(item, idx) {
  const imgSrc = item.url || (item.b64_json ? `data:image/png;base64,${item.b64_json}` : "");

  return h("div.image-card",
    h("div.image-card-preview", {
      onClick: () => {
        state.previewImage = imgSrc;
        draw();
      },
    },
      h("img", { src: imgSrc, alt: `生成结果 ${idx + 1}` }),
      h("div.image-card-hover-mask",
        icon("eye", { size: 24 }),
        h("span", "点击放大预览"),
      ),
    ),
    h("div.image-card-info",
      item.revised_prompt ? h("p.image-revised-prompt", { title: item.revised_prompt }, `优化提示词: ${item.revised_prompt}`) : null,
      h("div.image-card-actions",
        buttonNode("查看大图", {
          small: true,
          variant: "text",
          iconName: "eye",
          onClick: () => {
            state.previewImage = imgSrc;
            draw();
          },
        }),
        buttonNode("下载", {
          small: true,
          variant: "text",
          iconName: "download",
          onClick: () => downloadImage(imgSrc, `amkr-generated-${Date.now()}.png`),
        }),
        buttonNode("复制数据", {
          small: true,
          variant: "text",
          iconName: "copy",
          onClick: async () => {
            try {
              await copyText(item.url || item.b64_json || "");
              toast("已复制图片内容");
            } catch {
              toast("复制失败", "error");
            }
          },
        }),
      ),
    ),
  );
}

function buildImageHistorySection() {
  return h("div.image-history-section",
    h("div.gallery-head",
      h("h4", "历史生成记录"),
      h("div.spacer"),
      buttonNode("清空历史", {
        small: true,
        variant: "text",
        onClick: () => {
          state.image.history = [];
          draw();
        },
      }),
    ),
    h("div.image-history-list",
      state.image.history.map((rec) => h("div.image-history-card",
        h("div.history-header",
          h("strong", rec.model),
          badge(rec.size, "muted"),
          rec.duration ? h("span.mono.muted", `${rec.duration}ms`) : null,
          h("div.spacer"),
          buttonNode("使用此提示词", {
            small: true,
            variant: "text",
            onClick: () => {
              state.image.prompt = rec.prompt;
              state.image.selectedModel = rec.model;
              draw();
            },
          }),
          buttonNode("", {
            small: true,
            variant: "text",
            title: "查看入站请求与原始响应",
            iconName: "eye",
            onClick: () => {
              state.inspectPayload = {
                title: `图像生成详情 · ${rec.model}`,
                request: rec.rawPayload,
                response: rec.rawResponse,
                duration: rec.duration,
              };
              draw();
            },
          }),
        ),
        h("p.history-prompt", rec.prompt),
        h("div.history-thumbs-row",
          (rec.images || []).map((imgItem) => {
            const src = imgItem.url || (imgItem.b64_json ? `data:image/png;base64,${imgItem.b64_json}` : "");
            return h("img.history-thumb", {
              src,
              alt: "历史生成缩略图",
              onClick: () => {
                state.previewImage = src;
                draw();
              },
            });
          }),
        ),
      )),
    ),
  );
}

// —— 辅助下载函数 ——

function downloadImage(url, filename) {
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  document.body.removeChild(link);
}

// —— 大图与载荷查看模态框 ——

function buildModals() {
  const modals = [];

  // 图片大图预览
  if (state.previewImage) {
    modals.push(h("div.backdrop.image-preview-backdrop", {
      onClick: (e) => {
        if (e.target.classList.contains("image-preview-backdrop")) {
          state.previewImage = null;
          draw();
        }
      },
    },
      h("div.image-preview-panel",
        h("div.image-preview-toolbar",
          h("span", "图片预览"),
          h("div.spacer"),
          buttonNode("下载", {
            small: true,
            iconName: "download",
            onClick: () => downloadImage(state.previewImage, `amkr-preview-${Date.now()}.png`),
          }),
          buttonNode("关闭", {
            small: true,
            variant: "text",
            iconName: "close",
            onClick: () => {
              state.previewImage = null;
              draw();
            },
          }),
        ),
        h("div.image-preview-img-wrap",
          h("img", { src: state.previewImage, alt: "大图预览" }),
        ),
      ),
    ));
  }

  // 载荷检查弹窗
  if (state.inspectPayload) {
    const item = state.inspectPayload;
    const reqStr = JSON.stringify(item.request || {}, null, 2);
    const resStr = JSON.stringify(item.response || {}, null, 2);

    modals.push(h("div.backdrop.payload-backdrop", {
      onClick: (e) => {
        if (e.target.classList.contains("payload-backdrop")) {
          state.inspectPayload = null;
          draw();
        }
      },
    },
      h("div.dialog.payload-dialog",
        h("div.dialog-head",
          h("h3", item.title || "调用载荷检视"),
          buttonNode("", {
            variant: "text",
            small: true,
            iconName: "close",
            onClick: () => {
              state.inspectPayload = null;
              draw();
            },
          }),
        ),
        h("div.dialog-body.payload-dialog-body",
          item.duration ? h("div.payload-meta-row",
            badge(`总耗时 ${item.duration}ms`, "muted"),
            item.usage?.total_tokens ? badge(`总 Tokens: ${item.usage.total_tokens}`, "muted") : null,
          ) : null,
          h("h4", "入站请求数据 (Request Payload)"),
          h("div.code-block",
            h("div.code-header",
              h("span.code-lang", "JSON"),
              buttonNode("复制请求", {
                small: true,
                variant: "text",
                iconName: "copy",
                onClick: async () => {
                  await copyText(reqStr);
                  toast("已复制请求 JSON");
                },
              }),
            ),
            h("pre", h("code", reqStr)),
          ),
          h("h4", "原始响应数据 (Response Payload)"),
          h("div.code-block",
            h("div.code-header",
              h("span.code-lang", "JSON"),
              buttonNode("复制响应", {
                small: true,
                variant: "text",
                iconName: "copy",
                onClick: async () => {
                  await copyText(resStr);
                  toast("已复制响应 JSON");
                },
              }),
            ),
            h("pre", h("code", resStr)),
          ),
        ),
      ),
    ));
  }

  return modals;
}

// —— 整体界面重绘 ——

function draw() {
  if (!host) return;

  const content = [];
  content.push(buildHeader());

  if (state.loading) {
    content.push(loading("正在读取模型与空间配置…"));
    render(host, content);
    return;
  }

  if (state.error) {
    content.push(notice(`加载配置失败: ${state.error}`, "error"));
  }

  if (!state.models.length && !state.loading) {
    content.push(card(
      cardHead("尚未发现可用模型"),
      empty("当前 AMKR 尚未配置可调用的模型路由或供应商 Key。", {
        icon: "cpu",
        hint: "请先前往「供应商」或「模型路由」添加模型并绑定 Key，之后即可在试验场进行测试。",
        action: buttonNode("前往供应商配置", {
          variant: "primary",
          onClick: () => {
            if (currentCtx?.navigate) currentCtx.navigate("providers");
            else location.hash = "#/providers";
          },
        }),
      }),
    ));
    render(host, content, ...buildModals());
    return;
  }

  if (state.activeTab === "chat") {
    content.push(buildChatView());
  } else {
    content.push(buildImageView());
  }

  content.push(...buildModals());
  render(host, content);
}

// —— 页面导出 ——

export function renderPlayground(context) {
  currentCtx = context;
  host = h("div.stack.playground-page");

  // 页面离开时中止正在进行的请求
  context.onLeave(() => {
    if (state.chat.abortController) {
      state.chat.abortController.abort();
      state.chat.streaming = false;
      state.chat.abortController = null;
    }
    if (state.image.abortController) {
      state.image.abortController.abort();
      state.image.generating = false;
      state.image.abortController = null;
    }
  });

  if (state.loading) {
    draw();
    (async () => {
      await load();
      state.loading = false;
      draw();
    })();
    return host;
  }

  draw();
  return host;
}

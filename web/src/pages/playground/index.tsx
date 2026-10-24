import { useEffect, useMemo, useRef, useState } from "react";
import {
  Braces,
  ChevronDown,
  ChevronRight,
  Eye,
  Image as ImageIcon,
  Loader2,
  PanelLeft,
  Send,
  Sparkles,
  Square,
  Trash2,
  Upload,
} from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { copyText, errorText, formatDateTime } from "@/lib/format";
import { kindLabel, kindOf, normalizeModelKinds, type ModelKindsIndex } from "@/lib/model-kinds";
import type { ImageGenerationRequest } from "@/lib/types";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button, IconButton } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { Empty } from "@/components/ui/Empty";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Panel } from "@/components/ui/Panel";
import { Segmented } from "@/components/ui/Segmented";
import { Select } from "@/components/ui/Select";
import { Sheet } from "@/components/ui/Sheet";
import { Switch } from "@/components/ui/Switch";
import { Textarea } from "@/components/ui/Textarea";
import { Markdown } from "./markdown";
import { PayloadPanel, chatInspectPayload, type InspectPayload } from "./payload";
import { useChatSession, type PlaygroundMessage } from "./use-chat";

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
  { value: "1024x1024", label: "1024 × 1024（正方形 1:1）" },
  { value: "1024x1792", label: "1024 × 1792（手机竖屏 9:16）" },
  { value: "1792x1024", label: "1792 × 1024（桌面横屏 16:9）" },
  { value: "512x512", label: "512 × 512（小图 1:1）" },
];

interface ImageItem {
  url?: string;
  b64_json?: string;
  revised_prompt?: string;
}

interface ImageRecord {
  id: string;
  model: string;
  prompt: string;
  size: string;
  images: ImageItem[];
  duration: number;
  timestamp: string;
  rawPayload: ImageGenerationRequest;
  rawResponse: unknown;
}

function useNarrow(): boolean {
  const [narrow, setNarrow] = useState(() => {
    try { return window.matchMedia("(max-width: 1023px)").matches; } catch { return false; }
  });
  useEffect(() => {
    const media = window.matchMedia?.("(max-width: 1023px)");
    if (!media) return;
    const change = () => setNarrow(media.matches);
    media.addEventListener?.("change", change);
    return () => media.removeEventListener?.("change", change);
  }, []);
  return narrow;
}

function modelOptions(models: string[], kinds: ModelKindsIndex | null) {
  return models.map((id) => {
    const kind = kindOf(kinds, id);
    return { value: id, label: kind && kind !== "unknown" ? `${id}（${kindLabel(kind)}）` : id };
  });
}

function imageSrc(item: ImageItem): string {
  return item.url || (item.b64_json ? `data:image/png;base64,${item.b64_json}` : "");
}

function downloadImage(url: string) {
  const link = document.createElement("a");
  link.href = url;
  link.download = `amkr-generated-${Date.now()}.png`;
  document.body.appendChild(link);
  link.click();
  link.remove();
}

export default function PlaygroundPage() {
  const [tab, setTab] = useState<"chat" | "image">("chat");
  const [models, setModels] = useState<string[]>([]);
  const [kinds, setKinds] = useState<ModelKindsIndex | null>(null);
  const [workspaces, setWorkspaces] = useState<string[]>([]);
  const [workspace, setWorkspace] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [chatModel, setChatModel] = useState("");
  const [imageModel, setImageModel] = useState("");
  const [showLeft, setShowLeft] = useState(true);
  const [showRight, setShowRight] = useState(true);
  const [leftDrawer, setLeftDrawer] = useState(false);
  const [rightDrawer, setRightDrawer] = useState(false);
  const [inspect, setInspect] = useState<InspectPayload | null>(null);
  const [preview, setPreview] = useState<string | null>(null);
  const narrow = useNarrow();
  const scrollRef = useRef<HTMLDivElement>(null);

  const [systemPrompt, setSystemPrompt] = useState("");
  const [stream, setStream] = useState(true);
  const [temperature, setTemperature] = useState(0.7);
  const [topP, setTopP] = useState(1);
  const [maxTokens, setMaxTokens] = useState("");
  const [input, setInput] = useState("");
  const [attached, setAttached] = useState<string[]>([]);
  const [expandedThinking, setExpandedThinking] = useState<Set<string>>(() => new Set());
  const fileRef = useRef<HTMLInputElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const imagePromptRef = useRef<HTMLTextAreaElement>(null);

  const chat = useChatSession(api.chatCompletions);
  const latestMessage = chat.messages[chat.messages.length - 1];

  // 载荷面板跟着最新一次对话走：开始发送时先显示请求体，流式过程中用当前累积
  // 文本刷新，结束后再落到最终内容 + 最后一个 chunk。切到图像页时不覆盖图像载荷。
  useEffect(() => {
    if (tab !== "chat" || !latestMessage || latestMessage.role !== "assistant" || !latestMessage.rawPayload) return;
    setInspect(chatInspectPayload(latestMessage));
  }, [
    tab,
    latestMessage?.rawPayload,
    latestMessage?.content,
    latestMessage?.reasoning,
    latestMessage?.duration,
    latestMessage?.usage,
    latestMessage?.rawResponse,
    latestMessage?.streaming,
  ]);

  const [imagePrompt, setImagePrompt] = useState("");
  const [imageSize, setImageSize] = useState("1024x1024");
  const [imageN, setImageN] = useState(1);
  const [imageQuality, setImageQuality] = useState("standard");
  const [imageFormat, setImageFormat] = useState("url");
  const [generating, setGenerating] = useState(false);
  const [imageResults, setImageResults] = useState<ImageItem[]>([]);
  const [imageHistory, setImageHistory] = useState<ImageRecord[]>([]);
  const imageController = useRef<AbortController | null>(null);

  useEffect(() => () => imageController.current?.abort(), []);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setLoadError(null);
    Promise.all([
      api.v1Models(workspace || undefined).catch(async () => {
        const fallback = await api.models();
        return { data: (fallback.models || []).map((model) => ({ id: model.id })) };
      }),
      api.models().then((response) => {
        const names = (response.models || []).map((model) => model.id).filter(Boolean);
        return api.modelKinds(names).catch(() => null);
      }).catch(() => null),
      api.workspaces().catch(() => ({ workspaces: [] })),
    ]).then(([modelResponse, kindResponse, workspaceResponse]) => {
      if (cancelled) return;
      setKinds(normalizeModelKinds(kindResponse));
      setWorkspaces((workspaceResponse.workspaces || []).map((item) => item.name).filter(Boolean));
      const list = [...new Set((modelResponse.data || []).map((item) => String(item.id || "")).filter(Boolean))];
      setModels(list);
      setChatModel((current) => current && list.includes(current) ? current : (list.find((id) => id === "unified-model") || list.find((id) => kindOf(normalizeModelKinds(kindResponse), id) === "text") || list[0] || ""));
      setImageModel((current) => current && list.includes(current) ? current : (list.find((id) => kindOf(normalizeModelKinds(kindResponse), id) === "image") || list.find((id) => /image|dall|flux/i.test(id)) || list.find((id) => id === "unified-model") || list[0] || ""));
    }).catch((failure) => {
      if (!cancelled) setLoadError(errorText(failure));
    }).finally(() => {
      if (!cancelled) setLoading(false);
    });
    return () => { cancelled = true; };
  }, [workspace]);

  useEffect(() => {
    if (!scrollRef.current) return;
    scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
  }, [chat.messages.length, chat.streaming]);

  function toggleThinking(messageId: string) {
    setExpandedThinking((current) => {
      const next = new Set(current);
      if (next.has(messageId)) next.delete(messageId);
      else next.add(messageId);
      return next;
    });
  }

  function sendChat() {
    if (chat.streaming) return;
    if (!input.trim() && !attached.length) return;
    if (!chatModel) {
      toast.warning("请先选择要测试的模型");
      return;
    }
    const content = input;
    const images = [...attached];
    setInput("");
    setAttached([]);
    void chat.send({
      model: chatModel,
      content,
      images,
      workspace: workspace || undefined,
      systemPrompt,
      temperature,
      topP,
      maxTokens,
      stream,
    });
    if (narrow) setLeftDrawer(false);
  }

  function attachFile(file: File | null) {
    if (!file) return;
    if (!file.type.startsWith("image/")) {
      toast.warning("请选择有效的图片文件");
      return;
    }
    if (file.size > 15 * 1024 * 1024) {
      toast.warning("图片体积不能超过 15MB");
      return;
    }
    const reader = new FileReader();
    reader.onload = () => {
      if (typeof reader.result === "string") setAttached((current) => [...current, reader.result as string]);
    };
    reader.readAsDataURL(file);
  }

  async function generateImage() {
    if (generating) return;
    if (!imagePrompt.trim()) {
      toast.warning("请输入图像描述提示词");
      return;
    }
    if (!imageModel) {
      toast.warning("请先选择图像模型");
      return;
    }
    const payload: ImageGenerationRequest = {
      model: imageModel,
      prompt: imagePrompt.trim(),
      n: imageN,
      size: imageSize,
      quality: imageQuality,
      response_format: imageFormat,
    };
    const controller = new AbortController();
    imageController.current = controller;
    setGenerating(true);
    setImageResults([]);
    setInspect({ title: `图像生成详情 · ${imageModel}`, request: payload, response: null });
    try {
      const result = await api.imageGenerations({ body: payload, workspace: workspace || undefined, signal: controller.signal });
      // 停止后旧请求仍可能返回；不能让它覆盖新一轮生成的结果。
      if (controller.signal.aborted) return;
      const items = result.data as ImageItem[];
      setImageResults(items);
      const record: ImageRecord = {
        id: `img_${Date.now()}`, model: imageModel, prompt: imagePrompt.trim(), size: imageSize,
        images: items, duration: result.duration, timestamp: new Date().toISOString(),
        rawPayload: payload, rawResponse: result.raw,
      };
      setImageHistory((current) => [record, ...current].slice(0, 20));
      setInspect({ title: `图像生成详情 · ${imageModel}`, request: payload, response: result.raw, duration: result.duration });
      toast.success(`成功生成 ${items.length} 张图片（耗时 ${result.duration}ms）`);
    } catch (failure) {
      if (!controller.signal.aborted) toast.error(`生成失败: ${errorText(failure)}`);
    } finally {
      if (imageController.current === controller) {
        imageController.current = null;
        setGenerating(false);
      }
    }
  }

  function stopImage() {
    imageController.current?.abort();
    imageController.current = null;
    setGenerating(false);
  }

  function clearChat() {
    chat.clear();
    setInspect(null);
  }

  function usePrompt(prompt: string) {
    setImagePrompt(prompt);
    if (narrow) setLeftDrawer(false);
    else imagePromptRef.current?.focus();
  }

  const modelSelectOptions = useMemo(() => modelOptions(models, kinds), [models, kinds]);
  const chatMessages = chat.messages;

  const parameters = tab === "chat" ? (
    <div className="space-y-4">
      <Field label="系统提示词 (System Prompt)">
        <Textarea rows={3} value={systemPrompt} onChange={(event) => setSystemPrompt(event.target.value)} placeholder="例如：你是一位资深的高级软件架构师，回答要求言简意赅…" />
      </Field>
      <Switch checked={stream} onCheckedChange={setStream} label="流式输出 (SSE Stream)" description="开启后像打字机一样逐字返回，降低首字延迟。" />
      <div>
        <div className="flex items-center justify-between text-meta"><span className="font-medium text-ink-2">温度 (Temperature)</span><span className="font-mono text-ink-3">{temperature}</span></div>
        <input aria-label="温度" type="range" min="0" max="2" step="0.1" value={temperature} onChange={(event) => setTemperature(Number(event.target.value))} className="mt-2 w-full accent-[var(--accent)]" />
        <p className="mt-1 text-meta text-ink-3">值越高回复越有创造性，值越低越严谨确定。</p>
      </div>
      <div>
        <div className="flex items-center justify-between text-meta"><span className="font-medium text-ink-2">核采样 (Top P)</span><span className="font-mono text-ink-3">{topP}</span></div>
        <input aria-label="Top P" type="range" min="0.1" max="1" step="0.05" value={topP} onChange={(event) => setTopP(Number(event.target.value))} className="mt-2 w-full accent-[var(--accent)]" />
      </div>
      <Field label="最大回复 Tokens">
        <Input type="number" min="1" value={maxTokens} onChange={(event) => setMaxTokens(event.target.value)} placeholder="留空为模型默认上限" />
      </Field>
      <div>
        <div className="mb-2 text-meta font-medium text-ink-2">常用测试预设</div>
        <div className="flex flex-wrap gap-1.5">
          {CHAT_PRESETS.map((preset) => (
            <button key={preset.label} type="button" className="rounded-chip border border-rule bg-sunken px-2 py-1 text-meta text-ink-2 hover:bg-hover hover:text-ink" onClick={() => { setInput(preset.prompt); if (narrow) setLeftDrawer(false); textareaRef.current?.focus(); }}>{preset.label}</button>
          ))}
        </div>
      </div>
    </div>
  ) : (
    <div className="space-y-4">
      <Field label="图像模型">
        <Select aria-label="图像模型" value={imageModel} onChange={(event) => setImageModel(event.target.value)} options={modelSelectOptions} placeholder="选择模型" />
      </Field>
      <Field label="图像提示词 (Prompt)">
        <Textarea ref={imagePromptRef} rows={4} value={imagePrompt} onChange={(event) => setImagePrompt(event.target.value)} placeholder="描述你希望生成的图片内容、风格、光影与细节…" />
      </Field>
      <div>
        <div className="mb-2 text-meta font-medium text-ink-2">预设示例灵感</div>
        <div className="flex flex-wrap gap-1.5">
          {IMAGE_PRESETS.map((preset) => (
            <button key={preset.label} type="button" className="rounded-chip border border-rule bg-sunken px-2 py-1 text-meta text-ink-2 hover:bg-hover hover:text-ink" title={preset.prompt} onClick={() => usePrompt(preset.prompt)}>{preset.label}</button>
          ))}
        </div>
      </div>
      <Field label="图片分辨率 (Size)">
        <Select aria-label="图片分辨率" value={imageSize} onChange={(event) => setImageSize(event.target.value)} options={IMAGE_SIZES} />
      </Field>
      <div className="grid grid-cols-2 gap-2">
        <Field label="生成张数">
          <Select aria-label="生成张数" value={String(imageN)} onChange={(event) => setImageN(Number(event.target.value))} options={[{ value: "1", label: "1 张" }, { value: "2", label: "2 张" }, { value: "4", label: "4 张" }]} />
        </Field>
        <Field label="输出格式">
          <Select aria-label="输出格式" value={imageFormat} onChange={(event) => setImageFormat(event.target.value)} options={[{ value: "url", label: "图片 URL" }, { value: "b64_json", label: "Base64 数据" }]} />
        </Field>
      </div>
      <Field label="质量">
        <Select aria-label="质量" value={imageQuality} onChange={(event) => setImageQuality(event.target.value)} options={[{ value: "standard", label: "标准" }, { value: "hd", label: "高清" }]} />
      </Field>
      {generating ? (
        <Button variant="danger" leadingIcon={<Square aria-hidden className="size-3.5" />} onClick={stopImage}>停止生成</Button>
      ) : (
        <Button variant="primary" leadingIcon={<ImageIcon aria-hidden className="size-4" />} disabled={!imagePrompt.trim()} onClick={() => void generateImage()}>立即生成图片</Button>
      )}
    </div>
  );

  const leftPanel = (
    <Panel title={tab === "chat" ? "推理参数配置" : "图像生成参数"} padding>
      <div className="max-h-[calc(100dvh-16rem)] overflow-y-auto pr-0.5">{parameters}</div>
    </Panel>
  );

  const rightPanel = (
    <Panel title="请求与响应载荷" actions={<IconButton aria-label="关闭载荷" tooltip={null} size="sm" onClick={() => (narrow ? setRightDrawer(false) : setShowRight(false))}><ChevronRight aria-hidden className="size-4" /></IconButton>}>
      <div className="max-h-[calc(100dvh-16rem)] overflow-y-auto pr-0.5"><PayloadPanel inspect={inspect} /></div>
    </Panel>
  );

  const chatCenter = (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col rounded-panel border border-rule bg-panel">
      <div className="flex flex-wrap items-center gap-2 border-b border-rule px-3 py-2.5">
        <span className="text-meta text-ink-3">测试模型</span>
        <Select className="w-auto min-w-48 flex-1 sm:flex-none" aria-label="测试模型" value={chatModel} onChange={(event) => setChatModel(event.target.value)} options={modelSelectOptions} placeholder="选择模型" />
        {chatModel ? <Badge tone="neutral">{chatModel === "unified-model" ? "统一模型" : kindLabel(kindOf(kinds, chatModel))}</Badge> : null}
        <div className="ml-auto flex items-center gap-1">
          <IconButton aria-label="参数面板" tooltip="参数面板" size="sm" onClick={() => narrow ? setLeftDrawer(true) : setShowLeft((value) => !value)}><PanelLeft aria-hidden className="size-4" /></IconButton>
          <IconButton aria-label="载荷面板" tooltip="载荷面板" size="sm" onClick={() => narrow ? setRightDrawer(true) : setShowRight((value) => !value)}><Braces aria-hidden className="size-4" /></IconButton>
          <Button size="sm" variant="ghost" leadingIcon={<Trash2 aria-hidden className="size-3.5" />} disabled={chat.streaming || !chatMessages.length} onClick={clearChat}>清空对话</Button>
        </div>
      </div>
      <div ref={scrollRef} className="min-h-0 flex-1 overflow-y-auto px-3 py-4 sm:px-4">
        {chatMessages.length ? (
          <div className="space-y-4">
            {chatMessages.map((message) => (
              <MessageRow
                key={message.id}
                message={message}
                expanded={expandedThinking.has(message.id)}
                onToggleThinking={() => toggleThinking(message.id)}
                onInspect={() => setInspect(chatInspectPayload(message))}
                onPreview={setPreview}
              />
            ))}
          </div>
        ) : (
          <div className="flex h-full min-h-64 flex-col items-center justify-center text-center">
            <Sparkles aria-hidden className="mb-2 size-9 text-ink-3" />
            <h3 className="text-[16px] font-semibold leading-tight text-ink">开始试用文本模型</h3>
            <p className="mt-1 max-w-md text-meta text-ink-3">选择模型并在下方输入消息，或点击快捷预设提示词快速测试：</p>
            <div className="mt-3 flex flex-wrap justify-center gap-1.5">
              {CHAT_PRESETS.map((preset) => (
                <button key={preset.label} type="button" className="rounded-chip border border-rule bg-sunken px-2 py-1 text-meta text-ink-2 hover:bg-hover hover:text-ink" onClick={() => { setInput(preset.prompt); textareaRef.current?.focus(); }}>{preset.label}</button>
              ))}
            </div>
          </div>
        )}
      </div>
      <div className="border-t border-rule p-3">
        {attached.length ? (
          <div className="mb-2 flex flex-wrap gap-2">
            {attached.map((src, index) => (
              <div key={index} className="relative">
                <img src={src} alt="附件图片" className="size-14 rounded-control border border-rule object-cover" />
                <IconButton aria-label="移除附件图片" tooltip={null} size="sm" className="absolute -right-2 -top-2 size-5 bg-panel" onClick={() => setAttached((current) => current.filter((_, item) => item !== index))}>
                  <ChevronDown aria-hidden className="size-3 rotate-45" />
                </IconButton>
              </div>
            ))}
          </div>
        ) : null}
        <Textarea
          ref={textareaRef}
          rows={2}
          className="min-h-16 resize-none"
          aria-label="消息输入"
          placeholder="输入消息…（Enter 发送，Shift + Enter 换行，支持直接粘贴图片）"
          value={input}
          onChange={(event) => setInput(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
              event.preventDefault();
              sendChat();
            }
          }}
          onPaste={(event) => {
            for (const item of Array.from(event.clipboardData?.items || [])) {
              if (item.type.startsWith("image/")) {
                attachFile(item.getAsFile());
              }
            }
          }}
        />
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <input ref={fileRef} type="file" accept="image/*" className="hidden" onChange={(event) => { attachFile(event.target.files?.[0] || null); event.target.value = ""; }} />
          <Button size="sm" variant="ghost" leadingIcon={<Upload aria-hidden className="size-3.5" />} onClick={() => fileRef.current?.click()}>上传图片</Button>
          <span className="text-meta text-ink-3">支持视觉模型多模态输入</span>
          <div className="ml-auto">
            {chat.streaming ? (
              <Button variant="danger" leadingIcon={<Square aria-hidden className="size-3.5" />} onClick={chat.stop}>停止</Button>
            ) : (
              <Button variant="primary" leadingIcon={<Send aria-hidden className="size-3.5" />} disabled={!input.trim() && !attached.length} onClick={sendChat}>发送</Button>
            )}
          </div>
        </div>
      </div>
    </div>
  );

  const imageCenter = (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col rounded-panel border border-rule bg-panel">
      <div className="flex flex-wrap items-center gap-2 border-b border-rule px-3 py-2.5">
        <h2 className="text-[16px] font-semibold leading-tight text-ink">图像画廊</h2>
        <Badge tone="neutral">{generating ? "生成中" : `${imageResults.length} 张`}</Badge>
        <div className="ml-auto flex items-center gap-1">
          <IconButton aria-label="参数面板" tooltip="参数面板" size="sm" onClick={() => narrow ? setLeftDrawer(true) : setShowLeft((value) => !value)}><PanelLeft aria-hidden className="size-4" /></IconButton>
          <IconButton aria-label="载荷面板" tooltip="载荷面板" size="sm" onClick={() => narrow ? setRightDrawer(true) : setShowRight((value) => !value)}><Braces aria-hidden className="size-4" /></IconButton>
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-3 sm:p-4">
        {generating ? (
          <div className="flex h-full min-h-64 flex-col items-center justify-center text-center">
            <Loader2 aria-hidden className="mb-2 size-8 animate-spin text-ink-3 motion-reduce:animate-none" />
            <h3 className="text-[16px] font-semibold leading-tight text-ink">正在绘制图像中…</h3>
            <p className="mt-1 text-meta text-ink-3">AI 图像模型通常需要 5~20 秒进行扩散渲染，请稍候。</p>
          </div>
        ) : imageResults.length ? (
          <div className="grid gap-3 sm:grid-cols-2">
            {imageResults.map((item, index) => {
              const src = imageSrc(item);
              return (
                <figure key={index} className="overflow-hidden rounded-panel border border-rule bg-sunken/40">
                  <button type="button" className="block w-full" onClick={() => setPreview(src)} aria-label={`查看生成图片 ${index + 1}`}>
                    <img src={src} alt={`生成结果 ${index + 1}`} className="aspect-square w-full object-cover" />
                  </button>
                  <figcaption className="space-y-1 p-2">
                    {item.revised_prompt ? <p className="line-clamp-2 text-meta text-ink-3" title={item.revised_prompt}>优化提示词: {item.revised_prompt}</p> : null}
                    <div className="flex flex-wrap gap-1">
                      <Button size="sm" variant="ghost" leadingIcon={<Eye aria-hidden className="size-3" />} onClick={() => setPreview(src)}>查看大图</Button>
                      <Button size="sm" variant="ghost" onClick={() => downloadImage(src)}>下载</Button>
                    </div>
                  </figcaption>
                </figure>
              );
            })}
          </div>
        ) : (
          <Empty title="试用图像生成模型" description="在左侧选择模型、输入画面描述词并点击「立即生成图片」，生成结果将在此展示。" />
        )}

        {imageHistory.length ? (
          <section className="mt-5">
            <div className="mb-2 flex items-center justify-between gap-2">
              <h3 className="text-[16px] font-semibold leading-tight text-ink">历史生成记录</h3>
              <Button size="sm" variant="ghost" onClick={() => setImageHistory([])}>清空历史</Button>
            </div>
            <div className="space-y-3">
              {imageHistory.map((record) => (
                <div key={record.id} className="rounded-control border border-rule p-2.5">
                  <div className="flex flex-wrap items-center gap-2 text-meta">
                    <strong className="font-mono text-ink-2">{record.model}</strong>
                    <Badge tone="neutral">{record.size}</Badge>
                    <span className="text-ink-3">{record.duration}ms</span>
                    <span className="text-ink-3">{formatDateTime(record.timestamp)}</span>
                    <div className="ml-auto flex flex-wrap gap-1">
                      <Button size="sm" variant="ghost" onClick={() => { setImagePrompt(record.prompt); setImageModel(record.model); }}>使用此提示词</Button>
                      <Button size="sm" variant="ghost" leadingIcon={<Braces aria-hidden className="size-3" />} onClick={() => setInspect({ title: `图像生成详情 · ${record.model}`, request: record.rawPayload, response: record.rawResponse, duration: record.duration })}>载荷</Button>
                    </div>
                  </div>
                  <p className="mt-1 line-clamp-2 text-meta text-ink-3">{record.prompt}</p>
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    {record.images.map((item, index) => {
                      const src = imageSrc(item);
                      return <button key={index} type="button" onClick={() => setPreview(src)} aria-label={`查看历史图片 ${index + 1}`}><img src={src} alt="历史生成缩略图" className="size-14 rounded-control border border-rule object-cover" /></button>;
                    })}
                  </div>
                </div>
              ))}
            </div>
          </section>
        ) : null}
      </div>
    </div>
  );

  return (
    <div className="flex h-[calc(100dvh-7.5rem)] min-h-[560px] flex-col gap-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <p className="max-w-2xl text-[13px] leading-5 text-ink-3">试用并测试已配置的上游模型路由与统一模型，实时检验对话流式响应与图片生成效果。</p>
        <div className="flex flex-wrap items-center gap-2">
          {workspaces.length ? (
            <Select aria-label="工作空间" className="w-44" value={workspace} onChange={(event) => setWorkspace(event.target.value)} options={[{ value: "", label: "默认空间" }, ...workspaces.map((name) => ({ value: name, label: name }))]} />
          ) : null}
          <Segmented aria-label="试验场类型" options={[{ value: "chat", label: "文本对话" }, { value: "image", label: "图像生成" }]} value={tab} onValueChange={(value) => setTab(value as "chat" | "image")} />
        </div>
      </div>

      {loadError ? <Banner tone="critical">加载配置失败: {loadError}</Banner> : null}
      {loading ? (
        <div className="flex flex-1 items-center justify-center rounded-panel border border-rule bg-panel text-meta text-ink-3"><Loader2 aria-hidden className="mr-2 size-4 animate-spin" />正在读取模型与空间配置…</div>
      ) : !models.length ? (
        <Panel title="尚未发现可用模型">
          <Empty title="当前 AMKR 尚未配置可调用的模型路由或供应商 Key。" description="请先前往「供应商」或「模型路由」添加模型并绑定 Key，之后即可在试验场进行测试。" action={<Button variant="primary" onClick={() => { window.location.hash = "#/providers"; }}>前往供应商配置</Button>} />
        </Panel>
      ) : (
        <div className={[
          "grid min-h-0 flex-1 gap-3",
          showLeft && showRight ? "lg:grid-cols-[260px_minmax(0,1fr)_320px]" : showLeft ? "lg:grid-cols-[260px_minmax(0,1fr)]" : showRight ? "lg:grid-cols-[minmax(0,1fr)_320px]" : "lg:grid-cols-1",
        ].join(" ")}>
          {showLeft && !narrow ? <aside className="hidden min-h-0 lg:block">{leftPanel}</aside> : null}
          {tab === "chat" ? chatCenter : imageCenter}
          {showRight && !narrow ? <aside className="hidden min-h-0 lg:block">{rightPanel}</aside> : null}
        </div>
      )}

      <Sheet open={leftDrawer} onOpenChange={setLeftDrawer} title={tab === "chat" ? "推理参数配置" : "图像生成参数"} width="md" footer={<Button variant="secondary" onClick={() => setLeftDrawer(false)}>关闭</Button>}>
        {parameters}
      </Sheet>
      <Sheet open={rightDrawer} onOpenChange={setRightDrawer} title="请求与响应载荷" width="lg" footer={<Button variant="secondary" onClick={() => setRightDrawer(false)}>关闭</Button>}>
        <PayloadPanel inspect={inspect} />
      </Sheet>

      <Dialog open={Boolean(preview)} onOpenChange={(open) => { if (!open) setPreview(null); }} title="图片预览" size="lg" footer={<><Button variant="secondary" onClick={() => setPreview(null)}>关闭</Button><Button variant="primary" onClick={() => preview && downloadImage(preview)}>下载</Button></>}>
        {preview ? <img src={preview} alt="大图预览" className="max-h-[70vh] w-full object-contain" /> : null}
      </Dialog>
    </div>
  );
}

function MessageRow({
  message, expanded, onToggleThinking, onInspect, onPreview,
}: {
  message: PlaygroundMessage;
  expanded: boolean;
  onToggleThinking: () => void;
  onInspect: () => void;
  onPreview: (src: string) => void;
}) {
  const isUser = message.role === "user";
  return (
    <article className={isUser ? "ml-auto max-w-[88%] rounded-panel border border-rule bg-select px-3 py-2.5" : "max-w-[95%] px-1 py-1"}>
      <div className="mb-1 flex flex-wrap items-center gap-2 text-meta">
        <span className="font-medium text-ink-2">{isUser ? "你 (User)" : "助手"}</span>
        {!isUser && message.duration ? <span className="text-ink-3">耗时 {message.duration}ms{message.ttft ? ` · 首字 ${message.ttft}ms` : ""}</span> : null}
        {!isUser && message.usage?.total_tokens ? <span className="text-ink-3">· {message.usage.total_tokens} tokens</span> : null}
        <span className="ml-auto flex items-center gap-0.5">
          {message.rawPayload ? <IconButton aria-label="查看载荷" tooltip="查看载荷" size="sm" onClick={onInspect}><Eye aria-hidden className="size-3.5" /></IconButton> : null}
          <IconButton aria-label="复制消息" tooltip="复制消息" size="sm" onClick={() => void copyText(message.content).then(() => toast.success("已复制消息内容")).catch(() => toast.error("复制失败"))}><ChevronDown aria-hidden className="size-3.5 rotate-45" /></IconButton>
        </span>
      </div>
      {isUser && message.images?.length ? <div className="mb-2 flex flex-wrap gap-1.5">{message.images.map((src, index) => <button key={index} type="button" onClick={() => onPreview(src)}><img src={src} alt="附带图片" className="size-16 rounded-control border border-rule object-cover" /></button>)}</div> : null}
      {isUser ? <p className="whitespace-pre-wrap break-words text-body text-ink">{message.content}</p> : null}
      {!isUser && message.reasoning ? (
        <div className="mb-2 rounded-control border border-rule bg-sunken/60">
          <button type="button" className="flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left text-meta text-ink-2" onClick={onToggleThinking}>
            <Sparkles aria-hidden className="size-3.5" />
            <span>{message.streaming && !message.content ? "正在深入思考中…" : "深度思考过程"}</span>
            {message.streaming && !message.content ? <Loader2 aria-hidden className="size-3 animate-spin" /> : null}
            <ChevronRight aria-hidden className={`ml-auto size-3.5 transition-transform ${expanded ? "rotate-90" : ""}`} />
          </button>
          {expanded ? <pre className="max-h-56 overflow-auto border-t border-rule px-2.5 py-2 whitespace-pre-wrap break-words font-sans text-meta text-ink-3">{message.reasoning}</pre> : null}
        </div>
      ) : null}
      {!isUser && message.content ? <Markdown text={message.content} /> : null}
      {!isUser && message.streaming && !message.reasoning && !message.content ? <div className="flex items-center gap-2 text-meta text-ink-3"><Loader2 aria-hidden className="size-3.5 animate-spin" />正在生成回复…</div> : null}
      {!isUser && message.error ? <Banner tone="critical" className="mt-2">模型调用出错: {message.error}</Banner> : null}
    </article>
  );
}

// 模型类型展示口径：从 legacy 的 webui_model_kinds_probe.mjs 搬过来。
//
// 判定在服务端，前端只做三件事：把读数折成索引、按主标签分组、把名字切块问回去，
// 外加把 endpoints 折成"该走哪条路径"的提示。这里不重复断言后端规则表。

import { describe, expect, it } from "vitest";
import * as m from "./model-kinds";

const document = {
  version: 1,
  source: "https://models.dev/api.json",
  catalog_available: true,
  models: {
    "gpt-4o": { kinds: ["text"], primary: "text", endpoints: ["chat"], grounds: [] },
    "gpt-image-1": { kinds: ["image"], primary: "image", endpoints: ["images"], grounds: [] },
    "gpt-4o-audio-preview": { kinds: ["text", "tts", "stt"], primary: "text", endpoints: ["chat", "speech", "transcriptions"], grounds: [] },
    "gateway-renamed-model": { kinds: [], primary: "unknown", endpoints: [], grounds: [] },
  },
};

describe("载荷归一化", () => {
  it("保留全部模型、主标签与目录可用性", () => {
    const index = m.normalizeModelKinds(document);
    expect(index).not.toBeNull();
    expect(index?.models.size).toBe(4);
    expect(index?.available).toBe(true);
    expect(index?.models.get("gpt-image-1")?.primary).toBe("image");
  });

  it("形状不符返回 null，空模型表仍合法", () => {
    expect(m.normalizeModelKinds(null)).toBeNull();
    expect(m.normalizeModelKinds({ version: 1 })).toBeNull();
    expect(m.normalizeModelKinds({ models: "nope" })).toBeNull();
    expect(m.normalizeModelKinds({ models: {} })?.models.size).toBe(0);
    expect(m.normalizeModelKinds({ models: {} })?.available).toBe(false);
  });
});

describe("主标签", () => {
  const index = m.normalizeModelKinds(document);

  it("只认 primary，不把多能力模型摊成多组", () => {
    expect(m.kindOf(index, "gpt-image-1")).toBe("image");
    expect(m.kindOf(index, "gpt-4o-audio-preview")).toBe("text");
    expect(m.kindOf(index, "gateway-renamed-model")).toBe(m.UNKNOWN_KIND);
    expect(m.kindOf(index, "never-asked")).toBe(m.UNKNOWN_KIND);
    expect(m.kindOf(null, "gpt-4o")).toBe(m.UNKNOWN_KIND);
    expect(m.kindOf(index, null)).toBe(m.UNKNOWN_KIND);
  });

  it("生词类型原样透传而不是折进未分类", () => {
    const future = m.normalizeModelKinds({ models: { "x-model": { kinds: ["hologram"], primary: "hologram" } } });
    expect(m.kindOf(future, "x-model")).toBe("hologram");
  });
});

describe("分组与计数", () => {
  const index = m.normalizeModelKinds(document);
  const names = ["gpt-4o", "gpt-image-1", "gpt-4o-audio-preview", "gateway-renamed-model", "never-asked"];

  it("覆盖每个名字且不重复", () => {
    const groups = m.groupByKind(names, index);
    expect(groups.reduce((total, group) => total + group.names.length, 0)).toBe(names.length);
    expect(new Set(groups.flatMap((group) => group.names)).size).toBe(names.length);
    expect(groups.map((group) => group.kind).join(",")).toBe("text,image,unknown");
    expect(groups[0].label).toBe("文本");
    expect(groups[2].label).toBe("未分类");
    expect(groups.at(-1)?.kind).toBe(m.UNKNOWN_KIND);
    expect(m.groupByKind([], index)).toHaveLength(0);
    expect(m.groupByKind(["a", "b"], null)).toHaveLength(1);
    expect(m.groupByKind(["a", "b"], null)[0].kind).toBe(m.UNKNOWN_KIND);
  });

  it("生词类型排在已知之后、未分类之前", () => {
    const mixed = m.groupByKind(["x-model", "gateway-renamed-model", "gpt-4o"], m.normalizeModelKinds({
      models: {
        "x-model": { kinds: ["hologram"], primary: "hologram" },
        "gateway-renamed-model": { kinds: [], primary: "unknown" },
        "gpt-4o": { kinds: ["text"], primary: "text" },
      },
    }));
    expect(mixed.map((group) => group.kind).join(",")).toBe("text,hologram,unknown");
  });

  it("按组计数", () => {
    const counts = m.groupCounts(names, index);
    expect(counts.get("text")).toBe(2);
    expect(counts.get("image")).toBe(1);
    expect(counts.get("unknown")).toBe(2);
  });
});

describe("请求切块", () => {
  it("默认 100 一组，保留顺序且无空尾", () => {
    const long = Array.from({ length: 250 }, (_, index) => `model-${index}`);
    const chunks = m.chunkModelIDs(long);
    expect(chunks).toHaveLength(3);
    expect(chunks[0]).toHaveLength(100);
    expect(chunks[1]).toHaveLength(100);
    expect(chunks[2]).toHaveLength(50);
    expect(chunks.flat().join(",")).toBe(long.join(","));
    expect(m.chunkModelIDs(["a", "b"], 2)).toHaveLength(1);
    expect(m.chunkModelIDs([])).toHaveLength(0);
    expect(m.chunkModelIDs(null)).toHaveLength(0);
    expect(m.chunkModelIDs(["a", "b"], 0)[0]).toHaveLength(2);
  });
});

describe("标签与端点", () => {
  const index = m.normalizeModelKinds(document);

  it("已知类型给中文，生词原样显示", () => {
    expect(m.kindLabel("tts")).toBe("语音合成");
    expect(m.kindLabel(m.UNKNOWN_KIND)).toBe("未分类");
    expect(m.kindLabel("hologram")).toBe("hologram");
    expect(m.kindLabel(null)).toBe("");
  });

  it("端点标签、顺序与完整路径一致", () => {
    expect(m.ENDPOINT_LABELS.chat).toBe("对话");
    expect(m.ENDPOINT_LABELS.images).toBe("图像生成");
    expect(m.ENDPOINT_LABELS.embeddings).toBe("文本嵌入");
    expect(m.ENDPOINT_LABELS.speech).toBe("语音合成");
    expect(m.ENDPOINT_LABELS.transcriptions).toBe("语音识别");
    expect(m.ENDPOINT_LABELS.video).toBe("视频生成");
    expect(m.ENDPOINT_LABELS.rerank).toBe("重排");
    expect(m.ENDPOINT_ORDER.every((id) => Boolean(m.ENDPOINT_LABELS[id]))).toBe(true);
    expect(Object.keys(m.ENDPOINT_LABELS)).toHaveLength(m.ENDPOINT_ORDER.length);
    expect(m.ENDPOINT_ORDER.every((id) => String(m.ENDPOINT_PATHS[id] || "").startsWith("/v1/"))).toBe(true);
    expect(m.endpointLabel("hologram")).toBe("hologram");
    expect(m.endpointLabel(null)).toBe("");
    expect(m.endpointPath("hologram")).toBe("");
  });

  it("类型到路由模式的映射覆盖对话方言与两条语音模式", () => {
    expect(m.ENDPOINT_FAMILY.image.join(",")).toBe("images");
    expect(m.ENDPOINT_FAMILY.stt.join(",")).toBe("transcriptions,translations");
    expect(m.ENDPOINT_FAMILY.text.join(",")).toBe("openai,anthropic,responses");
    expect(m.ENDPOINT_FAMILY[m.UNKNOWN_KIND]).toHaveLength(0);
  });

  it("逐模型端点去重、排序、未知留空", () => {
    expect(m.endpointsOf(index, "gpt-image-1").join(",")).toBe("images");
    expect(m.endpointsOf(index, "gpt-4o-audio-preview").join(",")).toBe("chat,speech,transcriptions");
    expect(m.endpointLabelsOf(index, "gpt-4o-audio-preview").join(",")).toBe("对话,语音合成,语音识别");
    expect(m.endpointsOf(index, "gateway-renamed-model")).toHaveLength(0);
    expect(m.endpointsOf(index, "never-asked")).toHaveLength(0);
    expect(m.endpointsOf(null, "gpt-4o")).toHaveLength(0);
    expect(m.endpointsOf(index, null)).toHaveLength(0);
    expect(m.endpointLabelsOf(index, "gateway-renamed-model")).toHaveLength(0);

    const shuffled = m.normalizeModelKinds({
      models: {
        "shuffled-model": {
          kinds: ["rerank"], primary: "rerank",
          endpoints: ["rerank", "chat", "rerank", "images", "hologram"],
        },
      },
    });
    expect(m.endpointsOf(shuffled, "shuffled-model").join(",")).toBe("chat,images,rerank,hologram");
    const broken = m.normalizeModelKinds({ models: { "odd-model": { kinds: ["text"], primary: "text", endpoints: "chat" } } });
    expect(m.endpointsOf(broken, "odd-model")).toHaveLength(0);
    expect(index?.models.get("gpt-4o-audio-preview")?.endpoints).toHaveLength(3);
  });
});

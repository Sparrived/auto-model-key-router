import { afterEach, describe, expect, it } from "vitest";
import { navigate, parseHash, setParams } from "./router";

function setHash(hash: string): void {
  window.history.replaceState(null, "", `/${hash}`);
}

afterEach(() => {
  window.history.replaceState(null, "", "/");
});

describe("router", () => {
  it("解析页面与查询参数", () => {
    const route = parseHash("#/providers?x=1&id=openai");
    expect(route.page).toBe("providers");
    expect(route.params.get("x")).toBe("1");
    expect(route.params.get("id")).toBe("openai");
  });

  it("把 #/usage 识别为 activity", () => {
    expect(parseHash("#/usage?hours=24").page).toBe("activity");
  });

  it("未知页面回落到 overview", () => {
    expect(parseHash("#/does-not-exist").page).toBe("overview");
    expect(parseHash("#/").page).toBe("overview");
    expect(parseHash("#/%E0%A4%A").page).toBe("overview");
  });

  it("监控页之间切换时保留筛选参数", () => {
    setHash("#/overview?provider_id=p1&model_id=m1");
    navigate("activity");
    expect(window.location.hash).toBe("#/activity?provider_id=p1&model_id=m1");
  });

  it("从配置页进入监控页时不携带配置参数", () => {
    setHash("#/providers?id=openai");
    navigate("overview");
    expect(window.location.hash).toBe("#/overview");
  });

  it("显式参数覆盖保留的查询参数", () => {
    setHash("#/overview?provider_id=p1");
    navigate("status", { tab: "matrix" });
    expect(window.location.hash).toBe("#/status?tab=matrix");
  });

  it("setParams 可修改当前页参数", () => {
    setHash("#/activity?hours=24");
    setParams((params) => {
      params.set("hours", "168");
    });
    expect(window.location.hash).toBe("#/activity?hours=168");
  });
});

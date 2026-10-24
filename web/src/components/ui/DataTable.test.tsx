import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { DataTable, type DataTableColumn } from "./DataTable";

interface Row {
  id: string;
  name: string;
  count: number;
}

// 原始顺序刻意与数值顺序不一致，才能看出第三次点击是否真的回到原序。
const rows: Row[] = [
  { id: "1", name: "乙", count: 3 },
  { id: "2", name: "甲", count: 1 },
  { id: "3", name: "丙", count: 2 },
];

const columns: DataTableColumn<Row>[] = [
  {
    key: "count",
    header: "数量",
    align: "right",
    tabular: true,
    cell: (row) => row.count,
    sortValue: (row) => row.count,
  },
  {
    key: "name",
    header: "名称",
    cell: (row) => row.name,
    sortValue: (row) => row.name,
  },
];

function renderTable() {
  const user = userEvent.setup();
  render(<DataTable aria-label="模型列表" rows={rows} columns={columns} rowKey={(row) => row.id} />);
  return { user };
}

// 行文本按单元格拼接（数量在前、名称在后），用来断言实际渲染顺序。
function rowTexts(): string[] {
  const table = screen.getByRole("table", { name: "模型列表" });
  const [, ...bodyRows] = within(table).getAllByRole("row");
  return bodyRows.map((row) => row.textContent ?? "");
}

describe("DataTable", () => {
  it("点击数值表头按 asc → desc → none 循环，aria-sort 与行顺序同步", async () => {
    const { user } = renderTable();
    const countHeader = screen.getByRole("columnheader", { name: "数量" });
    const countSortButton = within(countHeader).getByRole("button");

    expect(countHeader).toHaveAttribute("aria-sort", "none");
    expect(rowTexts()).toEqual(["3乙", "1甲", "2丙"]);

    // 第一次：升序。
    await user.click(countSortButton);
    expect(countHeader).toHaveAttribute("aria-sort", "ascending");
    expect(rowTexts()).toEqual(["1甲", "2丙", "3乙"]);

    // 第二次：降序。
    await user.click(countSortButton);
    expect(countHeader).toHaveAttribute("aria-sort", "descending");
    expect(rowTexts()).toEqual(["3乙", "2丙", "1甲"]);

    // 第三次：取消排序，回到原始行序。
    await user.click(countSortButton);
    expect(countHeader).toHaveAttribute("aria-sort", "none");
    expect(rowTexts()).toEqual(["3乙", "1甲", "2丙"]);
  });

  it("切换到文本列排序时，数值列让出 aria-sort", async () => {
    const { user } = renderTable();
    const countHeader = screen.getByRole("columnheader", { name: "数量" });
    const nameHeader = screen.getByRole("columnheader", { name: "名称" });

    await user.click(within(countHeader).getByRole("button"));
    expect(countHeader).toHaveAttribute("aria-sort", "ascending");

    await user.click(within(nameHeader).getByRole("button"));
    expect(nameHeader).toHaveAttribute("aria-sort", "ascending");
    expect(countHeader).toHaveAttribute("aria-sort", "none");
    // 中文按 localeCompare 排序：丙、甲、乙。
    expect(rowTexts()).toEqual(["2丙", "1甲", "3乙"]);
  });
});

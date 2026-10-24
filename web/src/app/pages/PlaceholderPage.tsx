import { Construction } from "lucide-react";
import { Empty } from "@/components/ui/Empty";

// 业务页面的统一占位：T1b 只验证外壳与路由，页面内容在后续任务逐页替换。
export function PlaceholderPage({ title }: { title: string }) {
  return (
    <div className="flex min-h-[360px] items-center justify-center">
      <Empty
        icon={<Construction className="size-7" aria-hidden />}
        title={title}
        description="本页正在重建，稍后会在这里显示完整内容。"
      />
    </div>
  );
}

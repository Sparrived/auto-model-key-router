import { Component, type ErrorInfo, type ReactNode } from "react";
import { OctagonAlert } from "lucide-react";
import { Button } from "@/components/ui/Button";
import { Banner } from "@/components/ui/Banner";

interface ErrorBoundaryProps {
  children: ReactNode;
  title?: string;
}

interface ErrorBoundaryState {
  error: Error | null;
}

// 一页崩溃不能白屏整个控制台：边界只接住内容区，侧栏、顶栏与其它页仍然可用。
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    console.error("page render failed", error, info.componentStack);
  }

  private reload = (): void => {
    this.setState({ error: null });
  };

  render(): ReactNode {
    if (!this.state.error) return this.props.children;
    return (
      <div className="space-y-3">
        <Banner tone="critical" icon={<OctagonAlert aria-hidden />}>
          {this.props.title ?? "本页"}渲染失败：{this.state.error.message}
        </Banner>
        <Button variant="secondary" onClick={this.reload}>
          重新加载此页
        </Button>
      </div>
    );
  }
}

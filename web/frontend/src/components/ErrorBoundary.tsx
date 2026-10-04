/**
 * 错误边界。
 *
 * 作用：任何未捕获的渲染异常都只影响局部，而不是整页白屏。
 * 生产环境下不展示堆栈，只提供重试入口（细节通过浏览器控制台定位）。
 */

import { Component, type ErrorInfo, type ReactNode } from "react"
import { AlertTriangle } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"

interface ErrorBoundaryProps {
  children: ReactNode
}

interface ErrorBoundaryState {
  error: Error | null
}

export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null }

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // 只在浏览器控制台输出，便于开发与排障；不会发送到服务端。
    console.error("界面渲染异常：", error, info.componentStack)
  }

  private handleReset = () => {
    this.setState({ error: null })
  }

  render() {
    const { error } = this.state
    if (!error) {
      return this.props.children
    }

    return (
      <div className="flex min-h-svh items-center justify-center p-6">
        <Card className="w-full max-w-md">
          <CardContent className="space-y-4 py-10 text-center">
            <AlertTriangle className="text-destructive mx-auto size-7" />
            <div className="space-y-1">
              <p className="font-medium">界面渲染出错</p>
              <p className="text-muted-foreground text-sm break-words">{error.message}</p>
            </div>
            <div className="flex justify-center gap-2">
              <Button variant="outline" onClick={this.handleReset}>
                重试
              </Button>
              <Button onClick={() => window.location.reload()}>刷新页面</Button>
            </div>
          </CardContent>
        </Card>
      </div>
    )
  }
}

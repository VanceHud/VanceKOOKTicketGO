/**
 * AI 知识库（仅管理员）：自动应答检索用的「问题 → 回答」条目管理。
 *
 * 条目来源：
 *  - ticket：人工客服在工单内的回复被自动捕获（学习闭环，只读）；
 *  - manual：本页手动录入的 FAQ。
 *
 * 附「检索测试」：输入一句话预览当前会命中的条目，验证检索效果。
 */

import { useState } from "react"
import { BookOpen, Pencil, Plus, Search, Trash2 } from "lucide-react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import { PageHeader } from "@/components/PageHeader"
import { EmptyState, ErrorState, InlineLoader } from "@/components/StateViews"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { api } from "@/lib/api"
import { formatDateTime } from "@/lib/format"
import { useAIKnowledge } from "@/lib/queries"
import { toastError, toastSuccess } from "@/lib/toast"
import type { AIKnowledge } from "@/lib/types"

export function KnowledgePage() {
  const { t, i18n } = useTranslation()
  const queryClient = useQueryClient()
  const [query, setQuery] = useState("")
  const [source, setSource] = useState("all")
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<AIKnowledge | null>(null)
  const [deleting, setDeleting] = useState<AIKnowledge | null>(null)
  const [searchPreview, setSearchPreview] = useState<string>("")
  const [previewHits, setPreviewHits] = useState<AIKnowledge[] | null>(null)

  const listQuery = useAIKnowledge({
    q: query || undefined,
    source: source === "all" ? undefined : source,
    page: 1,
    pageSize: 50,
  })

  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ["ai-knowledge"] })

  const createMutation = useMutation({
    mutationFn: (payload: Record<string, unknown>) => api.post<AIKnowledge>("/ai/knowledge", payload),
    onSuccess: () => {
      toastSuccess(t("knowledge.createSuccess"))
      setCreating(false)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const updateMutation = useMutation({
    mutationFn: (payload: { id: number; body: Record<string, unknown> }) =>
      api.patch<AIKnowledge>(`/ai/knowledge/${payload.id}`, payload.body),
    onSuccess: () => {
      toastSuccess(t("knowledge.updateSuccess"))
      setEditing(null)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const toggleMutation = useMutation({
    mutationFn: (payload: { id: number; enabled: boolean }) =>
      api.patch(`/ai/knowledge/${payload.id}`, { enabled: payload.enabled }),
    onSuccess: () => invalidate(),
    onError: (error) => toastError(error),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.del(`/ai/knowledge/${id}`),
    onSuccess: () => {
      toastSuccess(t("knowledge.deleteSuccess"))
      setDeleting(null)
      invalidate()
    },
    onError: (error) => toastError(error),
  })

  const previewMutation = useMutation({
    mutationFn: (text: string) => api.post<{ items: AIKnowledge[] }>("/ai/knowledge/search", { query: text }),
    onSuccess: (data) => setPreviewHits(data.items ?? []),
    onError: (error) => toastError(error),
  })

  return (
    <div className="space-y-5">
      <PageHeader title={t("knowledge.title")} description={t("knowledge.description")}>
        <Button size="sm" onClick={() => setCreating(true)}>
          <Plus className="size-4" />
          {t("knowledge.createButton")}
        </Button>
      </PageHeader>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("knowledge.searchPreviewTitle")}</CardTitle>
          <CardDescription>{t("knowledge.searchPreviewHint")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex gap-2">
            <Input
              placeholder={t("knowledge.searchPreviewPlaceholder")}
              value={searchPreview}
              onChange={(event) => setSearchPreview(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter" && searchPreview.trim()) {
                  previewMutation.mutate(searchPreview.trim())
                }
              }}
            />
            <Button
              variant="outline"
              disabled={previewMutation.isPending || !searchPreview.trim()}
              onClick={() => previewMutation.mutate(searchPreview.trim())}
            >
              <Search className="size-4" />
              {t("knowledge.searchButton")}
            </Button>
          </div>
          {previewHits ? (
            previewHits.length === 0 ? (
              <p className="text-muted-foreground text-xs">{t("knowledge.searchPreviewEmpty")}</p>
            ) : (
              <ul className="space-y-2">
                {previewHits.map((hit) => (
                  <li key={hit.id} className="bg-muted/40 space-y-1 rounded-lg border p-3 text-sm">
                    <p className="break-words whitespace-pre-wrap">{hit.question}</p>
                    <p className="text-muted-foreground break-words whitespace-pre-wrap">{hit.answer}</p>
                  </li>
                ))}
              </ul>
            )
          ) : null}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("knowledge.listTitle")}</CardTitle>
          <CardDescription>{t("knowledge.listHint")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex flex-wrap items-center gap-2">
            <Input
              className="max-w-xs"
              placeholder={t("knowledge.filterPlaceholder")}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
            <Select value={source} onValueChange={setSource}>
              <SelectTrigger className="w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t("knowledge.sourceAll")}</SelectItem>
                <SelectItem value="ticket">{t("knowledge.sourceTicket")}</SelectItem>
                <SelectItem value="manual">{t("knowledge.sourceManual")}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {listQuery.isPending ? (
            <InlineLoader />
          ) : listQuery.isError ? (
            <ErrorState error={listQuery.error} onRetry={() => void listQuery.refetch()} />
          ) : (listQuery.data?.items.length ?? 0) === 0 ? (
            <EmptyState title={t("knowledge.empty")} className="py-10" />
          ) : (
            <ul className="space-y-3">
              {listQuery.data?.items.map((item) => (
                <KnowledgeRow
                  key={item.id}
                  item={item}
                  language={i18n.language}
                  onToggle={(enabled) => toggleMutation.mutate({ id: item.id, enabled })}
                  onEdit={() => setEditing(item)}
                  onDelete={() => setDeleting(item)}
                />
              ))}
            </ul>
          )}
        </CardContent>
      </Card>

      <KnowledgeDialog
        open={creating}
        onOpenChange={setCreating}
        title={t("knowledge.createTitle")}
        submitLabel={t("common.create")}
        pending={createMutation.isPending}
        onSubmit={(question, answer) => createMutation.mutate({ question, answer })}
      />

      {editing ? (
        <KnowledgeDialog
          open
          onOpenChange={(open) => {
            if (!open) setEditing(null)
          }}
          title={t("knowledge.editTitle")}
          submitLabel={t("common.save")}
          pending={updateMutation.isPending}
          initialQuestion={editing.question}
          initialAnswer={editing.answer}
          onSubmit={(question, answer) => updateMutation.mutate({ id: editing.id, body: { question, answer } })}
        />
      ) : null}

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => setDeleting(open ? deleting : null)}
        title={t("knowledge.deleteTitle")}
        description={t("knowledge.deleteDesc")}
        confirmLabel={t("common.delete")}
        destructive
        pending={deleteMutation.isPending}
        onConfirm={() => deleting && deleteMutation.mutate(deleting.id)}
      />
    </div>
  )
}

function KnowledgeRow({
  item,
  language,
  onToggle,
  onEdit,
  onDelete,
}: {
  item: AIKnowledge
  language: string
  onToggle: (enabled: boolean) => void
  onEdit: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  return (
    <li className="bg-muted/40 space-y-2 rounded-lg border p-4">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <Badge variant={item.source === "manual" ? "secondary" : "outline"} className="h-4 px-1.5 text-[10px]">
          {item.source === "manual" ? t("knowledge.sourceManual") : t("knowledge.sourceTicket")}
        </Badge>
        {item.isCorrection ? (
          <Badge variant="outline" className="h-4 px-1.5 text-[10px] text-amber-600">
            {t("knowledge.correctionBadge")}
          </Badge>
        ) : null}
        {item.ticketNo ? (
          <span className="text-muted-foreground font-mono text-[10px]">{item.ticketNo}</span>
        ) : null}
        <span className="text-muted-foreground" title={formatDateTime(item.createdAt, language)}>
          {formatDateTime(item.createdAt, language)}
        </span>
        <div className="ml-auto flex items-center gap-2">
          <Switch checked={item.enabled} onCheckedChange={onToggle} aria-label={t("knowledge.toggleLabel")} />
          <Button variant="ghost" size="icon" className="size-7" onClick={onEdit}>
            <Pencil className="size-3.5" />
          </Button>
          <Button variant="ghost" size="icon" className="text-destructive size-7" onClick={onDelete}>
            <Trash2 className="size-3.5" />
          </Button>
        </div>
      </div>
      <p className="text-sm break-words whitespace-pre-wrap">{item.question}</p>
      <p className="text-muted-foreground text-sm break-words whitespace-pre-wrap">{item.answer}</p>
    </li>
  )
}

function KnowledgeDialog({
  open,
  onOpenChange,
  title,
  submitLabel,
  pending,
  initialQuestion,
  initialAnswer,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  submitLabel: string
  pending: boolean
  initialQuestion?: string
  initialAnswer?: string
  onSubmit: (question: string, answer: string) => void
}) {
  const { t } = useTranslation()
  const [question, setQuestion] = useState(initialQuestion ?? "")
  const [answer, setAnswer] = useState(initialAnswer ?? "")

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <BookOpen className="size-4" />
            {title}
          </DialogTitle>
          <DialogDescription>{t("knowledge.dialogHint")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="knowledge-question">{t("knowledge.questionLabel")}</Label>
            <Textarea
              id="knowledge-question"
              rows={4}
              maxLength={2000}
              value={question}
              onChange={(event) => setQuestion(event.target.value)}
              placeholder={t("knowledge.questionPlaceholder")}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="knowledge-answer">{t("knowledge.answerLabel")}</Label>
            <Textarea
              id="knowledge-answer"
              rows={4}
              maxLength={2000}
              value={answer}
              onChange={(event) => setAnswer(event.target.value)}
              placeholder={t("knowledge.answerPlaceholder")}
            />
          </div>
        </div>
        <DialogFooter>
          <Button
            disabled={pending || question.trim().length === 0 || answer.trim().length === 0}
            onClick={() => onSubmit(question.trim(), answer.trim())}
          >
            {pending ? t("common.saving") : submitLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/**
 * AI 设置（仅管理员）：LLM 连接、自动应答规则与护栏参数。
 *
 * 表单约定与系统设置页一致：
 *  - API Key 为只写字段，留空表示不修改（响应只回掩码）；
 *  - 保存后可「测试连接」验证凭据与模型可用性；
 *  - 全局开关默认关闭，开启前要求 Key 与模型名已配置。
 */

import { useEffect, useState } from "react"
import { zodResolver } from "@hookform/resolvers/zod"
import { Bot, CheckCircle2, FlaskConical, ShieldAlert } from "lucide-react"
import { useForm } from "react-hook-form"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"
import { z } from "zod"

import { PageHeader } from "@/components/PageHeader"
import { ErrorState, InlineLoader } from "@/components/StateViews"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { api } from "@/lib/api"
import { queryKeys, useAISettings } from "@/lib/queries"
import { toastError, toastSuccess } from "@/lib/toast"

const aiSettingsSchema = z.object({
  enabled: z.boolean(),
  baseUrl: z
    .string()
    .refine((value) => value === "" || /^https?:\/\//.test(value), { message: "invalidUrl" }),
  model: z.string().max(128),
  embeddingModel: z.string().max(128),
  apiKey: z.string().optional(),
  temperature: z.number({ message: "invalidNumber" }).min(0).max(2),
  systemPrompt: z.string().max(4000),
  footer: z.string().max(200),
  quietSeconds: z.number({ message: "invalidNumber" }).int().min(1).max(120),
  maxReplies: z.number({ message: "invalidNumber" }).int().min(1).max(100),
  contextMessages: z.number({ message: "invalidNumber" }).int().min(5).max(100),
  knowledgeTopK: z.number({ message: "invalidNumber" }).int().min(1).max(20),
  autoSummaryOnClose: z.boolean(),
  callRetentionDays: z.number({ message: "invalidNumber" }).int().min(0).max(3650),
})

type AISettingsFormValues = z.infer<typeof aiSettingsSchema>

export function AISettingsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const settingsQuery = useAISettings()
  const [testResult, setTestResult] = useState<{ ok: boolean; message?: string } | null>(null)

  const form = useForm<AISettingsFormValues>({
    resolver: zodResolver(aiSettingsSchema),
    defaultValues: {
      enabled: false,
      baseUrl: "",
      model: "",
      embeddingModel: "",
      apiKey: "",
      temperature: 0.7,
      systemPrompt: "",
      footer: "",
      quietSeconds: 5,
      maxReplies: 10,
      contextMessages: 20,
      knowledgeTopK: 5,
      autoSummaryOnClose: false,
      callRetentionDays: 90,
    },
  })

  useEffect(() => {
    const data = settingsQuery.data
    if (!data) return
    form.reset({
      enabled: data.enabled,
      baseUrl: data.baseUrl ?? "",
      model: data.model ?? "",
      embeddingModel: data.embeddingModel ?? "",
      apiKey: "",
      temperature: data.temperature ?? 0.7,
      systemPrompt: data.systemPrompt ?? "",
      footer: data.footer ?? "",
      quietSeconds: data.quietSeconds ?? 5,
      maxReplies: data.maxReplies ?? 10,
      contextMessages: data.contextMessages ?? 20,
      knowledgeTopK: data.knowledgeTopK ?? 5,
      autoSummaryOnClose: data.autoSummaryOnClose ?? false,
      callRetentionDays: data.callRetentionDays ?? 90,
    })
  }, [settingsQuery.data, form])

  const saveMutation = useMutation({
    mutationFn: (values: AISettingsFormValues) => {
      const payload: Record<string, unknown> = {
        enabled: values.enabled,
        baseUrl: values.baseUrl.trim(),
        model: values.model.trim(),
        embeddingModel: values.embeddingModel.trim(),
        temperature: values.temperature,
        systemPrompt: values.systemPrompt,
        footer: values.footer,
        quietSeconds: values.quietSeconds,
        maxReplies: values.maxReplies,
        contextMessages: values.contextMessages,
        knowledgeTopK: values.knowledgeTopK,
        autoSummaryOnClose: values.autoSummaryOnClose,
        callRetentionDays: values.callRetentionDays,
      }
      // Key 只写：填了新值才提交，避免把已存 Key 覆盖为空。
      if (values.apiKey && values.apiKey.trim() !== "") {
        payload.apiKey = values.apiKey.trim()
      }
      return api.put("/ai/settings", payload)
    },
    onSuccess: () => {
      toastSuccess(t("aiSettings.saveSuccess"))
      form.setValue("apiKey", "")
      setTestResult(null)
      void queryClient.invalidateQueries({ queryKey: queryKeys.aiSettings })
    },
    onError: (error) => toastError(error),
  })

  const testMutation = useMutation({
    mutationFn: () => api.post<{ ok: boolean; error?: string }>("/ai/test"),
    onSuccess: (data) => {
      setTestResult({ ok: data.ok, message: data.error })
      if (data.ok) toastSuccess(t("aiSettings.testSuccess"))
    },
    onError: (error) => toastError(error),
  })

  const { register, handleSubmit, watch, setValue, formState } = form
  const errors = formState.errors

  if (settingsQuery.isError) {
    return (
      <div className="space-y-4">
        <PageHeader title={t("aiSettings.title")} description={t("aiSettings.description")} />
        <ErrorState error={settingsQuery.error} onRetry={() => void settingsQuery.refetch()} />
      </div>
    )
  }

  if (settingsQuery.isPending) {
    return (
      <div className="space-y-4">
        <PageHeader title={t("aiSettings.title")} description={t("aiSettings.description")} />
        <InlineLoader />
      </div>
    )
  }

  const settings = settingsQuery.data
  const usage = settings?.usage

  return (
    <div className="space-y-5">
      <PageHeader title={t("aiSettings.title")} description={t("aiSettings.description")} />

      <Alert>
        <ShieldAlert className="size-4" />
        <AlertDescription>{t("aiSettings.privacyNotice")}</AlertDescription>
      </Alert>

      <form onSubmit={handleSubmit((values) => saveMutation.mutate(values))} className="space-y-5">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Bot className="size-4" />
              {t("aiSettings.connectionTitle")}
            </CardTitle>
            <CardDescription>{t("aiSettings.connectionHint")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <Field>
              <FieldLabel htmlFor="aiEnabled">{t("aiSettings.enabledLabel")}</FieldLabel>
              <div className="flex items-center gap-3">
                <Switch
                  id="aiEnabled"
                  checked={watch("enabled")}
                  onCheckedChange={(checked) => setValue("enabled", checked, { shouldDirty: true })}
                />
                <FieldDescription>{t("aiSettings.enabledHint")}</FieldDescription>
              </div>
            </Field>

            <FieldGroup className="grid gap-4 md:grid-cols-2">
              <Field data-invalid={Boolean(errors.baseUrl)}>
                <FieldLabel htmlFor="aiBaseUrl">{t("aiSettings.baseUrlLabel")}</FieldLabel>
                <Input id="aiBaseUrl" placeholder="https://api.deepseek.com/v1" {...register("baseUrl")} />
                <FieldDescription>{t("aiSettings.baseUrlHint")}</FieldDescription>
                <FieldError errors={errors.baseUrl ? [errors.baseUrl] : undefined} />
              </Field>
              <Field data-invalid={Boolean(errors.model)}>
                <FieldLabel htmlFor="aiModel">{t("aiSettings.modelLabel")}</FieldLabel>
                <Input id="aiModel" placeholder="deepseek-chat" {...register("model")} />
                <FieldError errors={errors.model ? [errors.model] : undefined} />
              </Field>
              <Field data-invalid={Boolean(errors.apiKey)}>
                <FieldLabel htmlFor="aiApiKey">{t("aiSettings.apiKeyLabel")}</FieldLabel>
                <Input
                  id="aiApiKey"
                  type="password"
                  autoComplete="off"
                  placeholder={t("aiSettings.apiKeyPlaceholder")}
                  {...register("apiKey")}
                />
                <FieldDescription>
                  {t("aiSettings.apiKeyCurrent", { masked: settings?.apiKeyMasked || t("common.none") })}
                </FieldDescription>
              </Field>
              <Field data-invalid={Boolean(errors.embeddingModel)}>
                <FieldLabel htmlFor="aiEmbeddingModel">{t("aiSettings.embeddingModelLabel")}</FieldLabel>
                <Input id="aiEmbeddingModel" placeholder="text-embedding-v3" {...register("embeddingModel")} />
                <FieldDescription>{t("aiSettings.embeddingModelHint")}</FieldDescription>
              </Field>
            </FieldGroup>

            <div className="flex items-center gap-3">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={testMutation.isPending}
                onClick={() => testMutation.mutate()}
              >
                <FlaskConical className="size-4" />
                {testMutation.isPending ? t("aiSettings.testing") : t("aiSettings.testButton")}
              </Button>
              {testResult ? (
                <span className={`text-xs ${testResult.ok ? "text-status-open" : "text-destructive"}`}>
                  {testResult.ok ? t("aiSettings.testSuccess") : testResult.message}
                </span>
              ) : null}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("aiSettings.promptTitle")}</CardTitle>
            <CardDescription>{t("aiSettings.promptHint")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <Field data-invalid={Boolean(errors.systemPrompt)}>
              <FieldLabel htmlFor="aiSystemPrompt">{t("aiSettings.systemPromptLabel")}</FieldLabel>
              <Textarea id="aiSystemPrompt" rows={8} {...register("systemPrompt")} />
              <FieldDescription>{t("aiSettings.systemPromptHint")}</FieldDescription>
              <FieldError errors={errors.systemPrompt ? [errors.systemPrompt] : undefined} />
            </Field>
            <Field data-invalid={Boolean(errors.footer)}>
              <FieldLabel htmlFor="aiFooter">{t("aiSettings.footerLabel")}</FieldLabel>
              <Input id="aiFooter" {...register("footer")} />
              <FieldDescription>{t("aiSettings.footerHint")}</FieldDescription>
              <FieldError errors={errors.footer ? [errors.footer] : undefined} />
            </Field>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("aiSettings.guardTitle")}</CardTitle>
            <CardDescription>{t("aiSettings.guardHint")}</CardDescription>
          </CardHeader>
          <CardContent>
            <FieldGroup className="grid gap-4 md:grid-cols-2">
              <NumberField
                id="aiQuietSeconds"
                label={t("aiSettings.quietSecondsLabel")}
                hint={t("aiSettings.quietSecondsHint")}
                min={1}
                max={120}
                register={register("quietSeconds", { valueAsNumber: true })}
                error={errors.quietSeconds}
              />
              <NumberField
                id="aiMaxReplies"
                label={t("aiSettings.maxRepliesLabel")}
                hint={t("aiSettings.maxRepliesHint")}
                min={1}
                max={100}
                register={register("maxReplies", { valueAsNumber: true })}
                error={errors.maxReplies}
              />
              <NumberField
                id="aiContextMessages"
                label={t("aiSettings.contextMessagesLabel")}
                hint={t("aiSettings.contextMessagesHint")}
                min={5}
                max={100}
                register={register("contextMessages", { valueAsNumber: true })}
                error={errors.contextMessages}
              />
              <NumberField
                id="aiKnowledgeTopK"
                label={t("aiSettings.knowledgeTopKLabel")}
                hint={t("aiSettings.knowledgeTopKHint")}
                min={1}
                max={20}
                register={register("knowledgeTopK", { valueAsNumber: true })}
                error={errors.knowledgeTopK}
              />
              <NumberField
                id="aiTemperature"
                label={t("aiSettings.temperatureLabel")}
                hint={t("aiSettings.temperatureHint")}
                min={0}
                max={2}
                step={0.1}
                register={register("temperature", { valueAsNumber: true })}
                error={errors.temperature}
              />
              <NumberField
                id="aiCallRetentionDays"
                label={t("aiSettings.retentionLabel")}
                hint={t("aiSettings.retentionHint")}
                min={0}
                max={3650}
                register={register("callRetentionDays", { valueAsNumber: true })}
                error={errors.callRetentionDays}
              />
            </FieldGroup>

            <Field className="pt-2">
              <FieldLabel htmlFor="aiAutoSummary">{t("aiSettings.autoSummaryLabel")}</FieldLabel>
              <div className="flex items-center gap-3">
                <Switch
                  id="aiAutoSummary"
                  checked={watch("autoSummaryOnClose")}
                  onCheckedChange={(checked) => setValue("autoSummaryOnClose", checked, { shouldDirty: true })}
                />
                <FieldDescription>{t("aiSettings.autoSummaryHint")}</FieldDescription>
              </div>
            </Field>
          </CardContent>
        </Card>

        {usage ? (
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <CheckCircle2 className="size-4" />
                {t("aiSettings.usageTitle", { days: usage.days })}
              </CardTitle>
            </CardHeader>
            <CardContent>
              <dl className="text-muted-foreground grid grid-cols-2 gap-3 text-xs md:grid-cols-3">
                <UsageItem label={t("aiSettings.usageCalls")} value={usage.totalCalls} />
                <UsageItem label={t("aiSettings.usageFailed")} value={usage.failedCalls} />
                <UsageItem label={t("aiSettings.usageTokens")} value={usage.promptTokens + usage.completionTokens} />
                <UsageItem label={t("aiSettings.usageKnowledge")} value={usage.knowledgeEntries} />
                <UsageItem label={t("aiSettings.usageTickets")} value={usage.autoRepliedTickets} />
              </dl>
            </CardContent>
          </Card>
        ) : null}

        <div className="flex justify-end">
          <Button type="submit" disabled={saveMutation.isPending}>
            {saveMutation.isPending ? t("common.saving") : t("common.save")}
          </Button>
        </div>
      </form>
    </div>
  )
}

function NumberField({
  id,
  label,
  hint,
  min,
  max,
  step,
  register,
  error,
}: {
  id: string
  label: string
  hint: string
  min: number
  max: number
  step?: number
  register: ReturnType<ReturnType<typeof useForm<AISettingsFormValues>>["register"]>
  error?: { message?: string }
}) {
  return (
    <Field data-invalid={Boolean(error)}>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <Input id={id} type="number" min={min} max={max} step={step} {...register} />
      <FieldDescription>{hint}</FieldDescription>
      {error ? <FieldError>{error.message}</FieldError> : null}
    </Field>
  )
}

function UsageItem({ label, value }: { label: string; value: number }) {
  return (
    <div className="bg-muted/40 rounded-lg border p-3">
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd className="text-foreground mt-1 text-lg font-semibold tabular-nums">{value.toLocaleString()}</dd>
    </div>
  )
}

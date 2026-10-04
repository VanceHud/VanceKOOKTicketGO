/**
 * 系统设置（仅管理员）。
 *
 * 表单使用 react-hook-form + zod 校验，配合官方 registry 的 Field 组件呈现：
 *  - ID 字段校验 KOOK 雪花 ID 格式；
 *  - Token 为只写字段，留空表示不修改；
 *  - 超时小时数限制 0–720，0 表示关闭自动锁定。
 */

import { useEffect } from "react"
import { zodResolver } from "@hookform/resolvers/zod"
import { AlertTriangle, CheckCircle2 } from "lucide-react"
import { useForm, type FieldErrors, type UseFormRegister } from "react-hook-form"
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
import { api } from "@/lib/api"
import { queryKeys, useSettings } from "@/lib/queries"
import { toastError, toastSuccess } from "@/lib/toast"

/** KOOK 的 ID 为十进制雪花串。 */
const kookIdPattern = /^[0-9]{5,32}$/
const optionalKookId = z
  .string()
  .refine((value) => value === "" || kookIdPattern.test(value), { message: "invalidId" })

const settingsSchema = z.object({
  kookToken: z.string().optional(),
  guildId: optionalKookId,
  guildName: z.string().max(64),
  categoryId: optionalKookId,
  categoryName: z.string().max(64),
  logChannelId: optionalKookId,
  logChannelName: z.string().max(64),
  debugChannelId: optionalKookId,
  debugChannelName: z.string().max(64),
  // 使用 valueAsNumber 注册，避免 z.coerce 造成输入/输出类型不一致
  outdateHours: z.number({ message: "invalidNumber" }).int().min(0).max(720),
})

type SettingsFormValues = z.infer<typeof settingsSchema>

const EMPTY_VALUES: SettingsFormValues = {
  kookToken: "",
  guildId: "",
  guildName: "",
  categoryId: "",
  categoryName: "",
  logChannelId: "",
  logChannelName: "",
  debugChannelId: "",
  debugChannelName: "",
  outdateHours: 48,
}

export function SettingsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const settingsQuery = useSettings()

  const form = useForm<SettingsFormValues>({
    resolver: zodResolver(settingsSchema),
    defaultValues: EMPTY_VALUES,
  })

  useEffect(() => {
    const data = settingsQuery.data
    if (!data) return
    form.reset({
      kookToken: "",
      guildId: data.guildId ?? "",
      guildName: data.guildName ?? "",
      categoryId: data.categoryId ?? "",
      categoryName: data.categoryName ?? "",
      logChannelId: data.logChannelId ?? "",
      logChannelName: data.logChannelName ?? "",
      debugChannelId: data.debugChannelId ?? "",
      debugChannelName: data.debugChannelName ?? "",
      outdateHours: data.outdateHours ?? 48,
    })
  }, [settingsQuery.data, form])

  const saveMutation = useMutation({
    mutationFn: (values: SettingsFormValues) => {
      const payload: Record<string, unknown> = {
        guildId: values.guildId,
        guildName: values.guildName,
        categoryId: values.categoryId,
        categoryName: values.categoryName,
        logChannelId: values.logChannelId,
        logChannelName: values.logChannelName,
        debugChannelId: values.debugChannelId,
        debugChannelName: values.debugChannelName,
        outdateHours: values.outdateHours,
      }
      // Token 只写：只有填了新值才提交，避免把已存 token 覆盖为空。
      if (values.kookToken && values.kookToken.trim() !== "") {
        payload.kookToken = values.kookToken.trim()
      }
      return api.put<{ ok: boolean; changed: string[] }>("/settings", payload)
    },
    onSuccess: () => {
      toastSuccess(t("settings.saveSuccess"))
      form.setValue("kookToken", "")
      void queryClient.invalidateQueries({ queryKey: queryKeys.settings })
      void queryClient.invalidateQueries({ queryKey: queryKeys.runtime })
    },
    onError: (error) => toastError(error),
  })

  const { register, handleSubmit, formState } = form

  if (settingsQuery.isError) {
    return (
      <div className="space-y-4">
        <PageHeader title={t("settings.title")} description={t("settings.description")} />
        <ErrorState error={settingsQuery.error} onRetry={() => void settingsQuery.refetch()} />
      </div>
    )
  }

  if (settingsQuery.isPending) {
    return (
      <div className="space-y-4">
        <PageHeader title={t("settings.title")} description={t("settings.description")} />
        <InlineLoader />
      </div>
    )
  }

  const settings = settingsQuery.data
  const errors = formState.errors

  return (
    <div className="space-y-5">
      <PageHeader title={t("settings.title")} description={t("settings.description")} />

      {settings && settings.missingRequired.length > 0 ? (
        <Alert variant="destructive">
          <AlertTriangle className="size-4" />
          <AlertDescription>
            {t("settings.missingTitle")}：{settings.missingRequired.join("、")}
          </AlertDescription>
        </Alert>
      ) : (
        <Alert>
          <CheckCircle2 className="size-4" />
          <AlertDescription>{t("settings.missingNone")}</AlertDescription>
        </Alert>
      )}

      <form onSubmit={handleSubmit((values) => saveMutation.mutate(values))} className="space-y-5">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("settings.tokenLabel")}</CardTitle>
            <CardDescription>{t("settings.tokenHint")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <p className="text-muted-foreground text-xs">
              {t("settings.tokenCurrent", { masked: settings?.kookTokenMasked || t("common.none") })}
            </p>
            <Field data-invalid={Boolean(errors.kookToken)}>
              <FieldLabel htmlFor="kookToken">{t("settings.tokenLabel")}</FieldLabel>
              <Input
                id="kookToken"
                type="password"
                autoComplete="off"
                placeholder={t("settings.tokenPlaceholder")}
                aria-invalid={Boolean(errors.kookToken)}
                {...register("kookToken")}
              />
              <FieldError errors={errors.kookToken ? [errors.kookToken] : undefined} />
            </Field>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("bot.configTitle")}</CardTitle>
            <CardDescription>{t("settings.categoryHint")}</CardDescription>
          </CardHeader>
          <CardContent>
            <FieldGroup className="grid gap-4 md:grid-cols-2">
              <IdField
                register={register}
                errors={errors}
                idField="guildId"
                nameField="guildName"
                label={t("settings.guildLabel")}
                nameLabel={t("settings.nameLabel")}
              />
              <IdField
                register={register}
                errors={errors}
                idField="categoryId"
                nameField="categoryName"
                label={t("settings.categoryLabel")}
                nameLabel={t("settings.nameLabel")}
                description={t("settings.categoryHint")}
              />
              <IdField
                register={register}
                errors={errors}
                idField="logChannelId"
                nameField="logChannelName"
                label={t("settings.logLabel")}
                nameLabel={t("settings.nameLabel")}
              />
              <IdField
                register={register}
                errors={errors}
                idField="debugChannelId"
                nameField="debugChannelName"
                label={t("settings.debugLabel")}
                nameLabel={t("settings.nameLabel")}
              />

              <Field data-invalid={Boolean(errors.outdateHours)}>
                <FieldLabel htmlFor="outdateHours">{t("settings.outdateLabel")}</FieldLabel>
                <Input
                  id="outdateHours"
                  type="number"
                  min={0}
                  max={720}
                  aria-invalid={Boolean(errors.outdateHours)}
                  {...register("outdateHours", { valueAsNumber: true })}
                />
                <FieldDescription>{t("settings.outdateHint")}</FieldDescription>
                <FieldError errors={errors.outdateHours ? [errors.outdateHours] : undefined} />
              </Field>
            </FieldGroup>
          </CardContent>
        </Card>

        <div className="flex justify-end">
          <Button type="submit" disabled={saveMutation.isPending}>
            {saveMutation.isPending ? t("common.saving") : t("common.save")}
          </Button>
        </div>
      </form>
    </div>
  )
}

type IdFieldName = "guildId" | "categoryId" | "logChannelId" | "debugChannelId"
type NameFieldName = "guildName" | "categoryName" | "logChannelName" | "debugChannelName"

/** ID + 显示名成对字段；校验失败时展示统一的 ID 格式提示。 */
function IdField({
  register,
  errors,
  idField,
  nameField,
  label,
  nameLabel,
  description,
}: {
  register: UseFormRegister<SettingsFormValues>
  errors: FieldErrors<SettingsFormValues>
  idField: IdFieldName
  nameField: NameFieldName
  label: string
  nameLabel: string
  description?: string
}) {
  const { t } = useTranslation()
  const idError = errors[idField]

  return (
    <div className="space-y-3">
      <Field data-invalid={Boolean(idError)}>
        <FieldLabel htmlFor={idField}>{label}</FieldLabel>
        <Input
          id={idField}
          inputMode="numeric"
          placeholder="1000000000000001"
          aria-invalid={Boolean(idError)}
          {...register(idField)}
        />
        {description ? <FieldDescription>{description}</FieldDescription> : null}
        {idError ? <FieldError>{t("settings.idInvalid")}</FieldError> : null}
      </Field>
      <Field>
        <FieldLabel htmlFor={nameField} className="text-muted-foreground text-xs">
          {nameLabel}
        </FieldLabel>
        <Input id={nameField} {...register(nameField)} />
      </Field>
    </div>
  )
}

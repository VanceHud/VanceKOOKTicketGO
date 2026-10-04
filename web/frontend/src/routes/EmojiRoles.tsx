/** 表情上角色：规则列表与最近发放记录。 */

import { Info, SmilePlus } from "lucide-react"
import { useTranslation } from "react-i18next"

import { PageHeader } from "@/components/PageHeader"
import { EmptyState, ErrorState, InlineLoader } from "@/components/StateViews"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { formatDateTime } from "@/lib/format"
import { useEmojiGrants, useEmojiRules } from "@/lib/queries"

export function EmojiRolesPage() {
  const { t, i18n } = useTranslation()
  const rulesQuery = useEmojiRules()
  const grantsQuery = useEmojiGrants()

  return (
    <div className="space-y-4">
      <PageHeader title={t("emoji.title")} description={t("emoji.description")} />

      <Alert>
        <Info className="size-4" />
        <AlertDescription>{t("emoji.note")}</AlertDescription>
      </Alert>

      {rulesQuery.isError ? (
        <ErrorState error={rulesQuery.error} onRetry={() => void rulesQuery.refetch()} />
      ) : rulesQuery.isPending ? (
        <InlineLoader />
      ) : (
        <Card className="py-0">
          <CardContent className="px-0">
            {(rulesQuery.data?.items.length ?? 0) === 0 ? (
              <EmptyState title={t("emoji.empty")} icon={<SmilePlus className="size-6" />} />
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("emoji.colEmoji")}</TableHead>
                    <TableHead>{t("emoji.colLabel")}</TableHead>
                    <TableHead className="hidden md:table-cell">{t("emoji.colMessage")}</TableHead>
                    <TableHead className="hidden lg:table-cell">{t("emoji.colChannel")}</TableHead>
                    <TableHead>{t("emoji.colRole")}</TableHead>
                    <TableHead>{t("emoji.colEnabled")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rulesQuery.data?.items.map((rule) => (
                    <TableRow key={rule.id}>
                      <TableCell className="text-lg">{rule.emojiId}</TableCell>
                      <TableCell className="text-sm">{rule.label || "—"}</TableCell>
                      <TableCell className="hidden font-mono text-xs md:table-cell">{rule.messageId}</TableCell>
                      <TableCell className="hidden font-mono text-xs lg:table-cell">{rule.channelId || "—"}</TableCell>
                      <TableCell className="font-mono text-xs">{rule.roleId}</TableCell>
                      <TableCell>
                        <Badge variant={rule.enabled ? "secondary" : "outline"} className="font-normal">
                          {rule.enabled ? t("common.enabled") : t("common.disabled")}
                        </Badge>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("emoji.grantsTitle")}</CardTitle>
        </CardHeader>
        <CardContent className="px-0">
          {grantsQuery.isPending ? (
            <InlineLoader />
          ) : (grantsQuery.data?.items.length ?? 0) === 0 ? (
            <EmptyState title={t("emoji.grantsEmpty")} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("emoji.colUser")}</TableHead>
                  <TableHead>{t("emoji.colEmoji")}</TableHead>
                  <TableHead>{t("emoji.colRole")}</TableHead>
                  <TableHead>{t("emoji.colGrantedAt")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {grantsQuery.data?.items.map((grant) => (
                  <TableRow key={grant.id}>
                    <TableCell className="font-mono text-xs">{grant.kookUserId}</TableCell>
                    <TableCell className="text-lg">{grant.emojiId}</TableCell>
                    <TableCell className="font-mono text-xs">{grant.roleId}</TableCell>
                    <TableCell className="text-muted-foreground text-xs">
                      {formatDateTime(grant.grantedAt, i18n.language)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

/** 面板管理：展示已配置的工单按钮面板与其面板级管理员角色。 */

import { Info, LayoutPanelLeft } from "lucide-react"
import { useTranslation } from "react-i18next"

import { PageHeader } from "@/components/PageHeader"
import { EmptyState, ErrorState, InlineLoader } from "@/components/StateViews"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { formatDateTime } from "@/lib/format"
import { usePanels } from "@/lib/queries"

export function PanelsPage() {
  const { t, i18n } = useTranslation()
  const panelsQuery = usePanels()

  return (
    <div className="space-y-4">
      <PageHeader title={t("panels.title")} description={t("panels.description")} />

      <Alert>
        <Info className="size-4" />
        <AlertDescription>{t("panels.note")}</AlertDescription>
      </Alert>

      {panelsQuery.isError ? (
        <ErrorState error={panelsQuery.error} onRetry={() => void panelsQuery.refetch()} />
      ) : panelsQuery.isPending ? (
        <InlineLoader />
      ) : (panelsQuery.data?.items.length ?? 0) === 0 ? (
        <Card>
          <CardContent>
            <EmptyState title={t("panels.empty")} icon={<LayoutPanelLeft className="size-6" />} />
          </CardContent>
        </Card>
      ) : (
        <Card className="py-0">
          <CardContent className="px-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("panels.colChannel")}</TableHead>
                  <TableHead>{t("panels.colTitle")}</TableHead>
                  <TableHead className="hidden md:table-cell">{t("panels.colMessage")}</TableHead>
                  <TableHead>{t("panels.colRoles")}</TableHead>
                  <TableHead className="hidden lg:table-cell">{t("common.status")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {panelsQuery.data?.items.map((panel) => (
                  <TableRow key={panel.id}>
                    <TableCell>
                      <div className="space-y-0.5">
                        <p className="text-sm font-medium">{panel.channelName || "—"}</p>
                        <p className="text-muted-foreground font-mono text-xs">{panel.channelId}</p>
                      </div>
                    </TableCell>
                    <TableCell className="max-w-56 truncate text-sm">{panel.title || "—"}</TableCell>
                    <TableCell className="hidden font-mono text-xs md:table-cell">{panel.msgId || "—"}</TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-1">
                        {(panel.roles ?? []).length === 0 ? (
                          <span className="text-muted-foreground text-xs">{t("common.none")}</span>
                        ) : (
                          panel.roles?.map((role) => (
                            <Badge key={role.id} variant="outline" className="font-normal">
                              {role.roleName || role.roleId}
                            </Badge>
                          ))
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="hidden lg:table-cell">
                      <Badge variant={panel.enabled ? "secondary" : "outline"} className="font-normal">
                        {panel.enabled ? t("common.enabled") : t("common.disabled")}
                      </Badge>
                      <p className="text-muted-foreground mt-1 text-xs">
                        {formatDateTime(panel.updatedAt, i18n.language)}
                      </p>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}
    </div>
  )
}

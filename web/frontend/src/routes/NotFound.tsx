/** 404 页面。 */

import { Compass } from "lucide-react"
import { useTranslation } from "react-i18next"
import { Link } from "react-router"

import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"

export function NotFoundPage() {
  const { t } = useTranslation()
  return (
    <Card>
      <CardContent className="flex flex-col items-center gap-4 py-16 text-center">
        <Compass className="text-muted-foreground size-8" />
        <div className="space-y-1">
          <p className="text-lg font-semibold">{t("notFound.title")}</p>
          <p className="text-muted-foreground text-sm">{t("notFound.description")}</p>
        </div>
        <Button asChild variant="outline">
          <Link to="/">{t("notFound.backHome")}</Link>
        </Button>
      </CardContent>
    </Card>
  )
}

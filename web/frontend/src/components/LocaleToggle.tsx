/** 语言切换：中文 / English，偏好写入 localStorage。 */

import { Languages } from "lucide-react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { changeLanguage, SUPPORTED_LANGUAGES, type SupportedLanguage } from "@/lib/i18n"

const LANGUAGE_LABELS: Record<SupportedLanguage, string> = {
  "zh-CN": "简体中文",
  "en-US": "English",
}

export function LocaleToggle() {
  const { i18n } = useTranslation()

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" aria-label="Language">
          <Languages className="size-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuLabel>Language</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {SUPPORTED_LANGUAGES.map((language) => (
          <DropdownMenuItem key={language} onClick={() => changeLanguage(language)}>
            {LANGUAGE_LABELS[language]}
            {i18n.language === language ? <span className="text-primary ml-auto text-xs">●</span> : null}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

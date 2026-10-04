/**
 * i18n 初始化。
 *
 * 语言选择顺序：localStorage 中的用户偏好 → 浏览器语言 → 默认 zh-CN。
 * 文案按页面分区组织，键名见 src/locale/*.json。
 */

import i18n from "i18next"
import { initReactI18next } from "react-i18next"

import enUS from "@/locale/en-US.json"
import zhCN from "@/locale/zh-CN.json"

export const LANGUAGE_STORAGE_KEY = "kookticket.language"
export const SUPPORTED_LANGUAGES = ["zh-CN", "en-US"] as const
export type SupportedLanguage = (typeof SUPPORTED_LANGUAGES)[number]

function detectLanguage(): SupportedLanguage {
  const stored = localStorage.getItem(LANGUAGE_STORAGE_KEY)
  if (stored && (SUPPORTED_LANGUAGES as readonly string[]).includes(stored)) {
    return stored as SupportedLanguage
  }
  return navigator.language.toLowerCase().startsWith("zh") ? "zh-CN" : "en-US"
}

void i18n.use(initReactI18next).init({
  resources: {
    "zh-CN": { translation: zhCN },
    "en-US": { translation: enUS },
  },
  lng: detectLanguage(),
  fallbackLng: "zh-CN",
  interpolation: { escapeValue: false },
  returnNull: false,
})

export function changeLanguage(language: SupportedLanguage) {
  localStorage.setItem(LANGUAGE_STORAGE_KEY, language)
  void i18n.changeLanguage(language)
}

export default i18n

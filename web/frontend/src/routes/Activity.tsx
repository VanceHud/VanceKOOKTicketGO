/**
 * 机器人动态：游戏库管理与「在玩/在听」状态控制。
 *
 * 对应 KOOK 的 game 系列接口：
 *  - 游戏列表 / 新建 / 更新 / 删除（游戏库）；
 *  - game/activity、game/delete-activity（在玩/在听动态）。
 *
 * 平台限制：单日最多新建 5 个游戏，且动态绑定在网关会话上，
 * 机器人重启后会被清空，因此提供「重连后自动恢复」开关。
 */

import { useMemo, useState } from "react"
import {
  Gamepad2,
  Headphones,
  ImageOff,
  Music4,
  Pencil,
  Plus,
  RefreshCw,
  RotateCcw,
  Square,
  Trash2,
} from "lucide-react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import { PageHeader } from "@/components/PageHeader"
import { useAuth } from "@/lib/auth"
import { api } from "@/lib/api"
import { toastError, toastSuccess } from "@/lib/toast"
import { EmptyState, ErrorState, InlineLoader } from "@/components/StateViews"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { formatDateTime } from "@/lib/format"
import { queryKeys, useBotActivity, useGames } from "@/lib/queries"
import type { KookGame, MusicSoftware } from "@/lib/types"

const MUSIC_SOFTWARE: MusicSoftware[] = ["cloudmusic", "qqmusic", "kugou"]

export function ActivityPage() {
  const { t, i18n } = useTranslation()
  const { me } = useAuth()
  const isAdmin = me?.user.role === "admin"
  const queryClient = useQueryClient()

  const [gameType, setGameType] = useState(0)
  const activityQuery = useBotActivity()
  const gamesQuery = useGames(gameType)
  const activity = activityQuery.data
  const games = gamesQuery.data?.items ?? []

  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<KookGame | null>(null)
  const [deleting, setDeleting] = useState<KookGame | null>(null)
  const [stopping, setStopping] = useState(false)

  const invalidateActivity = () => void queryClient.invalidateQueries({ queryKey: queryKeys.botActivity })
  const invalidateGames = () => void queryClient.invalidateQueries({ queryKey: queryKeys.games(gameType) })

  const createMutation = useMutation({
    mutationFn: (payload: { name: string; icon: string }) => api.post<KookGame>("/games", payload),
    onSuccess: () => {
      toastSuccess(t("activity.createSuccess"))
      setCreating(false)
      invalidateGames()
    },
    onError: (error) => toastError(error),
  })

  const updateMutation = useMutation({
    mutationFn: (payload: { id: number; name: string; icon: string }) =>
      api.patch<KookGame>(`/games/${payload.id}`, { name: payload.name, icon: payload.icon }),
    onSuccess: () => {
      toastSuccess(t("activity.updateSuccess"))
      setEditing(null)
      invalidateGames()
      invalidateActivity()
    },
    onError: (error) => toastError(error),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.del(`/games/${id}`),
    onSuccess: () => {
      toastSuccess(t("activity.deleteSuccess"))
      setDeleting(null)
      invalidateGames()
    },
    onError: (error) => {
      toastError(error)
      setDeleting(null)
    },
  })

  const startGameMutation = useMutation({
    mutationFn: (game: KookGame) =>
      api.post("/bot/activity", { dataType: 1, gameId: game.id, gameName: game.name }),
    onSuccess: () => {
      toastSuccess(t("activity.startGameSuccess"))
      invalidateActivity()
    },
    onError: (error) => toastError(error),
  })

  const startMusicMutation = useMutation({
    mutationFn: (payload: { musicName: string; singer: string; software: MusicSoftware }) =>
      api.post("/bot/activity", { dataType: 2, ...payload }),
    onSuccess: () => {
      toastSuccess(t("activity.startMusicSuccess"))
      invalidateActivity()
    },
    onError: (error) => toastError(error),
  })

  const autoRestoreMutation = useMutation({
    mutationFn: (autoRestore: boolean) => api.put("/bot/activity/settings", { autoRestore }),
    onSuccess: () => {
      toastSuccess(t("activity.autoRestoreSuccess"))
      invalidateActivity()
    },
    onError: (error) => toastError(error),
  })

  const stopMutation = useMutation({
    mutationFn: () => api.del("/bot/activity"),
    onSuccess: () => {
      toastSuccess(t("activity.stopSuccess"))
      setStopping(false)
      invalidateActivity()
    },
    onError: (error) => {
      toastError(error)
      setStopping(false)
    },
  })

  const busy = startGameMutation.isPending || startMusicMutation.isPending

  return (
    <div className="space-y-5">
      <PageHeader title={t("activity.title")} description={t("activity.description")} />

      {!activity?.connected && !activityQuery.isPending ? (
        activity?.dryRun ? (
          <Alert>
            <RotateCcw className="size-4" />
            <AlertTitle>{t("bot.dryRun")}</AlertTitle>
            <AlertDescription>{t("activity.dryRunDesc")}</AlertDescription>
          </Alert>
        ) : (
          <Alert variant="destructive">
            <RotateCcw className="size-4" />
            <AlertTitle>{t("activity.offlineTitle")}</AlertTitle>
            <AlertDescription>{t("activity.offlineDesc")}</AlertDescription>
          </Alert>
        )
      ) : null}

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Gamepad2 className="size-4" />
              {t("activity.currentTitle")}
            </CardTitle>
            <CardDescription>{t("activity.currentDesc")}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {activityQuery.isError ? (
              <ErrorState error={activityQuery.error} onRetry={() => void activityQuery.refetch()} />
            ) : activityQuery.isPending ? (
              <InlineLoader />
            ) : activity?.current ? (
              <div className="space-y-3">
                <div className="flex items-start gap-3 rounded-lg border p-3">
                  {activity.current.dataType === 1 ? (
                    <Gamepad2 className="mt-0.5 size-5 shrink-0" />
                  ) : (
                    <Music4 className="mt-0.5 size-5 shrink-0" />
                  )}
                  <div className="min-w-0 space-y-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge variant="outline">
                        {activity.current.dataType === 1 ? t("activity.tabGame") : t("activity.tabMusic")}
                      </Badge>
                      <span className="truncate font-medium">
                        {activity.current.dataType === 1
                          ? activity.current.gameName || `#${activity.current.gameId}`
                          : activity.current.musicName}
                      </span>
                    </div>
                    <p className="text-muted-foreground text-xs">
                      {activity.current.dataType === 1
                        ? `ID ${activity.current.gameId}`
                        : `${activity.current.singer} · ${t(`activity.software_${activity.current.software}`, {
                            defaultValue: activity.current.software ?? "",
                          })}`}
                    </p>
                    <p className="text-muted-foreground text-xs">
                      {t("activity.currentStartedAt")}：{formatDateTime(activity.current.startedAt, i18n.language)}
                      {activity.current.actor ? ` · ${activity.current.actor}` : ""}
                    </p>
                  </div>
                </div>

                {isAdmin ? (
                  <Button variant="outline" size="sm" onClick={() => setStopping(true)}>
                    <Square className="size-4" />
                    {t("activity.stop")}
                  </Button>
                ) : (
                  <p className="text-muted-foreground text-xs">{t("activity.readonlyHint")}</p>
                )}
              </div>
            ) : (
              <EmptyState title={t("activity.currentEmpty")} icon={<Gamepad2 className="size-6" />} />
            )}

            <div className="flex items-center justify-between gap-3 rounded-lg border p-3">
              <div className="space-y-0.5">
                <Label htmlFor="auto-restore">{t("activity.autoRestore")}</Label>
                <p className="text-muted-foreground text-xs">{t("activity.autoRestoreHint")}</p>
              </div>
              <Switch
                id="auto-restore"
                checked={activity?.autoRestore ?? true}
                disabled={!isAdmin || autoRestoreMutation.isPending || activityQuery.isPending}
                onCheckedChange={(checked) => autoRestoreMutation.mutate(checked)}
              />
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Headphones className="size-4" />
              {t("activity.startTitle")}
            </CardTitle>
            <CardDescription>{t("activity.startDesc")}</CardDescription>
          </CardHeader>
          <CardContent>
            <Tabs defaultValue="game">
              <TabsList>
                <TabsTrigger value="game">{t("activity.tabGame")}</TabsTrigger>
                <TabsTrigger value="music">{t("activity.tabMusic")}</TabsTrigger>
              </TabsList>

              <TabsContent value="game" className="space-y-3 pt-3">
                <GameStarter
                  games={games}
                  available={gamesQuery.data?.available ?? false}
                  loading={gamesQuery.isPending}
                  pending={startGameMutation.isPending}
                  disabled={!isAdmin || !activity?.connected}
                  onStart={(game) => startGameMutation.mutate(game)}
                />
              </TabsContent>

              <TabsContent value="music" className="space-y-3 pt-3">
                <MusicStarter
                  pending={busy}
                  disabled={!isAdmin || !activity?.connected}
                  onSubmit={(payload) => startMusicMutation.mutate(payload)}
                />
              </TabsContent>
            </Tabs>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader className="flex-row items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2 text-base">
              <Gamepad2 className="size-4" />
              {t("activity.libraryTitle")}
            </CardTitle>
            <CardDescription>{t("activity.libraryDesc")}</CardDescription>
          </div>
          <div className="flex items-center gap-2">
            <Select value={String(gameType)} onValueChange={(value) => setGameType(Number(value))}>
              <SelectTrigger className="w-32">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="0">{t("activity.filterAll")}</SelectItem>
                <SelectItem value="1">{t("activity.filterUser")}</SelectItem>
                <SelectItem value="2">{t("activity.filterSystem")}</SelectItem>
              </SelectContent>
            </Select>
            <Button variant="outline" size="icon" title={t("common.refresh")} onClick={invalidateGames}>
              <RefreshCw className="size-4" />
            </Button>
            {isAdmin ? (
              <Button size="sm" disabled={!gamesQuery.data?.available} onClick={() => setCreating(true)}>
                <Plus className="size-4" />
                {t("activity.create")}
              </Button>
            ) : null}
          </div>
        </CardHeader>
        <CardContent className="px-0">
          {!gamesQuery.isPending && gamesQuery.data && !gamesQuery.data.available ? (
            <div className="px-6 pb-4">
              <Alert>
                <ImageOff className="size-4" />
                <AlertDescription>{gamesQuery.data.note ?? t("activity.needOnline")}</AlertDescription>
              </Alert>
            </div>
          ) : null}

          {gamesQuery.isError ? (
            <div className="px-6 pb-4">
              <ErrorState error={gamesQuery.error} onRetry={() => void gamesQuery.refetch()} />
            </div>
          ) : gamesQuery.isPending ? (
            <InlineLoader />
          ) : games.length === 0 ? (
            <EmptyState title={t("activity.libraryEmpty")} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-12">{t("activity.colIcon")}</TableHead>
                  <TableHead>{t("activity.colName")}</TableHead>
                  <TableHead className="hidden md:table-cell">{t("activity.colId")}</TableHead>
                  <TableHead className="hidden lg:table-cell">{t("activity.colProcess")}</TableHead>
                  <TableHead className="text-right">{t("common.actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {games.map((game) => (
                  <TableRow key={game.id}>
                    <TableCell>
                      <GameIcon game={game} />
                    </TableCell>
                    <TableCell className="text-sm font-medium">{game.name}</TableCell>
                    <TableCell className="hidden font-mono text-xs md:table-cell">{game.id}</TableCell>
                    <TableCell className="hidden max-w-56 truncate font-mono text-xs lg:table-cell">
                      {(game.processName ?? []).join(", ") || "—"}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={!isAdmin || !activity?.connected}
                          onClick={() => startGameMutation.mutate(game)}
                        >
                          {t("activity.play")}
                        </Button>
                        {isAdmin ? (
                          <>
                            <Button variant="ghost" size="icon" title={t("common.edit")} onClick={() => setEditing(game)}>
                              <Pencil className="size-4" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              title={t("common.delete")}
                              onClick={() => setDeleting(game)}
                            >
                              <Trash2 className="size-4" />
                            </Button>
                          </>
                        ) : null}
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* 新建游戏 */}
      <Dialog open={creating} onOpenChange={setCreating}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("activity.createTitle")}</DialogTitle>
            <DialogDescription>{t("activity.createDesc")}</DialogDescription>
          </DialogHeader>
          <GameForm
            pending={createMutation.isPending}
            onSubmit={(values) => createMutation.mutate(values)}
          />
        </DialogContent>
      </Dialog>

      {/* 编辑游戏 */}
      <Dialog open={Boolean(editing)} onOpenChange={(open) => !open && setEditing(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("activity.editTitle")}</DialogTitle>
            <DialogDescription>
              <span className="font-mono text-xs">{editing?.id}</span>
            </DialogDescription>
          </DialogHeader>
          {editing ? (
            <GameForm
              pending={updateMutation.isPending}
              initial={editing}
              onSubmit={(values) => updateMutation.mutate({ id: editing.id, ...values })}
            />
          ) : null}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={Boolean(deleting)}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={t("activity.deleteTitle")}
        description={t("activity.deleteDesc")}
        confirmLabel={t("common.delete")}
        destructive
        pending={deleteMutation.isPending}
        onConfirm={() => deleting && deleteMutation.mutate(deleting.id)}
      />

      <ConfirmDialog
        open={stopping}
        onOpenChange={setStopping}
        title={t("activity.stopTitle")}
        description={t("activity.stopDesc")}
        confirmLabel={t("activity.stop")}
        destructive
        pending={stopMutation.isPending}
        onConfirm={() => stopMutation.mutate()}
      />
    </div>
  )
}

/** 游戏图标：加载失败或缺失时回退到占位图标。 */
function GameIcon({ game }: { game: KookGame }) {
  const [failed, setFailed] = useState(false)
  if (!game.icon || failed) {
    return <Gamepad2 className="text-muted-foreground size-5" />
  }
  return (
    <img
      src={game.icon}
      alt=""
      className="size-6 rounded object-cover"
      loading="lazy"
      onError={() => setFailed(true)}
    />
  )
}

/** 从游戏库选择并开始游戏。 */
function GameStarter({
  games,
  available,
  loading,
  pending,
  disabled,
  onStart,
}: {
  games: KookGame[]
  available: boolean
  loading: boolean
  pending: boolean
  disabled: boolean
  onStart: (game: KookGame) => void
}) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState("")

  const selectedGame = useMemo(() => games.find((game) => String(game.id) === selected), [games, selected])

  if (loading) return <InlineLoader />
  if (!available) return <p className="text-muted-foreground text-sm">{t("activity.needOnline")}</p>
  if (games.length === 0) return <EmptyState title={t("activity.noGames")} />

  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="activity-game">{t("activity.selectGame")}</Label>
        <Select value={selected} onValueChange={setSelected}>
          <SelectTrigger id="activity-game">
            <SelectValue placeholder={t("activity.selectGamePlaceholder")} />
          </SelectTrigger>
          <SelectContent>
            {games.map((game) => (
              <SelectItem key={game.id} value={String(game.id)}>
                {game.name} · {game.id}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <Button disabled={disabled || pending || !selectedGame} onClick={() => selectedGame && onStart(selectedGame)}>
        <Gamepad2 className="size-4" />
        {t("activity.startGame")}
      </Button>
    </>
  )
}

/** 填歌曲信息并开始听歌。 */
function MusicStarter({
  pending,
  disabled,
  onSubmit,
}: {
  pending: boolean
  disabled: boolean
  onSubmit: (payload: { musicName: string; singer: string; software: MusicSoftware }) => void
}) {
  const { t } = useTranslation()
  const [musicName, setMusicName] = useState("")
  const [singer, setSinger] = useState("")
  const [software, setSoftware] = useState<MusicSoftware>("cloudmusic")

  const valid = musicName.trim() !== "" && singer.trim() !== ""

  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="activity-music">{t("activity.musicName")}</Label>
        <Input
          id="activity-music"
          value={musicName}
          maxLength={200}
          placeholder={t("activity.musicNamePlaceholder")}
          onChange={(event) => setMusicName(event.target.value)}
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="activity-singer">{t("activity.singer")}</Label>
        <Input
          id="activity-singer"
          value={singer}
          maxLength={200}
          placeholder={t("activity.singerPlaceholder")}
          onChange={(event) => setSinger(event.target.value)}
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="activity-software">{t("activity.software")}</Label>
        <Select value={software} onValueChange={(value) => setSoftware(value as MusicSoftware)}>
          <SelectTrigger id="activity-software">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {MUSIC_SOFTWARE.map((item) => (
              <SelectItem key={item} value={item}>
                {t(`activity.software_${item}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <Button
        disabled={disabled || pending || !valid}
        onClick={() => onSubmit({ musicName: musicName.trim(), singer: singer.trim(), software })}
      >
        <Music4 className="size-4" />
        {t("activity.startMusic")}
      </Button>
    </>
  )
}

/** 游戏名称与图标表单（新建 / 编辑共用）。 */
function GameForm({
  pending,
  initial,
  onSubmit,
}: {
  pending: boolean
  initial?: KookGame
  onSubmit: (values: { name: string; icon: string }) => void
}) {
  const { t } = useTranslation()
  const [name, setName] = useState(initial?.name ?? "")
  const [icon, setIcon] = useState(initial?.icon ?? "")

  const valid = name.trim() !== ""

  return (
    <>
      <div className="space-y-4">
        <div className="space-y-2">
          <Label htmlFor="game-name">{t("activity.nameLabel")}</Label>
          <Input
            id="game-name"
            value={name}
            maxLength={64}
            placeholder={t("activity.namePlaceholder")}
            onChange={(event) => setName(event.target.value)}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="game-icon">{t("activity.iconLabel")}</Label>
          <Input
            id="game-icon"
            value={icon}
            placeholder="https://example.com/icon.png"
            onChange={(event) => setIcon(event.target.value)}
          />
          <p className="text-muted-foreground text-xs">{t("activity.iconHint")}</p>
        </div>
      </div>
      <DialogFooter>
        <Button
          disabled={pending || !valid}
          onClick={() => onSubmit({ name: name.trim(), icon: icon.trim() })}
        >
          {pending ? t("common.saving") : t("common.save")}
        </Button>
      </DialogFooter>
    </>
  )
}

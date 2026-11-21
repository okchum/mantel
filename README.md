# Mantel

一块自托管的「常亮屏 + 遥控」：把服务器监控、日程、天气、服务探活、AI 额度和订阅用量，以及任何你能拿到 JSON 的东西（Home Assistant、Docker、GitHub、订单、备份……）排成带动效的页面，任何屏幕打开都能看；屏幕上的按钮由服务器去执行。

想让 Google Nest Hub (Max) 一直亮着这个页面，在家里跑它的搭档 [Mantel Cast](https://github.com/okchum/mantel-cast)。

- 多页，左右滑动切换；每页可以写坐标、选预设模板，或者交给自动排版。
- 18 种组件：时钟、天气、日程、服务器、探活、额度、订阅用量、数字、仪表、曲线、柱状、状态格、列表、进度、文字、倒计时、图片、按钮。
- 按钮和点开的详情面板可以执行**服务器端操作**（比如额度用完点「重置额度」），由服务器去调 API 或跑命令，屏幕上不存任何密钥。
- Claude Pro/Max、ChatGPT Plus/Pro（Codex）的 5 小时 / 每周用量，和账号里攒下的「重置次数」：快用完时屏幕上直接出现「重置」键，按住就由服务器经 CLIProxyAPI 去官方兑换。
- 访问令牌分权限（只看 / 能按按钮 / 能推数据），能放到外网；告警推到 Bark。
- 单个二进制，纯 Go 标准库，唯一的依赖是与 Mantel Cast 共用配置格式的 [mantel-kit](https://github.com/okchum/mantel-kit)（同样只用标准库）；页面、字体都打包在里面。

## 快速开始

```bash
make build                     # 或 make dist 交叉编译 linux/arm64、amd64、树莓派、Windows……
./mantel run -demo             # 用演示数据在 http://localhost:8080 预览（4 页，可拖动/方向键切换，按钮都能点）
```

正式使用：

```bash
cp config.example.json config.json   # 按需修改
export DEEPSEEK_API_KEY=sk-...       # 配置里 "$NAME" 会读环境变量
./mantel run
```

## 部署和 Nest Hub

Mantel 只负责页面和数据，任何浏览器打开 `http://<服务器>:8080/?k=令牌` 都能用。放在哪都行：

- **家里**（NAS、树莓派、小主机）：能直接采集内网的 node_exporter 和服务。
- **外网服务器**：配上令牌，前面放 Caddy/nginx 做 HTTPS。外网访问不到家里内网的 node_exporter 和服务，这部分要么用 Tailscale/WireGuard 把服务器接进内网，要么由家里的脚本往 Mantel 推数据（推送型数据源，见下文）。

**Nest Hub**：Cast 只在局域网里工作，所以要在家里跑 [Mantel Cast](https://github.com/okchum/mantel-cast)。它盯着设备，空闲了就把 Mantel 的页面投上去，夜里自动安静。Mantel Cast 要用一个 Mantel 令牌打开页面；想在屏幕上按按钮，给这个令牌 `"scopes": ["act"]`。两者在同一台机器上时，Mantel Cast 可以直接读这份 `config.json`，不必另写 `mantel-cast.json`（它只看 `public_url`、`tokens`、`timezone`、`cast`，没有 `public_url` 时再用 `listen`、`tls_cert` 推出页面地址；令牌优先用名为 `mantel-cast` 的，其次 `nesthub`，否则第一个；`cast` 段 Mantel 不读，留给 Mantel Cast）。

**路由器定时重启**：页面断网时会显示「连不上 dashboard 服务」，恢复后最多 30 秒自动接上，不用刷新；每天 04:00 的自动刷新也要等服务能打开才会刷。还要注意两点：
- 在路由器里给 Nest Hub、跑 Mantel Cast 的机器、家里跑 Mantel 的机器设 DHCP 固定地址。重启后地址变了，写死 IP 的配置（`cast.host`、`public_url` 里的 IP）就失效了。Mantel Cast 按 `cast.device` 名字找设备的话，地址变了也能重新找到。
- 告警里配上 `maintenance`（见「告警推送」），每天重启那几分钟就不会推「离线」又推「已恢复」。

## 访问令牌

页面一旦放到外网就必须加令牌，否则任何人都能看到服务器状态、日程和余额。

```bash
./mantel token        # 生成一个随机令牌
```
```json
"tokens": [
  { "name": "nesthub", "token": "$MANTEL_TOKEN_NESTHUB" },
  { "name": "phone",   "token": "$MANTEL_TOKEN_PHONE" }
]
```
- 每个显示端一个，作废其中一个不影响别的。
- `scopes`：默认只能看。`"act"` 能执行操作（按钮），`"push"` 能往推送型数据源写数据。比如给 Nest Hub 和手机 `["act"]`，给电视只读，给家里的推送脚本 `["push"]`。
- 没配 `tokens` 时：谁都能看；操作和推送只接受局域网/本机直接来的请求。带 `X-Forwarded-For`、`Forwarded`、`X-Real-IP`、`Via`、`X-Forwarded-Host`、`X-Forwarded-Proto` 任何一个头的请求算经过反代，不算直接来的。`"$NAME"` 读环境变量，不用把令牌写进文件。
- 访问 `/?k=令牌` 会写入 cookie（HttpOnly，HTTPS 下带 Secure，有效期约 400 天），之后书签不带 `k` 也能打开；页面请求数据时也会把 `k` 放进 `Authorization` 头，所以设备不存 cookie 也没关系。
- 脚本或以后的 iOS App：`curl -H "Authorization: Bearer 令牌" https://…/api/v1/state`。
- 只有页面本身和 `/api/` 需要令牌；JS、CSS、字体、`/healthz` 不含数据，是公开的。
- 被拒绝的请求会打日志（`access: denied …`），带令牌打开页面也会记一条是哪个令牌、从哪个 IP（图片组件带 `?k=` 定时刷新，同一令牌每 10 分钟最多记一条）。
- `tokens` 为空表示不设防，只适合家里局域网。
- 不设防时只接受这些 `Host`：IP 地址、`localhost`、不带点的主机名、`.local`/`.lan`/`.home.arpa`/`.internal`/`.localhost` 结尾的名字、`public_url` 里的主机名，以及 `"allowed_hosts": ["dash.example.com", "*.example.com"]` 里列出的（通配符至少要带两级，`"*.com"` 会被拒绝）；其他一律返回 421。这是为了挡住 DNS rebinding：恶意网页把自己的域名解析到你的局域网 IP，浏览器就会把它当成同源，而请求又来自局域网，能按按钮。
- 同一台机器上的反代（nginx、Caddy）和隧道（`ssh -R`、某些内网穿透）转发进来的请求，来源地址都是本机。反代通常会带上面那些头，但 nginx 只写 `proxy_pass`、不加任何 `proxy_set_header` 时一个都不带，隧道也不带，外网请求就会被当成本机请求、能按按钮。只要前面有反代或隧道，就请配置 `tokens`。

## 状态持久化

`"state_file": "state.json"`：每分钟存一次，正常退出时再存一次（先写临时文件再改名，断电不会写坏）。重启后：
- 服务器曲线、探活历史和「断开多久」、AI 额度的 24 小时消耗记录都接着之前的；
- 天气和日程如果是 6 小时内存的也先显示出来，几秒后被新数据替换；
- 按名字对应，改了配置里的名字就相当于新的一项。

演示模式不会读写这个文件。

## 告警推送（Bark）

iPhone 装 [Bark](https://apps.apple.com/app/bark-customed-notifications/id1403753865)，复制里面的 key：

```json
"alerts": {
  "after": "2m",
  "maintenance": ["04:55-05:20"],
  "url": "https://hub.example.com/?k=手机（phone）的令牌",
  "bark": { "key": "$BARK_KEY", "server": "https://api.day.app", "level": "timeSensitive" }
}
```
```bash
./mantel alert-test     # 发一条测试通知
```
- **服务器 / 服务探活**：连续断开超过 `after`（默认 2 分钟）才推送「离线」，恢复时推送「已恢复（中断 N 分钟）」。一两次抖动不会吵你。
- **AI 额度**：给 `quotas[]` 加 `"alert_below": 10`，余额低于它立即推送一次，充值后推送「已充值」。
- **订阅用量**：窗口超过 `plans.alert_at` 且还有重置次数、或重置快过期时推送，见「订阅用量和重置」。
- 同一轮里多项一起出问题（比如家里断网）合成一条，手机只响一次。
- **维护时段**：`maintenance` 是每天预计会断网的时段（`HH:MM-HH:MM`，按 `timezone`，可以跨午夜），比如路由器定时重启。时段内服务器和探活断开不推送；时段结束后还没恢复的，再过 `after` 推送「离线」。额度、订阅用量和数据源规则不受影响。
- 推送失败会在下一轮（15 秒后）重试，连续失败时逐步拉长间隔，最长 10 分钟。
- 不想被某台服务器或某个探活打扰：在那一项加 `"mute": true`。
- 点通知打开 `alerts.url`（默认 `public_url`），放外网时记得带上令牌。
- `server` 可以换成自建的 bark-server。演示模式下不推送。
- 其他 `bark` 字段原样交给 Bark：`group`（通知分组，默认用顶层 `title`）、`sound`（铃声名）、`icon`（图标 URL）、`level`（离线类告警的级别，默认 `timeSensitive`）。
- 已知限制：进程重启时如果某项正好处于离线状态，过 `after` 后会再推一次。

## 数据源

| 模块 | 配置 | 说明 |
|---|---|---|
| 服务器 | `servers[]`：`name`、`url`（node_exporter 的 `/metrics`）、`mount` | CPU、内存、磁盘、网络、负载、温度、运行时间。每台服务器装 [node_exporter](https://github.com/prometheus/node_exporter)，默认端口 9100，不需要 Prometheus |
| 日程 | `calendars[]`：`name`、`url`、`color` | ICS 链接，`webcal://` 也行。展开重复事件、例外、改期 |
| 天气 | `weather`：`latitude`、`longitude`、`name` | Open-Meteo，免 key |
| 服务探活 | `probes[]`：`url`、`expect`、`interval`、`insecure` | HTTP GET，显示延迟；断开时红点闪烁并显示断开多久 |
| 订阅用量 | `plans`：`cliproxy.url`、`cliproxy.key` | Claude / Codex 的用量窗口和重置次数，经 CLIProxyAPI 读取，见下文 |
| AI 额度 | `quotas[]`：`provider`、`api_key` | 内置 `deepseek`、`moonshot`、`siliconflow`、`openrouter`；其他服务用 `http`，用点路径取 JSON 字段（如 `data.balance`、`items.0.left`）。`base_url` 换成兼容的中转地址。会根据历史算日耗和大约还能用多久 |

日历链接从哪拿：
- Google 日历：网页版 → 设置 → 某个日历 → 「整合日历」→「iCal 格式的私密地址」。
- iCloud 日历：日历 App 里右键日历 →「共享日历」→ 勾「公开日历」，复制 `webcal://` 链接。

node_exporter 安装（Debian/Ubuntu）：`sudo apt install prometheus-node-exporter`；Docker：`docker run -d --net=host --pid=host -v /:/host:ro,rslave prom/node-exporter --path.rootfs=/host`。

## 页面和组件

不写 `pages` 时是上面截图那一页经典布局。写了就完全由你决定，见 `config.example.json`：

```json
"display": { "home": "main", "idle_return": "90s", "rotate": "", "rotate_quiet": "23:00-07:00", "text_size": "medium", "theme": "petrol" },
"pages": [
  { "name": "main", "title": "总览", "widgets": [
    { "type": "clock", "at": [0, 0, 4, 2] },
    { "type": "stat", "title": "当前功率", "source": "power", "value": "state", "unit": "W",
      "warn": 3000, "crit": 4500, "trend": true, "window": "30m", "at": [4, 0, 3, 2] }
  ]}
]
```

- `at: [列, 行, 宽, 高]`，网格默认 12 列 × 8 行（`display.cols/rows` 可改）。重叠、越界启动时直接报错。
- 不想算坐标就用模板或自动排版，见下一节。
- 多页：手指左右拖（有阻尼和回弹）、点底部圆点、键盘方向键；`?page=home` 直接打开某页。
- `idle_return`：没人碰多久回到 `home` 页（默认 90 秒，`"0s"` 关闭）。`rotate: "20s"` 无人操作时自动轮播，`rotate_quiet` 时段内不轮播。
- `id`：组件的名字，默认 `页名.序号`，全局不能重复；图片组件的地址 `/api/v1/image/<id>` 用的就是它。
- `accent`：组件的强调色，`seaglass`/`sodium`/`flare`/`bone`/`sky`/`plum`/`lichen`，或任意 CSS 颜色。
- 组件取数：`source` 是数据源名（内置的写 `server:nas`、`probe:Blog`、`quota:DeepSeek`、`weather`、`calendar`），`value`/`items`/`label` 等都是点路径（`data.items.0.name`、`items.#` 取长度、`-1` 取最后一个）。
- 数据过期（超过 `stale_after`）或拉取失败：组件变灰，标题旁写「更新失败，显示 5 分钟前的数据」，不会突然空白。

### 字号和配色

```json
"display": { "text_size": "large", "theme": "petrol", "theme_night": "ember" }
```

- `text_size`：`small` / `medium`（默认）/ `large` / `xlarge`，整页文字按 0.9 / 1 / 1.15 / 1.3 倍缩放。小字有下限（最小 11px），1024×600 的 Nest Hub 上也看得清。放不下时组件先收起次要内容（比如天气的逐小时曲线），不会溢出。
- `theme`：配色，默认 `petrol`。`theme_night`：夜里（22:00–05:00）换用的配色，不写就沿用 `theme`，只把底色压暗。
- 写错名字启动时直接报错。
- 一份配置给 Hub 和 Hub Max 两台用时，可以在地址上单独指定，优先于配置：`?size=xlarge`、`?theme=paper`、`?theme_night=ember`（名字不对就忽略）。Mantel Cast 投的地址里也能带。
- 在地址后加 `&preview=themes`（如 `/?k=令牌&preview=themes`）并排看所有配色。

| 配色 | 描述 | 适合 |
|---|---|---|
| `petrol` | 深青底、米白字、海玻璃绿（默认） | 大多数客厅 |
| `graphite` | 中性石墨灰、冷蓝强调 | 想要不带色偏的界面 |
| `midnight` | 深海军蓝、长春花蓝强调 | 偏冷的暗色环境 |
| `forest` | 深绿底、叶绿强调 | 木质、暖色家居 |
| `ember` | 暖褐底、橄榄绿表示正常、琥珀表示提醒，几乎不发蓝光 | 卧室，夜间（推荐作 `theme_night`） |
| `contrast` | 纯黑底、纯白字、亮黄强调 | 远距离看、视力不好 |
| `paper` | 浅米色底、深色字 | 白天光线充足的房间 |

| 组件 | 用途 | 主要字段 |
|---|---|---|
| `clock` `weather` `agenda` | 时钟、天气、日程 | — |
| `servers` `probes` `quotas` | 内置的服务器、探活、额度 | `names` 只显示其中几个 |
| `plans` | Claude / Codex 订阅用量和重置键（需要 `plans` 配置） | `names` 按邮箱/套餐筛选 |
| `stat` | 一个大数字 + 变化量 + 迷你曲线 | `value` `unit` `format`(int/percent/bytes/bitrate/duration/money) `decimals` `scale` `warn` `crit` `invert` `trend` `window` |
| `gauge` | 半圆仪表 | `value` `max`/`max_value` `warn` `crit` `invert` |
| `timeseries` | 多条曲线 | `series[]`: `label` `value` `color`；或 `points` 指向数据源里现成的数组 |
| `bars` | 柱状（如每天花费） | `items` `label` `item_value` |
| `status` | 一格一个状态点（容器、CI、设备） | `items` `label` `state` `detail` |
| `list` | 列表，可带徽标、数值 | `items` `label` `item_value` `detail` `badge` `state` `limit` |
| `progress` | 进度条（下载、任务） | `items` 里 `value` 0–100 |
| `text` | 一段文字（留言板） | `text` `size`(s/m/l/xl) |
| `countdown` | 倒计时 | `to: "2026-12-24T18:00"` |
| `image` | 图片/摄像头快照，服务器代取 | `url` `headers` `refresh` |
| `buttons` | 一块大按钮 | `actions` |

`state` 字段认识常见写法：`ok/up/running/healthy/success/true` 是绿，`warn/degraded/pending/in_progress/restarting/paused` 是黄，`down/failed/error/exited/unhealthy/false` 是红，`off/stopped/idle/disabled` 是灰。数值没有 state 时按 `warn`/`crit` 阈值判断。

## 三种摆法

每页任选一种，也可以混用：

**1. 坐标**：每个组件写 `at`，完全自己定。

**2. 预设模板**：页面写 `template`，组件写 `slot`：

```json
{ "name": "work", "template": "kpi", "widgets": [
  { "slot": "k1", "type": "stat", "source": "github", "value": "stargazers_count" },
  { "slot": "main", "type": "list", "source": "todo" },
  { "slot": "side", "type": "text", "source": "notes" }
]}
```

| 模板 | 样子 | 格子 |
|---|---|---|
| `sidebar` | 左边窄栏三格，右边一大块 + 底部两格（经典布局） | `side1` `side2` `side3` `main` `foot1` `foot2` |
| `kpi` | 顶部四个数字卡，下面一大一小 | `k1`–`k4` `main` `side` |
| `kpi-grid` | 顶部四个数字卡，下面田字四格 | `k1`–`k4` `a` `b` `c` `d` |
| `focus` | 上面一大块，下面三格 | `top` `b1` `b2` `b3` |
| `hero` | 左边一大块，右边上下两格 | `hero` `top` `bottom` |
| `halves` | 左右两半 | `left` `right` |
| `thirds` | 三竖栏 | `left` `center` `right` |
| `quad` | 田字四格 | `tl` `tr` `bl` `br` |
| `grid6` | 三列两行六格 | `a`–`f` |
| `full` | 整屏一个 | `full` |

格子可以空着；也可以在模板页里再放写了 `at` 的组件。网格改成 24×16 这类 12×8 的倍数时模板会等比放大。

**3. 自动排版**：页面写 `"auto": true`，组件什么位置都不写，按顺序从左上往右下填：

```json
{ "name": "more", "auto": true, "widgets": [
  { "type": "probes" }, { "type": "quotas" },
  { "type": "stat", "source": "github", "value": "forks_count" },
  { "type": "list", "source": "backup", "span": [6, 3] }
]}
```

- 每种组件有默认大小（数字卡 3×2、曲线 6×3、列表 4×4、服务器 8×5……），想要别的大小写 `span: [宽, 高]`。
- 小组件会补进大组件留下的空位；放不下时先试着缩小（不小于该组件能看清的最小尺寸），还放不下就自动续一页，叫 `more-2`、`more-3`。
- 排完后组件会往右边、下面的空位长大一些（最多到原来的两倍），减少空洞。
- 可以和模板一起用：`"template": "focus", "auto": true`，写了 `slot` 的先占格子，其余的自动填进剩下的地方。

**看排出来的样子**，不用上设备：

```bash
./mantel layout -config config.json    # 每页画成字符网格，列出每个组件的位置
./mantel layout -templates             # 看全部模板
```
```
more「更多」 auto
  AAAABBBBCCCC   A probes 服务 [0 0 4 3]
  AAAABBBBCCCC   B quotas AI 额度 [4 0 4 3]
  AAAABBBBCCCC   C stat Forks [8 0 4 4]
  DDDDDDDDCCCC   D list 备份 [0 3 8 5]
```

## 通用数据源

```json
"sources": [
  { "name": "power", "type": "http_json", "url": "http://homeassistant.local:8123/api/states/sensor.house_power",
    "headers": { "Authorization": "Bearer $HA_TOKEN" }, "interval": "10s" },
  { "name": "containers", "type": "exec", "command": ["docker", "ps", "-a", "--format", "{{json .}}"], "interval": "30s" },
  { "name": "backup", "type": "push", "stale_after": "26h",
    "alerts": [{ "title": "备份", "path": "status", "not": "ok" }] }
]
```

- `http_json`：定时请求（默认 GET，`method`/`body` 可改），`select` 只取其中一段。
- `exec`：定时跑命令（不经过 shell），`dir` 是工作目录，`env` 追加环境变量（值原样传入，不展开 `$NAME`）。输出是 JSON 就用 JSON；每行一个 JSON（`docker ps`、`jq -c`）变成 `{"items": [...]}`；单个数字变成 `{"value": n}`；其他文字变成 `{"text": "..."}`。
- `push`：别人推过来。适合家里的脚本、cron、外网拉不到的内网数据、手机快捷指令：
  ```bash
  curl -X POST -H "Authorization: Bearer $EDGE_TOKEN" -d '{"status":"ok","jobs":[...]}' https://hub/api/v1/push/backup
  curl -X POST -H "Authorization: Bearer $EDGE_TOKEN" "https://hub/api/v1/push/notes?merge=1" -d '{"text":"晚饭在冰箱"}'
  curl -X POST -H "Authorization: Bearer $EDGE_TOKEN" "https://hub/api/v1/push/temp?value=21.5"
  ```
  `?merge=1` 只覆盖给出的字段。需要 `push` 权限的令牌（或无令牌时从局域网直接来）。
- `alerts[]`：数据源上的告警规则，`above`/`below`/`not`，`after` 持续多久才报。走同一个 Bark。
- 曲线和趋势需要的历史由服务器自己采样保存（按图表的 `window`），重启也不丢。

## 订阅用量和重置（Claude / Codex）

Claude Pro/Max 和 ChatGPT Plus/Pro（Codex）按「5 小时」「每周」窗口限量，官方还会不定期送「免费重置」次数（Claude 叫 Reset for free，Codex 是 rate limit reset credits），有过期时间，不用就浪费。这一块把它们放到屏幕上：

- 每个账号一行：各窗口用了多少、多久后恢复；攒了几次重置、最近一次几时过期。
- 重置**现在就能用**时，账号行尾出现「重置」键，按住确认；服务器去官方兑换，成功后窗口归零，并顺手清掉 CLIProxyAPI 对这个账号的冷却。
- Claude 的重置要用到限额才允许（官方规则），没到时显示「还没到限额，到了才能重置」；点账号行能看到原因。
- 告警：某个窗口超过 `alert_at`（默认 90%）而账号还有重置次数时推一条；重置快过期（`expiry_alert`，默认提前 24 小时）也推一条。

登录不放在这里，而是放在 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 里（它本来就管理这些 OAuth 登录、自动刷新 token）。本程序只拿它的**管理密钥**，通过管理接口 `POST /v8/management/requests/api-call` 让它代为请求官方接口，官方 token 从不离开 CLIProxyAPI：

```json
"plans": {
  "cliproxy": { "url": "https://cpa.example.com", "key": "$CPA_MANAGEMENT_KEY" },
  "interval": "2m",
  "accounts": [],
  "alert_at": 90,
  "expiry_alert": "24h",
  "reset": true
}
```

- `url` 填 CLIProxyAPI 的地址（贴管理页地址也行，会自动去掉 `/v8/management`）；需要在 CLIProxyAPI 里开启远程管理。
- CLIProxyAPI 用自签证书时，把签发它的 CA 证书（PEM）路径写进 `cliproxy.ca_file`（如 `"/etc/mantel/cpa-ca.pem"`，可用 `$ENV`），在系统根证书之外额外信任它。也可以写 `"insecure": true` 完全跳过证书校验，但这样管理密钥会发给未经验证的对方，启动时会打一条警告；两者不能同时写。
- `accounts` 为空时显示 CLIProxyAPI 里所有 Claude、Codex 登录；写了就只显示名字/邮箱/备注里含这些字的。
- `reset: false` 只看不按。按钮背后是内置操作 `plan-reset`，同样要求令牌有 `act` 权限。
- 组件：`{ "type": "plans", "title": "订阅用量" }`，`names` 可以按邮箱/套餐筛选。不写 `pages` 的经典布局里不会自动出现，需要自己放。

先在命令行确认能连上：

```bash
mantel plans -config config.json            # 列出账号、窗口、重置次数
mantel plans -config config.json -json      # 原始 JSON
mantel plans -config config.json -reset codex:codex-me@example.com.json   # 真的花掉一次重置
```

注意：
- 读用量和兑换重置用的是 Claude Code / Codex 客户端自己调用的接口，不是公开 API，官方随时可能改；请求格式参照 CLIProxyAPI 管理中心的实现。
- 兑换请求如果中途断了、结果不确定，屏幕会说「结果不确定」。10 分钟内再按一次会重发**同一个请求号**，官方会认出来，不会再扣一次。

## 操作（按钮）

```json
"actions": [
  { "name": "relay-reset", "title": "重置额度", "type": "http",
    "url": "https://relay.example.com/api/user/reset", "headers": { "Authorization": "Bearer $RELAY_TOKEN" },
    "confirm": "hold", "cooldown": "1m", "refresh": ["quota:Relay"], "notify": true },
  { "name": "container-restart", "title": "重启", "type": "exec",
    "command": ["docker", "restart", "{param}"], "param": { "pattern": "[a-z0-9][a-z0-9_.-]*" } }
]
```

放在哪：
- `buttons` 组件的 `actions`：大按钮。
- 内置项上：`quotas[].actions`、`servers[].actions`、`probes[].actions`。额度低于 `alert_below` 时，行尾直接出现按钮；点这一行弹出面板列出全部操作。
- 列表类组件的 `item_actions`：点某一行弹出面板，`param` 字段的值（如容器名）作为 `{param}` 传给操作。

执行：
- 设备只发 `POST /api/v1/actions/<name>`（带 `param`），真正的 URL、密钥、命令都只在服务器上。需要 `act` 权限，同源检查防跨站请求。
- `{param}` 会被替换进 URL（转义）、body（JSON 转义）、header、命令参数；必须在 `param.values` 里或完整匹配 `param.pattern`，否则拒绝。不经过 shell，没有注入；配置里把 `{param}` 写进 URL 的协议/主机部分，或写进 `sh -c` 脚本，启动时会直接报错（要用 shell 就把它作为位置参数传：`["sh","-c","docker restart \"$1\"","sh","{param}"]`）。`exec` 操作还会拒绝以 `-` 开头的参数（防止被当成命令选项），除非它原样写在 `param.values` 里；每个操作最多记住最近 32 个不同参数的结果。
- `confirm`：`hold`（默认，按住约 1 秒，进度条走满才执行，防误触）、`tap`（点一下变成「再点一次确认」）、`none`（开灯这类无所谓的）。
- `http` 操作默认 `POST`，`method` 可改；`exec` 操作同样有 `dir`、`env`。
- `cooldown` 防连点，按操作计、不分参数（刚重启了 web，紧接着重启 db 也要等）；同一操作执行中再点会提示「正在执行」。`expect` 指定期望的 HTTP 状态码，否则任何 2xx 算成功。
- 结果：顶部弹一条提示（成功绿、失败红，消息取返回 JSON 里的 `message`/`msg`/`detail`/`error`），按钮下方显示「3 分钟前：已重置」。`refresh` 列出的数据源立即重新拉取，屏幕上马上看到变化。`notify: true` 再推一条 Bark，日志里记谁在什么时候点了什么。

## 接口

| 路径 | 说明 |
|---|---|
| `GET /api/v1/dashboard?v=版本` | 页面用的：布局（版本变了才带）+ 每个组件的数据。iOS 小组件等也可以直接读 |
| `GET /api/v1/state` | 原始状态。没有 `act` 权限的令牌看不到是谁执行的操作（`by`），探活地址里的账号密码和查询参数也会被去掉 |
| `POST /api/v1/actions/{name}` | 执行操作，body `{"param": "..."}`，需 `act` |
| `POST /api/v1/refresh/{source}` | 立刻重新拉一个数据源，需 `act`；`{source}` 可以是自定义数据源名，也可以是内置的 `server:<名字>`、`probe:<名字>`、`quota:<名字>`、`weather`、`calendar`、`plans` |
| `POST /api/v1/push/{source}` | 推数据，需 `push` |
| `GET /api/v1/image/{widget}` | 图片组件的代取（不接受 SVG） |

## 手机睡眠状态（给 Mantel Cast 的自动安静用）

Mantel Cast 夜里会让 Nest Hub 安静（规则见 Mantel Cast 的 README），其中一个信号是 iPhone 的「睡眠」专注模式。手机把状态报给 Mantel，Mantel Cast 再来读：

1. 快捷指令 App → 自动化 → 新建 → 专注模式 → 选「睡眠」→ 勾「打开时」→ 选「立即运行」。
2. 操作选「获取 URL 内容」：
   - URL：`https://<Mantel>/api/v1/quiet?asleep=1`
   - 方法：POST
   - 头部：`Authorization` = `Bearer <令牌>`（令牌需要 `act` 或 `push` 权限，比如示例里的 `phone`）
3. 再建一条，勾「关闭时」，URL 改成 `asleep=0`。

接口：`POST /api/v1/quiet?asleep=1|0`（或 JSON `{"asleep": true}`）；`GET /api/v1/quiet` 返回最近一次报告（只读令牌即可）。

## Custom Receiver 页面模式

Nest Hub 上要触摸、不超时，需要在 Google Cast 后台注册自己的 Custom Receiver（步骤见 Mantel Cast 的 README）。Mantel 这边要做的：

- 页面走 HTTPS 且证书受信任：用自己的域名 + Let's Encrypt（DNS 验证即可，域名解析到内网 IP 也行），在配置里设 `tls_cert`、`tls_key`、`public_url`，或者放在反代后面。
- Receiver 的网址填 `https://你的域名/?receiver=1&k=令牌`。`?receiver=1` 会加载 Cast Receiver SDK，开启 `disableIdleTimeout` 和 `touchScreenOptimizedApp`。

## 动效与性能

Nest Hub Max 的芯片很弱，所以动画几乎只动 `transform` 和 `opacity`：

- 翻页：整条轨道 `translateX`，到页后组件带一点视差地跟上；页名在底部闪现。
- 数字滚动补间、仪表弧线和进度条缓动、柱子带回弹升起、列表新增项滑入/完成项滑出、进行中的进度条有流光、异常状态点向外扩散。
- 按钮：按住时进度从左向右填满，执行中转圈，失败抖一下。

- 每台服务器一条实时滚动的 CPU 曲线（核心视觉）：每来一个采样，整条曲线用一次线性 `translateX` 平移一格，合成器完成，不重绘。
- 服务器离线时曲线拉平变红、整行慢速闪烁；服务断开时红点向外扩散。
- 数字补间、内存/磁盘条 `scaleX` 过渡、时钟数字翻滚、即将开始的日程描边呼吸、天气图标（雨滴、雪、闪电、云飘动）。
- 背景随时间段（凌晨、白天、傍晚、深夜）缓慢变色；页面每天 04:00 自动刷新一次。
- 尊重 `prefers-reduced-motion`。

屏幕按 1280×800 设计，字号随视口等比缩放，桌面浏览器里预览的效果和设备上一致。

## 目录

```
main.go                  CLI：run / layout / token / alert-test / plans
auth.go                  访问令牌（?k= → cookie，Bearer）
internal/alert           告警规则 + Bark
internal/calendar        ICS 解析 + RRULE 展开
internal/collect         node_exporter、天气、探活、AI 额度、通用数据源、演示数据
internal/dash            页面布局、组件数据解析、历史采样、图片代取
internal/action          服务器端操作（http / exec / 订阅重置）
internal/plans           Claude / Codex 用量和重置（经 CLIProxyAPI）
internal/jpath           点路径取 JSON 字段
api.go                   /api/v1 接口
internal/state           共享状态 + 持久化
web/                     页面（嵌入二进制）
```

## 许可证

[MIT](LICENSE)

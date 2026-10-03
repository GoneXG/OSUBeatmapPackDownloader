# OSU 曲包下载器（osu! Beatmap Pack Downloader）

一个用于批量下载 osu! 官方曲包（Beatmap Packs）的命令行小工具。启动后按菜单选择想要的曲包类型（常规 / 精选艺术家 / 锦标赛 / 社区喜爱 / 聚光灯等），程序会驱动你本机浏览器抓取官方列表、构造并校验下载链接，再调用 aria2 并发下载到本地。由 Codex+DeepSeek 辅助创建。

> 使用请看「快速开始」，想了解背后的原理请看「工作原理」。

## 为什么需要浏览器脚本

osu! 官网 `osu.ppy.sh` 会按客户端指纹拦截非浏览器请求：Go、curl、.NET 直接请求一律拿到 `403` 与 `Just a moment...` 的 JS 挑战页，而同一台机器上的真实 Chromium 可以正常打开。所以从 v1 起：

- **抓取与链接解析放回浏览器**：由油猴脚本在你自己已通过校验的浏览器里同源抓取，结果通过 `127.0.0.1` 上的回环服务回传给本程序；
- **下载仍然由本程序负责**：`packs.ppy.sh` 不受拦截，aria2 照常并发下载；
- **链接靠候选空间 + HEAD 校验确定**：抽检校验锚点、按区段结构推断其余，失败链接经浏览器解析修复；不是"猜一个文件名就下载"。

## 功能一览

- 覆盖 osu! 官网 7 类曲包：常规、精选艺术家、锦标赛、社区喜爱计划、艺术家/专辑、聚光灯、主题
- 常规 / 锦标赛 / 社区喜爱 / 聚光灯支持按游戏模式筛选（osu!、osu!catch、osu!taiko、osu!mania 等）
- 常规分类支持限定曲包编号区间（如 `90-168`）或「最新 N 个」后再下载
- 下载链接在有限候选空间内构造，并逐个用 HEAD 校验；校验失败的链接会按区段学习命名规则批量修复，最后还可用浏览器逐个解析兜底 调用 aria2 高并发下载，带断点续传、逐曲包实时进度；不装 aria2 也可退化为「仅保存链接」
- 自动生成 `URL/urls.txt`（全部直链）与 `URL/failed.txt`（失败记录）
- 脚本不可用时可用 `-packs <本地列表文件>` 离线兜底
- 可选自动解压（`-unzip`）：边下载边解压，`.zip` 与 `.7z` 双格式，产物可直接拖进 osu!；默认关闭

## 快速开始（Windows）

### 1. 准备

你需要 Windows 电脑、网络、一个能登录 osu! 的浏览器。请保持以下文件在同一个目录：

| 文件 / 目录 | 作用 |
| --- | --- |
| `osu-pack-downloader.exe` | 程序本体 |
| `tools/aria2c.exe` | 下载引擎（可选，但强烈建议保留） |
| `userscript/osu-pack-bridge.user.js` | 浏览器桥接脚本（必须安装到油猴脚本管理器） |

> `aria2c` 不是 osu! 官方工具，它负责真正的高并发下载。没有它程序也能运行，只是只能保存链接、无法下载文件。

### 2. 安装浏览器桥接脚本（必做，只需一次）

1. 安装一个用户脚本管理器，二选一：
   - [Tampermonkey](https://www.tampermonkey.net/)（Chrome / Edge / Firefox 均有）
   - [Violentmonkey](https://violentmonkey.github.io/)
2. 安装桥接脚本，二选一：
   - **在线安装（推荐）**：在脚本管理器里打开下面这个地址，点「安装」：

     ```text
     https://raw.githubusercontent.com/GoneXG/OSUBeatmapPackDownloader/master/userscript/osu-pack-bridge.user.js
     ```

   - **离线安装（国内访问代码托管原始地址经常失败时用这个）**：打开脚本管理器的「新建脚本」，把下方[完整脚本代码](#完整脚本代码)整段粘贴进去并保存。
3. 在浏览器里登录 osu! 账号（脚本抓取的是你自己的登录会话）。
4. 打开 <https://osu.ppy.sh/beatmaps/packs?type=standard> 验证脚本已生效：页面右下角会出现「osu! Pack Bridge」浮层提示。正常浏览时它是安静的，只有本程序带着作业信息唤起页面时才会开始抓取。

> 查看当前脚本版本：在脚本管理器的脚本列表里，`osu! Pack Bridge` 应显示 `1.0.2`（或更新）。osu! 官网偶尔改版会让旧版脚本失效，遇到抓取异常先点一次「检查更新」。

### 3. 启动程序

- 方式 A（最简单）：在文件管理器中**双击** `osu-pack-downloader.exe`，会弹出黑色命令行窗口；
- 方式 B：在项目目录打开 PowerShell 或 CMD，输入：

```powershell
.\osu-pack-downloader.exe
```

### 4. 跟着菜单操作

整个过程是问答式的，输入编号后按回车即可：

1. 选择曲包分类，例如输入 `1` 选择「常规」；
2. 部分分类还会让你选择游戏模式，例如输入 `3` 选择 osu!mania；
3. 程序会自动拉起默认浏览器打开曲包列表页，并等待脚本握手：
   - 浏览器里的脚本会先检查登录状态，未登录会立即停止并提示；
   - 登录正常时脚本顺序翻页抓取（页面右下角显示进度），把列表回传给程序；
4. 如果选了「常规」，会询问是否限定编号区间，直接回车 = 全部保留；
5. 程序构造并校验下载链接：先按命名规则构造候选，再抽检一部分曲包用 HEAD 校验、其余按区段结构推断；有失败链接时会经浏览器解析并学习命名规则，批量修复后复验，直到一次抽检没有失败链接或本轮没有进展；
6. 询问下载方式时：
   - 输入 `1`：调用 aria2 下载（推荐）；
   - 输入 `2`：仅把链接保存到 `URL/urls.txt`，方便以后用其它工具下载；
7. 下载完成后，到 `download/` 目录查看压缩包。

控制台大致长这样：

```text
==============================================
  osu! Beatmap Pack 曲包下载器
==============================================

请选择曲包分类:
  [1] 常规
  [2] 精选艺术家
  [3] 锦标赛
  ...
请输入编号: 1
```

### 5. 下载结果去哪了

所有压缩包都混存在同一个目录（默认 `download/`，位于程序所在目录下）。压缩包里装的是 `.osz` 谱面文件，程序**默认不会自动解压**：请手动解压，再把 `.osz` 拖进 osu! 窗口（或直接双击 `.osz`）即可导入。想省掉手工解压，见「自动解压（可选）」。

### 6. 常用启动参数

| 参数 | 作用 | 示例 |
| --- | --- | --- |
| `-dir <路径>` | 修改下载目录 | `.\osu-pack-downloader.exe -dir "D:\osu曲包"` |
| `-proxy <地址>` | 校验/下载走代理（不填则使用系统代理） | `.\osu-pack-downloader.exe -proxy http://127.0.0.1:7890` |
| `-lookup-concurrency <数量>` | 链接校验与浏览器解析的并发数（默认 4，上限 8） | `.\osu-pack-downloader.exe -lookup-concurrency 8` |
| `-verify-rate <比例>` | 链接抽检比例，`0.01`~`1`（默认 `0.1`，即逐条 HEAD 校验约十分之一的曲包，其余按区段结构推断）；`1` = 逐条全量校验 | `.\osu-pack-downloader.exe -verify-rate 1` |
| `-packs <文件>` | 直接读本地曲包列表文件（JSON 载荷），跳过浏览器抓取 | `.\osu-pack-downloader.exe -packs packs.json` |
| `-progress <模式>` | 下载进度显示：`bar` 原地刷新的进度块（默认）、`line` 每次输出一整块文本、`off` 不显示 | `.\osu-pack-downloader.exe -progress line` |
| `-unzip` | 下载时自动解压压缩包（默认关闭） | `.\osu-pack-downloader.exe -unzip` |
| `-unzip-dir <路径>` | 指定解压目录（默认取下载目录同级的 `unzip` 目录） | `.\osu-pack-downloader.exe -unzip -unzip-dir "D:\unzip"` |
| `-unzip-layout <布局>` | `flat`（默认，全部平铺）或 `per-pack`（每个曲包一个子目录） | `.\osu-pack-downloader.exe -unzip -unzip-layout per-pack` |
| `-unzip-delete` | 解压成功后删除对应压缩包（默认保留） | `.\osu-pack-downloader.exe -unzip -unzip-delete` |
| `-nopause` | 出错后不等待回车直接退出（脚本调用时用） | `.\osu-pack-downloader.exe -nopause` |

> 出错时程序会停在「按回车键关闭窗口...」，方便双击运行时看清原因；不想停留就加 `-nopause`。
> 输出被重定向到文件或管道时，`bar` 会自动按 `line` 处理，不会写入回车等控制字符。
> 程序源码与脚本都在仓库里，版本不匹配时程序会直接提示「协议版本不匹配」，此时更新脚本或重新编译即可。

## 自动解压（可选）

默认**关闭**。加上 `-unzip` 后，程序会在下载的同时把每个完成的曲包解压成可直接导入 osu! 的 `.osz`：

```powershell
.\osu-pack-downloader.exe -unzip
```

- **双格式**：`.zip` 用 Go 标准库、`.7z` 用纯 Go 库，**不需要额外安装解压工具**（除 `aria2c` 外仍只依赖单个 exe）。
- **目标目录**：默认解压到下载目录的**同级** `unzip` 目录——默认下载目录 `.\download\` 对应 `.\unzip\`；用 `-dir "D:\osu曲包"` 时对应 `D:\unzip\`。可用 `-unzip-dir <路径>` 显式覆盖。
- **两种布局**：
  - `-unzip-layout flat`（默认）：所有 `.osz` 直接落在解压目录下，方便**一次全选**拖进 osu!。代价是全量解压会在同一目录产生上万个文件，部分文件管理器会变慢；
  - `-unzip-layout per-pack`：每个曲包一个与压缩包同名的子目录，便于追溯某个 `.osz` 来自哪个曲包，目录更整洁。
- **边下边解**：每完成一个压缩包就立即解压，不用等整批下载结束；正在下载（存在 `.aria2` 控制文件）的压缩包不会被解压。
- **不覆盖已有文件**：目标文件已存在时跳过并计数，重复运行不会破坏你已经整理过的文件；按曲包布局下子目录已存在时会沿用该目录继续补解缺失文件。
- **默认保留压缩包**：只有加 `-unzip-delete` 才会在**解压成功后**删除压缩包；解压失败的曲包一律保留，方便重试。
- **进度与失败**：解压阶段显示「已处理/总数、成功、失败」，结束后列出失败曲包。
- **安全**：会写出到解压目录之外的条目（`../` 上跳、绝对路径等）一律拒绝并记为失败，不会在解压目录之外生成任何文件。

> 磁盘占用提醒：解压会与压缩包并存，短时间内接近**两倍**空间；选择 `-unzip-delete` 或稍后手工删除压缩包即可回落到一份。
> 解压失败**不会**影响下载结果，也不会让程序整体判为失败：下载成功数、`URL/failed.txt` 都只反映下载阶段。

## 从源码编译（可选）

如果你拿到的是源码而不是现成的 exe：

1. 安装 [Go 1.21 或更高版本](https://go.dev/dl/)；
2. 在项目目录打开终端，执行：

```powershell
go build -o osu-pack-downloader.exe .
```

3.（可选）把 [aria2c.exe](https://github.com/aria2/aria2) 放进 `tools/` 目录，或加入系统 PATH。

国内网络编译慢可先设置镜像加速：

```powershell
go env -w GOPROXY=https://goproxy.cn,direct
```

## 工作原理简述

### 1. 抓取发生在你自己的浏览器里

程序启动时会在 `127.0.0.1` 上开一个小型 HTTP 服务，并把「本次运行的一次性凭据 + 端口 + 目标分类」写进曲包列表页 URL 的**片段**（`#` 之后）里，然后拉起默认浏览器。片段不会被发送给站点，所以凭据既不进站点日志，也不会出现在查询参数里。

浏览器里的桥接脚本看到片段后才会开始工作：

- 先校验凭据（脚本→本程序方向，来源校验为辅）；
- 先检测登录态。未登录时不抓取、不解析，直接上报「未登录」；
- 已登录时从第 1 页开始**顺序**翻页（节流、不并发）抓取列表，每页提取 `tag`、官网名称、详情页地址，最后把整份列表回传；
- 抓取完成后脚本会保持轮询，等待程序下发的「解析某个曲包详情页」任务。



### 2. 直链是「候选空间 + 校验」得到的

`packs.ppy.sh` 上的文件名规则是：

```text
<TAG> - <名称变体> Beatmap Pack #<编号><扩展名>
```

其中名称变体只有两种（官网显示名原样，或去掉 `osu!` 的历史短名），扩展名只有 `.zip` 和 `.7z`，因此每个曲包有 4 个候选。程序逐个候选做 **HEAD** 校验，命中即采用，所以规则可以是**学习到的**，而不是写死的。

### 3. 抽检 + 区段推断 + 定向修复 + 浏览器兜底

老区段与新区的命名规则不一致，且名称变体与扩展名会各自独立翻转。校验是逐个直链做 HEAD，本机实测：短时间可达约 12 次/秒，持续压测后站点会限速到约 4 次/秒——常规分类的 osu! 区段有 1873 个曲包，逐条全量校验要 7~8 分钟。而命名规则是按区段成片变化的，逐条探测性价比很低。因此程序：

1. 先按实测频率排好候选顺序（新区段=官网名+`.zip`、老区段=历史短名+`.7z`），让绝大多数曲包第一个或第二个候选就命中；
2. **按比例抽检**（默认 `-verify-rate 0.1`，抽样覆盖整个列表而不是只取前缀）逐条 HEAD 校验；
3. 没被抽到、也没有校验失败的曲包，**按最近的已验证曲包所用的区段结构推断链接**，不额外发请求；
4. 抽到的锚点如果分属两种结构，说明中间夹着一次翻转，那一段**改为逐条确认**（实测 S1296~S1302 就是这样的翻转带）；
5. 对失败的链接取其在列表中的 ±7 邻近曲包（含自身，窗口去重）交浏览器解析真实链接；
6. 相邻两个失败链接解析出的名称变体与扩展名一致时，按该结构**批量构造两者之间当前校验失败的**链接（已通过校验的链接不会被改写），并重新校验；
7. 解析出真实链接的失败曲包直接采用该链接；
8. 重复直到一次抽检没有失败链接；若某一轮失败集合没有减少（无进展），则终止循环，把剩余曲包记入 `failed.txt`。

实测（常规 / osu!，1873 个曲包）：

| 模式 | 逐条校验/推断 | 耗时 | 结果 |
| --- | --- | --- | --- |
| 默认 `-verify-rate 0.1` | 206 / 1667 | 约 40 秒 | 翻转带逐条确认；个别孤立例外（如 S704 是区内唯一改用 `.zip` 的曲包）可能推断错 |
| `-verify-rate 1` | 1873 / 0 | 约 7~8 分钟 | 每条都确认； |

程序会打印本次链接的来源（逐条校验 / 区段推断各多少个）。**推断错的链接不会静默留下坏文件**：下载阶段会失败，随后自动经浏览器解析真实链接重试（见下一节）。所以默认抽检是"用少量下载重试换掉几分钟的等待"；如果你希望开跑前就逐条确认，加 `-verify-rate 1`（有邻段结构做首选候选，同样只按 1 次 HEAD/包计费）。

### 4. aria2 批量下载

程序把整批直链和输出文件名写入 `URL/aria2-input.txt`，再调用 aria2：

- 同时最多下载 8 个文件；
- 每个文件再分多线程下载（默认 16 片，批内文件很多时自动降到 10 片，避免连接数过多）；
- 支持断点续传：中断后重新运行，已下载的部分不会重来（通过文件旁 `.aria2` 控制文件判断）；
- 下载期间显示一个原地刷新的进度块：**第一行**是整体进度（百分比、完成数/总数、总字节、总速度与整体剩余时间），**下面每个正在下载的曲包各占一行**，可以看到该曲包自身的百分比、已下载/总字节、速度与剩余时间：
- 若启用了解压（`-unzip`），程序会在下载进行的同时用**单个**工作协程串行解压已完成的曲包（边下边解），避免与下载争抢磁盘 IO；解压统计与下载结果彼此独立。


### 5. 目录与文件说明

| 路径 | 内容 |
| --- | --- |
| `URL/urls.txt` | 本次抓到的全部下载直链（每行一个），即使不下载也会生成 |
| `URL/failed.txt` | 终态失败（没有任何可用链接）与下载失败的记录 |
| `URL/aria2-input.txt` | 传给 aria2 的批量任务文件（含输出文件名） |
| `download/` | 默认下载目录（可用 `-dir` 修改） |
| `unzip/` | 启用 `-unzip` 后的默认解压目录（与下载目录同级，可用 `-unzip-dir` 修改） |
| `tools/` | 放置 `aria2c.exe` 的位置 |
| `userscript/osu-pack-bridge.user.js` | 桥接脚本|

### 6. 无脚本兜底：本地列表文件

如果脚本暂时不可用（例如换了一台机器、浏览器装不了扩展），只要有一份符合载荷格式的 JSON 列表文件，仍然可以走完全部流程：

```powershell
.\osu-pack-downloader.exe -packs .\packs.json
```

```json
{
  "protocol": 1,
  "type": "standard",
  "total": 2,
  "packs": [
    { "tag": "SM111", "name": "osu!mania Beatmap Pack #111", "url": "https://osu.ppy.sh/beatmaps/packs/SM111" },
    { "tag": "S1300", "name": "osu! Beatmap Pack #1300", "url": "https://osu.ppy.sh/beatmaps/packs/S1300" }
  ]
}
```

该入口复用与在线抓取完全相同的载荷校验、候选构造、HEAD 校验与修复流程，只是没有浏览器可以做解析。

## 完整脚本代码

把下面整段内容保存为 `osu-pack-bridge.user.js`，在脚本管理器里新建脚本后粘贴保存即可（与仓库内文件内容一致）：

```javascript
// ==UserScript==
// @name         osu! Pack Bridge
// @namespace    https://github.com/GoneXG/OSUBeatmapPackDownloader
// @version      1.0.2
// @description  在本机浏览器里抓取 osu! 官方曲包列表并解析真实下载链接，经回环地址回传给 osu! 曲包下载器。
// @author       GoneXG
// @match        https://osu.ppy.sh/beatmaps/packs*
// @grant        GM_xmlhttpRequest
// @connect      127.0.0.1
// @run-at       document-idle
// @updateURL    https://raw.githubusercontent.com/GoneXG/OSUBeatmapPackDownloader/master/userscript/osu-pack-bridge.user.js
// @downloadURL  https://raw.githubusercontent.com/GoneXG/OSUBeatmapPackDownloader/master/userscript/osu-pack-bridge.user.js
// ==/UserScript==

/*
 * 协议版本必须与本地进程 bridge.go 中的 bridgeProtocolVersion 保持一致。
 * 载荷格式：{ protocol, type, total, packs: [{ tag, name, url }] }
 */
(function () {
  'use strict';

  const PROTOCOL = 1;
  const OVERLAY_ID = 'opd-bridge-overlay';

  // 抓取节流：顺序翻页、不并发，避免对站点造成压力。
  const PAGE_THROTTLE_MS = 400;
  const RESOLVE_THROTTLE_MS = 300;
  const POLL_INTERVAL_MS = 700;
  const MAX_PAGES = 200;

  // 未登录时详情页出现的提示文本（实测）。
  const LOGIN_MARKER = '需要 登录 才能下载';
  // 已登录时详情页出现的下载锚点类名（实测）。
  const DOWNLOAD_LINK_CLASS = 'beatmap-pack-download__link';

  // 列表项与名称的类名会随 osu-web 改版（2026-09-16 起名称由 .beatmap-pack__name
  // 移入 .beatmap-pack-item-header__name）。按优先级探测多个选择器，避免改版后
  // 静默解析成 0 条、又被误判为「未登录」。
  const PACK_ITEM_SELECTORS = ['div.js-beatmap-pack', 'div.beatmap-pack'];
  const PACK_NAME_SELECTORS = [
    '.beatmap-pack-item-header__name',
    '.beatmap-pack__name',
    '[class*="beatmap-pack"][class*="__name"]',
  ];

  let running = false;
  let lastJobKey = '';
  let overlayEl = null;
  let stopped = false;
  let heartbeatTimer = null;

  // ---------- URL 片段中的作业信息 ----------

  // parseJob 读取片段中的一次性凭据、桥接端口与目标分类。
  // 正常浏览官网时没有片段，脚本保持沉默。
  function parseJob() {
    const raw = location.hash.startsWith('#') ? location.hash.slice(1) : '';
    if (!raw) return null;
    const params = new URLSearchParams(raw);
    const token = params.get('opd') || '';
    const port = parseInt(params.get('port') || '', 10);
    const type = params.get('type') || '';
    if (!token || !Number.isFinite(port) || port <= 0 || port > 65535 || !type) return null;
    return { token, port, type, key: `${token}|${port}|${type}` };
  }

  // ---------- 与本地进程通信 ----------

  // gmRequest 选择可用的带外请求实现：Tampermonkey / Violentmonkey 的
  // GM_xmlhttpRequest，或 Greasemonkey 4+ 的 GM.xmlHttpRequest。
  // 两者都不可用时返回 null，由 bridge 给出可读提示，而不是抛 ReferenceError。
  const gmRequest = (() => {
    if (typeof GM_xmlhttpRequest === 'function') return GM_xmlhttpRequest;
    if (typeof GM !== 'undefined' && GM && typeof GM.xmlHttpRequest === 'function') {
      // Greasemonkey 4+ 的 GM.xmlHttpRequest 返回 Promise，可能忽略回调选项；
      // 这里统一转成回调式，保证 bridge 的 onload/onerror 都能生效。
      return (opts) => {
        let ret;
        try {
          ret = GM.xmlHttpRequest(opts);
        } catch (err) {
          if (opts.onerror) opts.onerror(err);
          return;
        }
        if (ret && typeof ret.then === 'function') {
          ret.then((res) => { if (opts.onload) opts.onload(res); })
            .catch((err) => { if (opts.onerror) opts.onerror(err); });
        }
      };
    }
    return null;
  })();

  // bridge 通过带外请求访问回环桥接服务。
  // 不使用页面上下文的 fetch，避免跨源与私有网络访问（PNA）预检。
  function bridge(job, path, payload) {
    return new Promise((resolve, reject) => {
      if (!gmRequest) {
        reject(new Error('脚本管理器未提供 GM_xmlhttpRequest：请用 Tampermonkey / Violentmonkey 安装脚本，并确认 @grant 未被改动'));
        return;
      }
      gmRequest({
        method: 'POST',
        url: `http://127.0.0.1:${job.port}${path}`,
        headers: {
          'Content-Type': 'application/json',
          'X-Bridge-Token': job.token,
        },
        data: JSON.stringify(Object.assign({ protocol: PROTOCOL }, payload || {})),
        timeout: 20000,
        onload: (res) => {
          let body = {};
          try {
            body = JSON.parse(res.responseText || '{}');
          } catch (_) {
            body = {};
          }
          if (res.status >= 200 && res.status < 300 && body.ok !== false) {
            resolve(body);
            return;
          }
          reject(new Error(body.error || `桥接请求失败（HTTP ${res.status}）`));
        },
        onerror: () => reject(new Error('桥接服务不可达：本地进程可能已退出')),
        ontimeout: () => reject(new Error('桥接请求超时')),
      });
    });
  }

  // ---------- 页面浮层 ----------

  function ensureOverlay() {
    if (overlayEl && document.body.contains(overlayEl)) return overlayEl;
    overlayEl = document.createElement('div');
    overlayEl.id = OVERLAY_ID;
    overlayEl.style.cssText = [
      'position:fixed',
      'right:16px',
      'bottom:16px',
      'z-index:2147483647',
      'max-width:320px',
      'padding:10px 14px',
      'border-radius:8px',
      'background:rgba(20,20,24,.92)',
      'color:#fff',
      'font:13px/1.5 system-ui,"Microsoft YaHei",sans-serif',
      'box-shadow:0 4px 16px rgba(0,0,0,.35)',
      'white-space:pre-wrap',
    ].join(';');
    overlayEl.textContent = 'osu! Pack Bridge';
    document.body.appendChild(overlayEl);
    return overlayEl;
  }

  function setOverlay(text) {
    ensureOverlay().textContent = text;
  }

  function sleep(ms) {
    return new Promise((r) => setTimeout(r, ms));
  }

  // 心跳：抓取/解析期间定期上报，让本地进程能区分「脚本还在跑」与「页面已被关闭」。
  function startHeartbeat(job) {
    stopHeartbeat();
    heartbeatTimer = setInterval(() => {
      bridge(job, '/v1/heartbeat', {}).catch(() => {});
    }, 5000);
  }

  function stopHeartbeat() {
    if (heartbeatTimer) {
      clearInterval(heartbeatTimer);
      heartbeatTimer = null;
    }
  }

  // runPool 以受限并发处理一批任务，在途数量不超过 limit。
  async function runPool(items, limit, worker) {
    const size = Math.max(1, Math.min(limit || 1, items.length));
    let next = 0;
    const runners = [];
    for (let i = 0; i < size; i++) {
      runners.push((async () => {
        for (;;) {
          const idx = next++;
          if (idx >= items.length) return;
          await worker(items[idx]);
        }
      })());
    }
    await Promise.all(runners);
  }

  // ---------- 同源抓取 ----------

  // fetchText 以同源身份抓取站内页面（自带会话 Cookie，且不会被按客户端指纹拦截）。
  async function fetchText(path) {
    const res = await fetch(path, {
      credentials: 'same-origin',
      headers: { Accept: 'text/html,application/xhtml+xml' },
    });
    if (res.status !== 200) {
      throw new Error(`抓取 ${path} 失败（HTTP ${res.status}）`);
    }
    return res.text();
  }

  function parseHTML(html) {
    return new DOMParser().parseFromString(html, 'text/html');
  }

  // ---------- 列表页解析 ----------

  function normalizeSpace(s) {
    return (s || '').replace(/\s+/g, ' ').trim();
  }

  // packName 依次尝试多种名称选择器，返回第一个非空文本。
  function packName(node) {
    for (const sel of PACK_NAME_SELECTORS) {
      const el = node.querySelector(sel);
      if (!el) continue;
      const name = normalizeSpace(el.textContent);
      if (name) return name;
    }
    return '';
  }

  // extractPacks 从列表页 HTML 中提取 tag、官网名称与详情页地址。
  // 节点选择器与名称选择器都留了回退，官网改版时尽量不整体失效。
  function extractPacks(doc) {
    const out = [];
    for (const itemSel of PACK_ITEM_SELECTORS) {
      const nodes = doc.querySelectorAll(itemSel);
      if (nodes.length === 0) continue;
      nodes.forEach((node) => {
        const name = packName(node);
        if (!name) return;
        // 详情页地址优先取列表项头部链接；拿不到就从 tag 兜底拼接
        // （osu-web 的 packs.show 路由键就是 tag，两种写法都有效）。
        let href = '';
        const link = node.querySelector('a.beatmap-pack__header') || node.querySelector('a[href*="/beatmaps/packs/"]');
        if (link) href = link.getAttribute('href') || '';
        let tag = normalizeSpace(node.getAttribute('data-pack-tag'));
        if (!tag && href) {
          const m = href.match(/\/beatmaps\/packs\/([^/?#]+)/);
          if (m) tag = normalizeSpace(decodeURIComponent(m[1]));
        }
        if (!tag) return;
        if (!href) href = `/beatmaps/packs/${tag}`;
        out.push({ tag, name, url: new URL(href, location.origin).href });
      });
      if (out.length > 0) break;
    }
    return out;
  }

  // hasNextPage 判断列表页是否存在比当前页更大的页码链接。
  function hasNextPage(doc, currentPage) {
    const links = doc.querySelectorAll('a[href*="page="]');
    for (const a of links) {
      let page;
      try {
        page = parseInt(new URL(a.getAttribute('href'), location.origin).searchParams.get('page') || '', 10);
      } catch (_) {
        continue;
      }
      if (Number.isFinite(page) && page > currentPage) return true;
    }
    return false;
  }

  function listURL(type, page) {
    const base = `/beatmaps/packs?type=${encodeURIComponent(type)}`;
    return page > 1 ? `${base}&page=${page}` : base;
  }

  // ---------- 详情页解析 ----------

  // analyzePackPage 解析详情页，区分「解析成功 / 已登录但无下载锚点 / 未登录」。
  function analyzePackPage(html) {
    const doc = parseHTML(html);
    const link = doc.querySelector(`a.${DOWNLOAD_LINK_CLASS}`);
    if (link) {
      const href = normalizeSpace(link.getAttribute('href'));
      if (href) return { status: 'ok', href: absoluteHref(href) };
    }
    // 未登录提示由 require_login 渲染为「需要 <a class="js-user-link">登录</a> 才能下载」，
    // 整句被标签拆开，因此按「渲染后的文本」与登录锚点判定，而不是在原始 HTML 里找整句。
    const text = normalizeSpace(doc.body ? doc.body.textContent : '');
    if (text.indexOf(LOGIN_MARKER) !== -1 || doc.querySelector('a.js-user-link') !== null) {
      return { status: 'need-login', href: '' };
    }
    return { status: 'no-link', href: '' };
  }

  function absoluteHref(href) {
    if (href.startsWith('//')) return `https:${href}`;
    if (href.startsWith('/')) return `${location.origin}${href}`;
    return href;
  }

  async function resolvePack(pageURL) {
    const raw = pageURL.split('?')[0];
    const html = await fetchText(`${raw}?format=raw`);
    return analyzePackPage(html);
  }

  // ---------- 登录探测 ----------

  // probeLogin 在抓取前判定登录态。
  // 判据（实测）：详情页出现「需要 登录 才能下载」，或列表页与详情页都不存在下载锚点。
  async function probeLogin(firstPack, listDoc, listHTML) {
    if (listHTML.indexOf(LOGIN_MARKER) !== -1) {
      return { loggedIn: false, reason: '页面提示需要登录后才能下载曲包' };
    }
    const listHasDownload = listDoc.querySelector(`a.${DOWNLOAD_LINK_CLASS}`) !== null;
    // 调用方保证 firstPack 存在：列表页是公开的，解析不到曲包属于抓取失败，
    // 绝不是「未登录」，因此这里绝不因缺少样本而报未登录。
    let html;
    try {
      html = await fetchText(`${firstPack.url.split('?')[0]}?format=raw`);
    } catch (err) {
      return { loggedIn: false, reason: `登录探测失败：${err.message}` };
    }
    const detail = analyzePackPage(html);
    if (detail.status === 'need-login') {
      return { loggedIn: false, reason: '详情页提示需要登录后才能下载' };
    }
    if (detail.status !== 'ok' && !listHasDownload) {
      return { loggedIn: false, reason: '页面中缺少下载链接锚点（beatmap-pack-download__link）' };
    }
    return { loggedIn: true, reason: '' };
  }

  // ---------- 抓取主流程 ----------

  // onLoginProbed 在登录态判定完成后立刻调用，用于把本次作业与登录态一起握手上报。
  async function scrapeCategory(job, onLoginProbed) {
    const packs = [];
    const seen = new Set();
    for (let page = 1; page <= MAX_PAGES; page++) {
      const html = await fetchText(listURL(job.type, page));
      const doc = parseHTML(html);
      const pagePacks = extractPacks(doc);
      if (page === 1) {
        // 列表页公开可访问（无需登录）。第 1 页解析不到任何曲包说明官网结构已变化
        // 或该分类为空，属于抓取失败；若据此报「未登录」，用户会被误导、程序也会终止。
        if (pagePacks.length === 0) {
          return { failed: true, kind: 'error', message: '列表页没有解析到任何曲包：osu! 官网结构可能已更新（请更新脚本），或该分类为空' };
        }
        const probe = await probeLogin(pagePacks[0], doc, html);
        // 先握手：本地进程要拿到登录态才会认为脚本已就绪并继续等待载荷。
        // 这里不吞掉错误——连不上本地进程时由外层统一给出可读提示。
        await onLoginProbed(probe);
        if (!probe.loggedIn) return { failed: true, kind: 'need-login', message: probe.reason };
      }
      if (pagePacks.length === 0) break;
      let added = 0;
      for (const p of pagePacks) {
        if (seen.has(p.tag)) continue;
        seen.add(p.tag);
        packs.push(p);
        added++;
      }
      setOverlay(`正在抓取曲包列表…\n第 ${page} 页：本页新增 ${added} 个，累计 ${packs.length} 个`);
      try {
        await bridge(job, '/v1/progress', { stage: 'scrape', done: page, total: 0, message: `已抓取第 ${page} 页，累计 ${packs.length} 个曲包` });
      } catch (_) {
        /* 进度上报失败不中断抓取 */
      }
      if (!hasNextPage(doc, page)) break;
      await sleep(PAGE_THROTTLE_MS);
      if (stopped) break;
    }
    if (packs.length === 0) {
      return { failed: true, kind: 'error', message: '该分类下没有提取到任何曲包' };
    }
    return { failed: false, packs };
  }

  // resolveLoop 持续轮询本地进程下发的解析任务，直到进程通知收工。
  async function resolveLoop() {
    let poolSize = 4;
    while (!stopped) {
      let resp;
      try {
        resp = await bridge(jobRef, '/v1/poll', {});
      } catch (err) {
        setOverlay(`与本地进程失联：${err.message}\n浏览器可保持打开以便重试。`);
        return;
      }
      const tasks = resp.tasks || [];
      if (Number.isFinite(resp.concurrency) && resp.concurrency > 0) {
        poolSize = resp.concurrency;
      }
      // 在途解析请求数由本地进程下发的并发上限约束。
      await runPool(tasks, poolSize, async (task) => {
        let result;
        try {
          const r = await resolvePack(task.url);
          result = { id: task.id, tag: task.tag, status: r.status, href: r.href, message: '' };
        } catch (err) {
          result = { id: task.id, tag: task.tag, status: 'no-link', href: '', message: err.message };
        }
        setOverlay(`正在解析曲包详情页…\n${task.tag}：${result.status}`);
        try {
          await bridge(jobRef, '/v1/resolve', result);
        } catch (_) {
          /* 回执失败时进程会超时，不再额外处理 */
        }
        await sleep(RESOLVE_THROTTLE_MS);
      });
      if (stopped) return;
      if (resp.done && tasks.length === 0) {
        setOverlay('本次任务已完成，可以关闭此页面。');
        return;
      }
      await sleep(POLL_INTERVAL_MS);
    }
  }

  let jobRef = null;

  async function runJob(job) {
    if (running) return;
    running = true;
    jobRef = job;
    startHeartbeat(job);
    try {
      setOverlay('已连接本地下载器，正在检查登录状态…');
      const scrape = await scrapeCategory(job, async (probe) => {
        await bridge(job, '/v1/hello', {
          type: job.type,
          loggedIn: probe.loggedIn,
          message: probe.reason,
        });
      });
      if (scrape.failed) {
        setOverlay(scrape.kind === 'need-login'
          ? `未登录，已停止。\n${scrape.message}\n请登录 osu! 后重新运行。`
          : `抓取失败：${scrape.message}`);
        try {
          await bridge(job, '/v1/fail', { kind: scrape.kind, message: scrape.message });
        } catch (_) { /* 忽略回传失败 */ }
        return;
      }
      const payload = { type: job.type, total: scrape.packs.length, packs: scrape.packs };
      await bridge(job, '/v1/packs', payload);
      setOverlay(`列表已回传（${scrape.packs.length} 个曲包）。\n等待本地进程下发解析任务…`);
      await resolveLoop();
    } catch (err) {
      setOverlay(`脚本出错：${err.message}`);
      try {
        await bridge(job, '/v1/fail', { kind: 'error', message: err.message });
      } catch (_) { /* 忽略回传失败 */ }
    } finally {
      stopHeartbeat();
      running = false;
      jobRef = null;
    }
  }

  // ---------- 触发与片段监听 ----------

  // maybeStart 在片段携带作业信息时启动一轮抓取。
  // 同一个片段（同一份凭据 + 端口 + 分类）只启动一次，避免重复抓取。
  function maybeStart() {
    const job = parseJob();
    if (!job) return;
    if (job.key === lastJobKey) return;
    if (running) return; // 上一轮还没结束；兜底轮询会在其收尾后再次尝试
    lastJobKey = job.key;
    runJob(job);
  }

  // 片段变化（例如本地进程再次打开同一页面并写入新凭据）时自动开始新一轮。
  window.addEventListener('hashchange', maybeStart);
  window.addEventListener('beforeunload', () => {
    stopped = true;
  });
  // 兜底轮询：有些场景下 hashchange 不触发（例如被脚本管理器拦截）。
  setInterval(() => {
    const job = parseJob();
    if (job && job.key !== lastJobKey) maybeStart();
  }, 1000);

  maybeStart();
})();
```


## 常见问题（FAQ）

**Q：提示「未检测到桥接脚本」，抓取无法进行？**

A：按顺序排查：

1. 是否装了 Tampermonkey / Violentmonkey？
2. 是否安装了桥接脚本（见「安装浏览器桥接脚本」）？
3. 脚本是否处于启用状态？（在 osu.ppy.sh 页面上点脚本管理器图标确认）
4. 浏览器是否被自动打开了？没打开就手动打开程序打印的那个地址，页面右下角应出现「osu! Pack Bridge」浮层。

程序在等待超时后会打印同样的引导，并且保持可重试：装好脚本后重新运行即可。

> 如果程序提示的是「桥接脚本请求被本地服务拒绝」，说明脚本其实已经连上了，只是凭据/版本不匹配（最常见的原因是浏览器里还开着上一次运行留下的旧页面）。请关掉旧的 osu! 标签页，在脚本管理器里更新脚本后重新运行。

**Q：脚本报「未登录」？**

A：先在同一个浏览器里确认已登录 osu!（能打开曲包详情页并看到下载按钮），然后重新运行程序。脚本会在抓取前检查登录态，未登录时不会抓取也不会解析。

**Q：浏览器明明是登录的，脚本却说「未登录」或「列表页没有解析到任何曲包」？**

A：这不是登录问题。曲包列表页是公开的，未登录也能看到列表；解析不到曲包说明 osu! 官网改版、脚本里的页面选择器失效了（2026-09-16 官网把曲包名称从 `.beatmap-pack__name` 移到 `.beatmap-pack-item-header__name`，触发过一次）。处理办法：

1. 在脚本管理器里对 `osu! Pack Bridge` 点「检查更新」，更新到最新版（当前 `1.0.2`）；
2. 刷新 osu! 页面后重新运行程序。

最新版脚本仍报同样的错，说明官网又改版了——请把浮层提示和当时 <https://osu.ppy.sh/beatmaps/packs> 页面结构的变化反馈到仓库 issue，按新结构补选择器即可。

**Q：提示「未找到 aria2c，已转为仅保留链接文件」？**

A：把 `aria2c.exe` 放到程序旁的 `tools/` 目录（或加入系统 PATH）后重跑即可。程序会先找 `tools/aria2c.exe`，再找系统 PATH。

**Q：下载到一半中断了怎么办？**

A：重新运行程序、选择同样的分类再次下载即可。aria2 开启了断点续传（`--continue=true`），会接着未完成的部分继续下载。

**Q：个别曲包一直失败？**

A：程序会先构造候选并抽检校验，再按区段推断/学习命名规则批量修复，最后用浏览器逐个解析；只有确实取不到链接或本轮毫无进展时才停下。查看 `URL/failed.txt` 里的记录。

**Q：校验阶段还要等多久？**

A：默认抽检 10%（`-verify-rate 0.1`），其余按区段结构推断：常规 / osu! 的 1873 个曲包实测约 40 秒进入下载。`-verify-rate 1` 逐条校验全部链接，同样这批曲包实测约 7~8 分钟（站点在持续请求下会限速到约 4 次/秒）。想更快可以用 `-verify-rate 0.02`（区段推断跨度更大）或 `-lookup-concurrency 8`（并发上限）。

**Q：抽检会不会漏掉坏链接？**

A：会漏掉少量"孤立例外"——例如某个曲包是它所在区段里唯一改用 `.zip` 的（实测 S704），抽检抽不到就只能等下载阶段暴露。这类链接不会静默产生坏文件：下载会失败，程序随即经浏览器解析真实链接并重试，成功后才算完成；最终仍取不到的才写进 `failed.txt`。翻转带（两种结构交界）另做逐条确认，不会漏。要求零残留就用 `-verify-rate 1`。

**Q：程序会自动解压或自动导入 osu! 吗？**

A：默认都不做。加 `-unzip` 可以让程序在下载时顺带解压成 `.osz`（见「自动解压（可选）」）；导入 osu! 仍然是把 `.osz` 拖进游戏窗口的手动动作——游戏侧的导入行为本工具不介入。

**Q：抓取很慢或连不上？**

A：抓取在浏览器里进行，慢通常是分页较多；校验与下载依赖 `packs.ppy.sh`。若网络受限，请为命令行程序配置可用的代理后重试（`-proxy`），浏览器侧仍走系统网络。

**Q：启用了 `-unzip`，但有些曲包解压失败？**

A：解压失败只记入解压汇总，**不影响下载结果**：失败的曲包会保留压缩包（可能是压缩包本身损坏，或用了纯 Go 库尚不支持的压缩特性），可以稍后用外部工具单独处理。下载成功数与 `URL/failed.txt` 只反映下载阶段。

**Q：会下载很多数据吗？**

A：整类曲包数量很大（例如「常规」目前有 1800+ 个，单个几十到几百 MB）。建议先用编号区间或「最新 N 个」缩小范围，并确认磁盘空间足够。

## 注意事项

- 请合理使用，下载内容仅用于个人游玩或备份，请不要滥用该工具下载以免造成对OSU！官方的困扰；
- 桥接脚本只在被程序唤起（URL 片段里带作业信息）时工作，正常浏览官网时保持安静；它只在回环地址 `127.0.0.1` 上与本程序通信，且每次运行使用一次性凭据；
- 本项目是第三方工具，与 osu! 官方没有隶属关系。
## 参考项目

本项目在开发与运行中引用/参考了以下项目，特此致谢：

| 项目 | 用途 | 链接 |
| --- | --- | --- |
| aria2 | 多线程/多连接下载引擎。程序只通过命令行调用 `aria2c`，不内嵌或修改其代码 | <https://github.com/aria2/aria2> |
| bodgit/sevenzip | 纯 Go 的 7z 读取库，用于解压历史 `.7z` 曲包，避免依赖外部解压工具 | <https://github.com/bodgit/sevenzip> |
| Tampermonkey / Violentmonkey | 用户脚本管理器，负责在浏览器里运行桥接脚本 | <https://www.tampermonkey.net/> |
| Go | 本项目的编译语言与构建工具链 | <https://go.dev> |

> aria2 是独立开源项目（GPLv2），`aria2c.exe` 属于它自己的发行物；本工具只是把 aria2 当作外部下载程序调用。

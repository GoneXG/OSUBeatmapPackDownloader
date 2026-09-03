# osu! Beatmap Pack 下载器（OSU曲包下载器）

按类型/模式批量抓取 osu! 官方 Beatmap Packs 下载链接，并可调用 aria2 批量下载。

## 功能

- 7 类曲包菜单：常规 / 精选艺术家 / 锦标赛 / 社区喜爱计划 / 艺术家专辑 / 聚光灯 / 主题
- 常规、锦标赛、社区喜爱、聚光灯支持按游戏模式筛选（osu! / osu!catch / osu!taiko / osu!mania 等）
- 两种保存方式：按 `类型_模式` 自动分目录，或统一保存到指定路径
- 自动分页抓取（每类最多约 3000+ 曲包）
- 支持 aria2 批量下载；未安装 aria2 时自动降级为“仅保存链接”
- 自动浏览器登录采集 `osu_sid`（rod），失败自动降级为手动粘贴

## 工作原理（重要）

osu! 官网页面上每个曲包对应的真实下载链接是 packs.ppy.sh 上的压缩包，例如：

```
https://packs.ppy.sh/S1813%20-%20osu%21%20Beatmap%20Pack%20%231813.zip
```

压缩包内才是 `.osz` 谱面文件。程序从公开的列表页（`https://osu.ppy.sh/beatmaps/packs?type=...&page=N`）解析曲包 `tag` 与 `名称`，据此构造直链。该直链无需登录即可下载（已实测验证）。

Cookie 采集（T3）用于：

1. 降低 osu.ppy.sh 页面对无浏览器特征请求的风控概率；
2. 对构造直链失败的旧曲包，带 Cookie 抓取其详情页 `?format=raw`，读取站点数据库里记录的官方原始下载地址做一次重试。

## 使用

```bash
# 1. 构建（需要 Go 1.21+）
go build -o osu-pack-downloader.exe .

# 2. 可选：准备 aria2c
#    将 aria2c.exe 放入 tools/ 目录（或加入 PATH）。

# 3. 运行（可用 -dir 修改下载根目录）
.\osu-pack-downloader.exe
```

交互流程：

1. 选择下载保存方式（自动分目录 / 统一路径）
2. 选择分类与模式
3. 自动登录（120 秒）或手动粘贴 `osu_sid`
4. 抓取链接并写入 `URL/urls.txt`
5. 选择 [1] aria2 下载 或 [2] 仅保留链接文件

## 输出文件

```
URL/urls.txt       所有下载直链（每行一个）
URL/failed.txt     下载失败或抓取失败的记录
URL/aria2-input.txt aria2 输入文件（含文件名，可单独用于 aria2 -i）
download/          下载目录（取决于你的选择）
tools/             aria2c.exe 存放位置
```

## 注意

- 下载量很大时请自行评估磁盘空间与带宽（例如“常规”分类目前有 1800+ 曲包）。
- 若 aria2 对某曲包直链返回 404，程序会尝试用 Cookie 读取官方存储地址重试一次；仍失败则记入 `failed.txt`。
- 程序不会自动解压 zip；安装时把 zip 解压出的 `.osz` 拖入 osu! 即可。

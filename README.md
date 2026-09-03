# osu! Beatmap Pack 下载器（OSU曲包下载器）

按类型/模式批量抓取 osu! 官方 Beatmap Packs 下载链接，并可调用 aria2 批量下载。

## 功能

- 7 类曲包菜单：常规 / 精选艺术家 / 锦标赛 / 社区喜爱计划 / 艺术家专辑 / 聚光灯 / 主题
- 常规、锦标赛、社区喜爱、聚光灯支持按游戏模式筛选；子菜单提供“返回上级”
- 先无 Cookie 直连抓取；直连失败时提示手动粘贴 `osu_sid` 后重新爬取真实链接（无自动登录/无浏览器自动化）
- 自动分页抓取（每类最多约 3000+ 曲包）
- 下载文件全部混存到下载根目录（默认 `./download/`，可用 `-dir` 修改）
- 支持 aria2 批量下载；未安装 aria2 时自动降级为“仅保存链接”

## 工作原理（重要）

osu! 官网页面上每个曲包对应的真实下载链接是 packs.ppy.sh 上的压缩包，例如：

```
https://packs.ppy.sh/S1813%20-%20osu%21%20Beatmap%20Pack%20%231813.zip
```

压缩包内才是 `.osz` 谱面文件。程序从公开的列表页（`https://osu.ppy.sh/beatmaps/packs?type=...&page=N`）解析曲包 `tag` 与 `名称`，据此构造直链。该直链无需登录即可下载（已实测验证），因此默认不要求 Cookie。

仅当直连抓取被站点拒绝（如 403）时，程序才提示你手动粘贴 `osu_sid`（从浏览器开发者工具复制），随后用该 Cookie 重新爬取；个别构造直链失败的旧曲包也会在下载阶段用 Cookie 读取官方存储地址重试一次。

## 使用

```bash
# 1. 构建（需要 Go 1.21+；国内可设置 GOPROXY 加速）
go build -o osu-pack-downloader.exe .

# 2. 可选：准备 aria2c
#    将 aria2c.exe 放入 tools/ 目录（或加入 PATH）。

# 3. 运行（可用 -dir 修改下载根目录）
.\osu-pack-downloader.exe
```

国内构建加速：

```bash
go env -w GOPROXY=https://goproxy.cn,direct
```

交互流程：

1. 选择曲包分类，有子模式时再选择模式（可“返回上级”换分类）
2. 自动直连抓取链接并写入 `URL/urls.txt`；若直连失败则手动粘贴 `osu_sid` 重爬
3. 选择 [1] aria2 下载 或 [2] 仅保留链接文件

## 输出文件

```
URL/urls.txt        所有下载直链（每行一个）
URL/failed.txt      下载失败或抓取失败的记录
URL/aria2-input.txt aria2 输入文件（含文件名，可单独用于 aria2 -i）
download/           混存下载目录（默认）
tools/              aria2c.exe 存放位置
```

## 注意

- 下载量很大时请自行评估磁盘空间与带宽（例如“常规”分类目前有 1800+ 曲包）。
- 程序不会自动解压 zip；安装时把 zip 解压出的 `.osz` 拖入 osu! 即可。

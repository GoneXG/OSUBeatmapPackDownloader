# 第三方组件声明（Third-Party Notices）

本项目（OSU Beatmap Pack Downloader）**自身的源代码**以 MIT 许可证发布，全文见
[`LICENSE`](LICENSE)。

发布的可执行文件在编译时会**静态链接**下列开源 Go 模块，它们各自遵循自己的许可证。
其中 MIT / BSD-3-Clause / Apache-2.0 要求保留版权与许可声明，MPL-2.0 还要求
其源代码可获取。为满足这些要求，本文件与 [`LICENSES/`](LICENSES) 目录收录了
完整的署名与许可证全文。

> 分发本程序的可执行文件时，请将本文件与 `LICENSES/` 目录一并随附。

## 依赖汇总

| 模块 | 版本 | 许可证 | 许可证全文 |
| --- | --- | --- | --- |
| github.com/bodgit/sevenzip | v1.5.2 | BSD-3-Clause | [`LICENSES/bodgit-sevenzip.txt`](LICENSES/bodgit-sevenzip.txt) |
| github.com/andybalholm/brotli | v1.1.0 | MIT | [`LICENSES/andybalholm-brotli.txt`](LICENSES/andybalholm-brotli.txt) |
| github.com/bodgit/plumbing | v1.3.0 | BSD-3-Clause | [`LICENSES/bodgit-plumbing.txt`](LICENSES/bodgit-plumbing.txt) |
| github.com/bodgit/windows | v1.0.1 | BSD-3-Clause | [`LICENSES/bodgit-windows.txt`](LICENSES/bodgit-windows.txt) |
| github.com/hashicorp/errwrap | v1.0.0 | MPL-2.0 | [`LICENSES/hashicorp-errwrap.txt`](LICENSES/hashicorp-errwrap.txt) |
| github.com/hashicorp/go-multierror | v1.1.1 | MPL-2.0 | [`LICENSES/hashicorp-go-multierror.txt`](LICENSES/hashicorp-go-multierror.txt) |
| github.com/hashicorp/golang-lru/v2 | v2.0.7 | MPL-2.0 | [`LICENSES/hashicorp-golang-lru-v2.txt`](LICENSES/hashicorp-golang-lru-v2.txt) |
| github.com/klauspost/compress | v1.17.9 | BSD-3-Clause | [`LICENSES/klauspost-compress.txt`](LICENSES/klauspost-compress.txt) |
| github.com/pierrec/lz4/v4 | v4.1.21 | BSD-3-Clause | [`LICENSES/pierrec-lz4.txt`](LICENSES/pierrec-lz4.txt) |
| github.com/ulikunitz/xz | v0.5.12 | BSD-3-Clause | [`LICENSES/ulikunitz-xz.txt`](LICENSES/ulikunitz-xz.txt) |
| go4.org | v0.0.0-20200411211856-f5505b9728dd | Apache-2.0 | [`LICENSES/go4-org.txt`](LICENSES/go4-org.txt) |
| golang.org/x/text | v0.17.0 | BSD-3-Clause | [`LICENSES/golang-x-text.txt`](LICENSES/golang-x-text.txt) |

## 各组件署名

- **github.com/bodgit/sevenzip** — Copyright (c) 2020, Matt Dainty — <https://github.com/bodgit/sevenzip>
- **github.com/andybalholm/brotli** — Copyright (c) 2009, 2010, 2013-2016 by the Brotli Authors — <https://github.com/andybalholm/brotli>
- **github.com/bodgit/plumbing** — Copyright (c) 2019, Matt Dainty — <https://github.com/bodgit/plumbing>
- **github.com/bodgit/windows** — Copyright (c) 2020, Matt Dainty — <https://github.com/bodgit/windows>
- **github.com/hashicorp/errwrap** — Copyright (c) HashiCorp, Inc. — <https://github.com/hashicorp/errwrap>
- **github.com/hashicorp/go-multierror** — Copyright (c) HashiCorp, Inc. — <https://github.com/hashicorp/go-multierror>
- **github.com/hashicorp/golang-lru/v2** — Copyright (c) 2014 HashiCorp, Inc. — <https://github.com/hashicorp/golang-lru>
- **github.com/klauspost/compress** — Copyright (c) 2012 The Go Authors / Copyright (c) 2019 Klaus Post — <https://github.com/klauspost/compress>
- **github.com/pierrec/lz4/v4** — Copyright (c) 2015, Pierre Curto — <https://github.com/pierrec/lz4>
- **github.com/ulikunitz/xz** — Copyright (c) 2014-2022, Ulrich Kunitz — <https://github.com/ulikunitz/xz>
- **go4.org** — Copyright the go4.org Authors — <https://go4.org>
- **golang.org/x/text** — Copyright 2009 The Go Authors — <https://cs.opensource.google/go/x/text>

## 关于 MPL-2.0 组件

`github.com/hashicorp/errwrap`、`github.com/hashicorp/go-multierror` 与
`github.com/hashicorp/golang-lru/v2` 采用 Mozilla Public License 2.0（MPL-2.0）。
MPL-2.0 是**文件级** copyleft：

- 本项目**未修改**这些模块的源代码，仅以未修改形式编译、链接进可执行文件；
- 这些模块的源代码可分别从上面的上游仓库按对应版本获取，也可通过 Go module
  proxy（<https://proxy.golang.org/>）按 `模块@版本` 下载，例如
  `github.com/hashicorp/golang-lru/v2@v2.0.7`；
- 任何对这些模块源代码的修改（本项目目前没有）仍须以 MPL-2.0 发布。

MPL-2.0 允许与采用其他许可证（含本项目使用的 MIT）的作品组合成更大作品，
因此本项目整体以 MIT 发布不构成冲突。

## 外部可选工具（不随本项目分发）

- **aria2（`aria2c`）** — 采用 GNU General Public License v2.0。它是**用户自行
  选装的外部程序**，并不包含在本项目的源码或发行物中。本程序仅在运行时以独立
  进程方式调用它，两者属于独立程序，不构成对本项目源代码的 GPL 传染。
  项目主页：<https://github.com/aria2/aria2>

## 网站与用户脚本

`userscript/osu-pack-bridge.user.js` 是本项目自行编写的用户脚本，随本项目以
MIT 许可证发布。它在浏览器中读取 osu! 官网（osu.ppy.sh）页面 DOM 并回传数据，
不包含、复制或改编 osu! 官方网站（osu-web）的源代码。

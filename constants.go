package main

import "time"

const (
	// RootDir 程序运行根目录。
	RootDir = "."
	// UrlOutputDir urls.txt / failed.txt 存放目录。
	UrlOutputDir = "./URL/"
	// ToolsDir aria2c 存放位置。
	ToolsDir = "./tools/"
	// CookieTimeout 自动登录等待超时。
	CookieTimeout = 120 * time.Second

	// userAgent 使用与真实浏览器接近的 UA，降低被站点风控的概率。
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"
)

// DownloadRoot 下载根目录（可通过 -dir 启动参数修改）。
var DownloadRoot = "./download/"

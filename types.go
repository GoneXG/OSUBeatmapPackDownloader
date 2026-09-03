package main

// Category 对应曲包类型菜单中的一项。
type Category struct {
	Name  string
	Modes []string // 空切片表示无子模式，直接抓取整类
}

// CategoryMap 曲包菜单映射表（与需求中的硬编码常量一致）。
var CategoryMap = map[int]Category{
	1: {"常规", []string{"osu!", "osu!catch", "osu!taiko", "osu!mania"}},
	2: {"精选艺术家", []string{}}, // 无子模式，直接爬
	3: {"锦标赛", []string{"osu!", "osu!catch", "osu!taiko", "osu!mania 4k", "osu!mania 7k"}},
	4: {"社区喜爱计划", []string{"osu!", "osu!catch", "osu!taiko", "osu!mania"}},
	5: {"艺术家/专辑", []string{}},
	6: {"聚光灯", []string{"osu!", "osu!catch", "osu!taiko", "osu!mania"}},
	7: {"主题", []string{}},
}

// SiteTypeByCatID 分类编号 -> osu.ppy.sh 列表页的 type 参数。
var SiteTypeByCatID = map[int]string{
	1: "standard",
	2: "featured",
	3: "tournament",
	4: "loved",
	5: "artist",
	6: "chart", // 页面标签为 Spotlights(聚光灯)
	7: "theme",
}

// Pack 列表页中的一个曲包。
type Pack struct {
	Tag       string // 如 S1813、ST406
	Name      string // 如 osu! Beatmap Pack #1813
	PageURL   string // 曲包详情页
	DirectURL string // packs.ppy.sh 直链（由 Tag+Name 拼出）
}

// CategoryChoice 用户在 T2 选择的结果。
type CategoryChoice struct {
	CatID int
	Mode  string // 无子模式时为空
}

// ScrapeResult 抓取一整个列表类型的结果。
type ScrapeResult struct {
	Packs  []Pack
	Failed bool
	Reason string
}

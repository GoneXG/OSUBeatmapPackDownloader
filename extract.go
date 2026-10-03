package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bodgit/sevenzip"
)

// extractLayout 解压布局（-unzip-layout）。
type extractLayout int

const (
	// extractLayoutFlat 所有解压产物直接落在解压目录下（默认）。
	extractLayoutFlat extractLayout = iota
	// extractLayoutPerPack 每个压缩包一个以压缩包文件名（去掉扩展名）命名的子目录。
	extractLayoutPerPack
)

// ParseExtractLayout 解析 -unzip-layout 的取值，返回布局与是否识别成功。
func ParseExtractLayout(v string) (extractLayout, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "flat":
		return extractLayoutFlat, true
	case "per-pack", "perpack", "per_pack", "pack":
		return extractLayoutPerPack, true
	default:
		return extractLayoutFlat, false
	}
}

// String 返回布局的启动参数取值，用于日志与错误信息。
func (l extractLayout) String() string {
	if l == extractLayoutPerPack {
		return "per-pack"
	}
	return "flat"
}

// ExtractConfig 解压相关配置，由启动参数决定。
type ExtractConfig struct {
	Enabled     bool          // 是否启用解压（默认关闭）
	Dir         string        // 解压目录；为空时按下载目录推导（同级 unzip）
	Layout      extractLayout // 扁平或按曲包
	DeleteAfter bool          // 解压成功后删除压缩包
}

// DefaultExtractDir 由下载目录推导默认解压目录：下载目录的同级 `unzip` 目录。
// 默认下载目录 `.\download\` 对应 `.\unzip\`，`D:\osu曲包` 对应 `D:\unzip\`。
func DefaultExtractDir(downloadDir string) string {
	abs, err := filepath.Abs(downloadDir)
	if err != nil {
		abs = filepath.Clean(downloadDir)
	}
	return filepath.Join(filepath.Dir(abs), "unzip")
}

// destRootFor 返回某个压缩包的解压目标目录：扁平布局用解压根目录，按曲包布局用同名子目录。
func destRootFor(extractRoot, archive string, layout extractLayout) string {
	if layout != extractLayoutPerPack {
		return extractRoot
	}
	base := strings.TrimSuffix(filepath.Base(archive), filepath.Ext(archive))
	return filepath.Join(extractRoot, base)
}

// extractOutcome 单个压缩包的解压结果。
type extractOutcome struct {
	Archive     string   // 压缩包路径
	Files       int      // 新落盘的文件数
	Skipped     int      // 目标已存在而跳过的文件数（不覆盖用户已有数据）
	Rejected    int      // 因路径逃逸被拒绝的条目数
	Reasons     []string // 被拒绝条目的原因，用于日志与失败明细
	Unsupported bool     // 扩展名既非 .zip 也非 .7z，已跳过且不产生任何产物
	Err         error    // 读取/写出失败原因
}

// failed 判断该压缩包是否应记入解压失败：读取/写出错误，或有条目因路径逃逸被拒绝。
func (o extractOutcome) failed() bool { return o.Err != nil || o.Rejected > 0 }

// reason 汇总该压缩包的失败原因。
func (o extractOutcome) reason() string {
	if o.Err != nil {
		return briefError(o.Err)
	}
	if len(o.Reasons) > 0 {
		return strings.Join(o.Reasons, "；")
	}
	return "未知原因"
}

// extractArchive 按压缩包实际扩展名分派解压器；非 .zip/.7z 记为「格式不支持」并跳过。
func extractArchive(archive, extractRoot string, layout extractLayout) extractOutcome {
	out := extractOutcome{Archive: archive}
	ext := strings.ToLower(filepath.Ext(archive))
	if ext != ".zip" && ext != ".7z" {
		out.Unsupported = true
		return out
	}
	destRoot := destRootFor(extractRoot, archive, layout)
	var (
		stats entryStats
		err   error
	)
	if ext == ".7z" {
		stats, err = extractSevenZip(archive, destRoot)
	} else {
		stats, err = extractZip(archive, destRoot)
	}
	out.Files = stats.Files
	out.Skipped = stats.Skipped
	out.Rejected = stats.Rejected
	out.Reasons = stats.Reasons
	out.Err = err
	return out
}

// archiveEntry 压缩包内一个待写盘的条目；Open 由调用方保证在压缩包关闭前调用。
type archiveEntry struct {
	Name  string
	IsDir bool
	Open  func() (io.ReadCloser, error)
}

// entryStats 一个压缩包内各条目的写盘统计。
type entryStats struct {
	Files    int
	Skipped  int
	Rejected int
	Reasons  []string
}

// extractZip 解压 zip 曲包。
//
// 说明：Go 的 archive/zip 在条目名不是合法 UTF-8 时会把原始字节留在 Name 里，
// 这里不做编码猜测、也不丢弃该条目——按原始字节继续走路径校验后落盘，
// 避免个别历史曲包的条目名让整包失败。
func extractZip(archive, destRoot string) (entryStats, error) {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return entryStats{}, err
	}
	defer r.Close()

	entries := make([]archiveEntry, 0, len(r.File))
	for _, f := range r.File {
		file := f
		entries = append(entries, archiveEntry{
			Name:  file.Name,
			IsDir: file.FileInfo().IsDir(),
			Open:  func() (io.ReadCloser, error) { return file.Open() },
		})
	}
	return writeArchiveEntries(destRoot, entries)
}

// extractSevenZip 解压 7z 曲包（纯 Go 库，不依赖外部可执行文件）。
func extractSevenZip(archive, destRoot string) (entryStats, error) {
	r, err := sevenzip.OpenReader(archive)
	if err != nil {
		return entryStats{}, err
	}
	defer r.Close()

	entries := make([]archiveEntry, 0, len(r.File))
	for _, f := range r.File {
		file := f
		entries = append(entries, archiveEntry{
			Name:  file.Name,
			IsDir: file.FileInfo().IsDir(),
			Open:  func() (io.ReadCloser, error) { return file.Open() },
		})
	}
	return writeArchiveEntries(destRoot, entries)
}

// writeArchiveEntries 把条目写入 destRoot：已存在的目标跳过不覆盖，
// 会写到 destRoot 之外的条目被拒绝并记录，其余照常解压。
func writeArchiveEntries(destRoot string, entries []archiveEntry) (entryStats, error) {
	var st entryStats
	rootAbs, err := filepath.Abs(destRoot)
	if err != nil {
		return st, fmt.Errorf("解压目录不可用: %w", err)
	}
	for _, e := range entries {
		rel, ok := safeEntryPath(e.Name)
		if !ok {
			st.Rejected++
			st.Reasons = append(st.Reasons, fmt.Sprintf("越界条目 %q", e.Name))
			continue
		}
		if rel == "" {
			continue // 压缩包根条目，没有实际文件
		}
		target := filepath.Join(rootAbs, rel)
		if !resolvesInside(rootAbs, target) {
			st.Rejected++
			st.Reasons = append(st.Reasons, fmt.Sprintf("越界条目 %q", e.Name))
			continue
		}
		if e.IsDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return st, fmt.Errorf("创建目录 %s 失败: %w", target, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return st, fmt.Errorf("创建目录 %s 失败: %w", filepath.Dir(target), err)
		}
		// O_EXCL：与「目标已存在就不覆盖」语义一致，同时避免与并发写入串味。
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			st.Skipped++
			continue
		}
		if err != nil {
			return st, fmt.Errorf("创建文件 %s 失败: %w", target, err)
		}
		rc, err := e.Open()
		if err != nil {
			f.Close()
			_ = os.Remove(target)
			return st, fmt.Errorf("读取条目 %q 失败: %w", e.Name, err)
		}
		_, copyErr := io.Copy(f, rc)
		rc.Close()
		if closeErr := f.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			_ = os.Remove(target)
			return st, fmt.Errorf("写出 %s 失败: %w", target, copyErr)
		}
		st.Files++
	}
	return st, nil
}

// safeEntryPath 校验条目名并返回可安全拼接的相对路径：
// 拒绝绝对路径、盘符/UNC 形式与 `..` 上跳；压缩包根条目返回空路径。
func safeEntryPath(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	// zip 与 7z 都用 `/` 记录条目名，但历史包可能混用 `\`，统一后再判断。
	normalized := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(normalized, "/") {
		return "", false // 根路径形式
	}
	if len(normalized) >= 2 && normalized[1] == ':' {
		return "", false // 盘符形式（C:/、C:foo）
	}
	if filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", false
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." {
		return "", true
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return filepath.FromSlash(cleaned), true
}

// resolvesInside 取绝对路径后做前缀校验，确认 target 确实落在 root 之内。
func resolvesInside(root, target string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// extractScanTick 解压扫描间隔：单个工作协程按此间隔检查本批是否又有压缩包下载完成。
// 集成测试会调小它来缩短等待。
var extractScanTick = 500 * time.Millisecond

// extractFailure 一个解压失败（或格式不支持）的曲包。
type extractFailure struct {
	Tag     string
	Archive string
	Reason  string
}

// String 生成失败明细里的单条展示文本。
func (f extractFailure) String() string {
	label := f.Tag
	if label == "" {
		label = filepath.Base(f.Archive)
	}
	if f.Reason == "" {
		return label
	}
	return fmt.Sprintf("%s（%s）", label, f.Reason)
}

// extractStats 一次解压会话的统计快照；同时作为端到端验收的「已解压」依据。
type extractStats struct {
	Total       int // 本批曲包总数
	Processed   int // 已处理的压缩包数（含格式不支持的）
	Succeeded   int // 解压成功数
	Failed      int // 解压失败数
	Unsupported int // 格式不支持而跳过数
	Files       int // 新落盘文件数
	Skipped     int // 目标已存在而跳过的文件数
	Rejected    int // 被拒绝的越界条目数
	SetupErr    string

	Failures          []extractFailure
	UnsupportedPacks  []extractFailure
	SucceededArchives map[string]bool // 压缩包文件名 -> 已成功解压（可能已被删除）
}

// extractSession 边下边解会话：单个工作协程串行扫描本批曲包，
// 每发现一个已完成（无 .aria2 控制文件且体积大于 0）且尚未处理的压缩包就立即解压。
type extractSession struct {
	cfg       ExtractConfig
	targetDir string
	items     []aria2Item

	// root 是解析后的解压根目录，在 Start 时创建；创建失败则 setupErr 非空并停止解压。
	root     string
	setupErr string

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once

	mu       sync.Mutex
	handled  map[string]bool
	handledN int
	failures []extractFailure
	badFmt   []extractFailure
	okNames  map[string]bool
	stat     extractStats
	lastLine string
}

// newExtractSession 创建解压会话；调用方随后用 Start/Stop 驱动。
func newExtractSession(cfg ExtractConfig, targetDir string, items []aria2Item) *extractSession {
	root := cfg.Dir
	if strings.TrimSpace(root) == "" {
		root = DefaultExtractDir(targetDir)
	}
	if abs, err := AbsOrRelJoin(root); err == nil {
		root = abs
	}
	return &extractSession{
		cfg:       cfg,
		targetDir: targetDir,
		items:     items,
		root:      root,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		handled:   make(map[string]bool),
		okNames:   make(map[string]bool),
		stat:      extractStats{Total: len(items)},
	}
}

// prepareExtraction 按配置与下载方式创建解压会话。
// 仅保留链接文件（method != "1"）或未显式启用时返回 nil：不建解压目录、不产生任何解压产物。
func prepareExtraction(cfg ExtractConfig, method, targetDir string, items []aria2Item) *extractSession {
	if !cfg.Enabled || method != "1" {
		return nil
	}
	return newExtractSession(cfg, targetDir, items)
}

// Start 启动扫描协程。
func (s *extractSession) Start() {
	if s == nil {
		return
	}
	go s.run()
}

// Stop 停止扫描并返回统计快照；重复调用安全。
func (s *extractSession) Stop() *extractStats {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.done
	return s.snapshot()
}

func (s *extractSession) run() {
	defer close(s.done)
	if err := EnsureDir(s.root); err != nil {
		s.mu.Lock()
		s.setupErr = fmt.Sprintf("无法创建解压目录 %s: %v", s.root, err)
		s.stat.SetupErr = s.setupErr
		s.mu.Unlock()
		msgf("解压目录创建失败: %s（跳过解压，不影响下载结果）", s.setupErr)
		s.reportSummary()
		return
	}
	s.emitProgress(true)
	ticker := time.NewTicker(extractScanTick)
	defer ticker.Stop()
	for {
		s.scanOnce()
		if s.allHandled() {
			s.reportSummary() // 本批全部处理完毕，无需继续空转扫描
			return
		}
		select {
		case <-s.stop:
			s.scanOnce() // 收尾：把停止前刚完成的压缩包也处理掉
			s.reportSummary()
			return
		case <-ticker.C:
		}
	}
}

// scanOnce 扫描本批曲包，把新完成且未处理过的压缩包逐个解压（串行，保持任务顺序）。
func (s *extractSession) scanOnce() {
	for _, it := range s.items {
		name := it.Pack.DownloadFileName()
		s.mu.Lock()
		seen := s.handled[name]
		s.mu.Unlock()
		if seen {
			continue
		}
		archive := filepath.Join(s.targetDir, name)
		fi, err := os.Stat(archive)
		if err != nil || fi.Size() == 0 || controlFileExists(archive) {
			continue // 尚未下载完成（或仍在预分配），本轮跳过
		}
		s.mu.Lock()
		s.handled[name] = true
		s.handledN++
		s.mu.Unlock()
		s.processOne(it, archive, name)
	}
}

// allHandled 报告本批曲包是否都已处理过（含解压失败与格式不支持）。
func (s *extractSession) allHandled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handledN >= len(s.items)
}

// processOne 解压单个压缩包并更新统计；可选在成功后删除压缩包。
func (s *extractSession) processOne(it aria2Item, archive, name string) {
	out := extractArchive(archive, s.root, s.cfg.Layout)

	s.mu.Lock()
	s.stat.Processed++
	s.stat.Files += out.Files
	s.stat.Skipped += out.Skipped
	s.stat.Rejected += out.Rejected
	switch {
	case out.Unsupported:
		s.stat.Unsupported++
		s.badFmt = append(s.badFmt, extractFailure{Tag: it.Pack.Tag, Archive: name, Reason: "格式不支持"})
	case out.failed():
		s.stat.Failed++
		s.failures = append(s.failures, extractFailure{Tag: it.Pack.Tag, Archive: name, Reason: out.reason()})
	default:
		s.stat.Succeeded++
		s.okNames[name] = true
	}
	s.mu.Unlock()

	if len(out.Reasons) > 0 && !out.Unsupported {
		msgf("解压告警: %s %s", it.Pack.Tag, strings.Join(out.Reasons, "；"))
	}
	// 只有整包干净解压成功才删除压缩包；失败（含越界条目）保留原包以便复核。
	if s.cfg.DeleteAfter && !out.Unsupported && !out.failed() {
		if err := os.Remove(archive); err != nil {
			msgf("注意: 解压成功但删除压缩包失败: %s: %v", name, err)
		}
	}
	s.emitProgress(false)
}

// emitProgress 输出解压进度行；内容未变化时不重复输出。
func (s *extractSession) emitProgress(force bool) {
	if ResolveProgressMode(ProgressMode, isStdoutTerminal()) == progressOffMode {
		return
	}
	line := s.progressLine()
	s.mu.Lock()
	if !force && line == s.lastLine {
		s.mu.Unlock()
		return
	}
	s.lastLine = line
	s.mu.Unlock()
	msgf("%s", line)
}

// progressLine 生成解压进度文本，至少包含已处理/总数与失败数。
func (s *extractSession) progressLine() string {
	s.mu.Lock()
	st := s.stat
	s.mu.Unlock()
	parts := []string{
		fmt.Sprintf("解压: 已处理 %d/%d", st.Processed, st.Total),
		fmt.Sprintf("成功 %d", st.Succeeded),
		fmt.Sprintf("失败 %d", st.Failed),
	}
	if st.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("跳过已存在文件 %d", st.Skipped))
	}
	if st.Unsupported > 0 {
		parts = append(parts, fmt.Sprintf("格式不支持 %d", st.Unsupported))
	}
	return strings.Join(parts, "，")
}

// reportSummary 输出解压收尾统计，并列出失败与格式不支持的曲包以便复查。
func (s *extractSession) reportSummary() {
	s.mu.Lock()
	st := s.stat
	failures := append([]extractFailure(nil), s.failures...)
	badFmt := append([]extractFailure(nil), s.badFmt...)
	setupErr := s.setupErr
	s.mu.Unlock()

	if setupErr != "" {
		msgf("解压结果: 未执行（%s）", setupErr)
		return
	}
	msgf("解压结果: 已处理 %d/%d，成功 %d，失败 %d，新增文件 %d，跳过已存在文件 %d",
		st.Processed, st.Total, st.Succeeded, st.Failed, st.Files, st.Skipped)
	if len(failures) > 0 {
		msgf("解压失败曲包: %s", joinFailures(failures))
	}
	if len(badFmt) > 0 {
		msgf("格式不支持（已跳过，保留原压缩包）: %s", joinFailures(badFmt))
	}
	if st.Rejected > 0 {
		msgf("注意: 有 %d 个越界条目被拒绝，解压目录之外未产生任何文件。", st.Rejected)
	}
}

// snapshot 返回统计快照（深拷贝切片与映射，供会话结束后读取）。
func (s *extractSession) snapshot() *extractStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.stat
	out.Failures = append([]extractFailure(nil), s.failures...)
	out.UnsupportedPacks = append([]extractFailure(nil), s.badFmt...)
	out.SucceededArchives = make(map[string]bool, len(s.okNames))
	for k, v := range s.okNames {
		out.SucceededArchives[k] = v
	}
	return &out
}

// joinFailures 把失败明细拼成一行。
func joinFailures(items []extractFailure) string {
	parts := make([]string, 0, len(items))
	for _, f := range items {
		parts = append(parts, f.String())
	}
	return strings.Join(parts, ", ")
}

// countVerifiableFiles 端到端验收口径：未启用解压时等同 CountExpectedFiles；
// 启用解压时，压缩包存在或该曲包本次已成功解压（压缩包可能已被删除）都算完成。
func countVerifiableFiles(dir string, extraction *extractStats) int {
	if extraction == nil {
		return CountExpectedFiles(dir)
	}
	accepted := make(map[string]bool)
	for _, pattern := range []string{"*.zip", "*.7z"} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			continue
		}
		for _, m := range matches {
			if controlFileExists(m) {
				continue
			}
			if fi, err := os.Stat(m); err == nil && fi.Size() > 0 {
				accepted[filepath.Base(m)] = true
			}
		}
	}
	for name := range extraction.SucceededArchives {
		accepted[name] = true
	}
	return len(accepted)
}

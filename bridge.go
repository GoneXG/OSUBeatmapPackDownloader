package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// bridgeProtocolVersion 是脚本与本地进程约定的载荷协议版本。
// 版本不匹配时本地进程拒绝载荷并给出可读提示，而不是静默失败。
const bridgeProtocolVersion = 1

const (
	// defaultBridgePort 桥接服务的默认端口；被占用时依次回退到后续端口。
	defaultBridgePort = 27183
	// bridgePortAttempts 端口回退时最多尝试的端口数量。
	bridgePortAttempts = 32
	// maxBridgePayloadBytes 单个请求体的上限，超过即拒绝，避免超大载荷打爆内存。
	maxBridgePayloadBytes = 8 << 20
	// defaultHandshakeTimeout 拉起浏览器后等待脚本握手的默认上限。
	defaultHandshakeTimeout = 60 * time.Second
	// bridgeHeartbeatStale 心跳超过该时长即认为脚本已失联（页面被关闭或脚本被停用）。
	bridgeHeartbeatStale = 15 * time.Second
	// bridgeResolveTimeout 一轮浏览器解析等待回执的上限。
	bridgeResolveTimeout = 3 * time.Minute

	// osuSiteOrigin 目标站点来源；带外请求携带该 Origin 时才允许通过来源校验。
	osuSiteOrigin = "https://osu.ppy.sh"
	// bridgeTokenHeader 一次性凭据的请求头名。
	bridgeTokenHeader = "X-Bridge-Token"
)

// 桥接请求被拒绝的原因分类：用于把「脚本没装」与「脚本连上了但被拒绝」区分开。
const (
	rejectReasonOrigin = "来源网页不在白名单"
	rejectReasonToken  = "凭据缺失或不匹配"
)

// 脚本解析单个曲包详情页后的结果分类。
const (
	resolveStatusOK        = "ok"         // 解析到真实下载链接
	resolveStatusNoLink    = "no-link"    // 已登录，但官网未提供下载地址
	resolveStatusNeedLogin = "need-login" // 当前会话未登录
)

// 脚本上报的终止原因。
const (
	bridgeFailNeedLogin = "need-login"
	bridgeFailError     = "error"
)

var (
	// errScriptNotDetected 等待握手超时：浏览器里没有可用的桥接脚本。
	errScriptNotDetected = errors.New("未检测到脚本")
	// errHandshakeIncomplete 收到了脚本的请求但没有握手：脚本与程序版本可能不匹配。
	errHandshakeIncomplete = errors.New("脚本未完成握手")
	// errScriptHalted 脚本主动终止（未登录或抓取出错）。
	errScriptHalted = errors.New("脚本已终止")
)

// bridgeTask 交给脚本解析的单个曲包详情页。
type bridgeTask struct {
	ID      int64  `json:"id"`
	Tag     string `json:"tag"`
	PageURL string `json:"url"`
}

// bridgeResolveResult 脚本对一个解析任务的回执。
type bridgeResolveResult struct {
	ID      int64  `json:"id"`
	Tag     string `json:"tag"`
	Status  string `json:"status"`
	Href    string `json:"href"`
	Message string `json:"message"`
}

// bridgeHandshake 脚本完成握手时上报的状态。
type bridgeHandshake struct {
	Protocol int    `json:"protocol"`
	Type     string `json:"type"`
	LoggedIn bool   `json:"loggedIn"`
	Message  string `json:"message"`
}

// bridgeFailure 脚本在上报过程中主动终止时的说明。
type bridgeFailure struct {
	Protocol int    `json:"protocol"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
}

// bridgeProgress 脚本侧的抓取进度事件（用于本地进程打印可读进度）。
type bridgeProgress struct {
	Protocol int    `json:"protocol"`
	Stage    string `json:"stage"`
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	Message  string `json:"message"`
}

// bridgeConfig 桥接服务的启动配置。
type bridgeConfig struct {
	token          string
	allowedOrigins []string
	port           int
	portAttempts   int
	// resolveConcurrency 下发给脚本的解析并发上限（1~8）。
	resolveConcurrency int
}

// bridgeServer 是本地进程侧的桥接服务：只绑回环地址，校验一次性凭据与来源，
// 接收脚本回传的握手、心跳、进度、载荷与解析结果，并把解析任务下发给脚本。
type bridgeServer struct {
	token   string
	allowed map[string]bool
	port    int
	ln      net.Listener
	srv     *http.Server
	done    chan struct{}
	once    sync.Once

	handshakeC chan bridgeHandshake
	packsC     chan PackListPayload
	failC      chan bridgeFailure
	resolveC   chan bridgeResolveResult

	lastBeat atomic.Int64 // UnixNano
	// sawTraffic 是否收到过通过校验的脚本请求（用于区分「脚本没装」与「脚本版本不匹配」）。
	sawTraffic atomic.Bool
	// rejected 被拒绝的请求数；lastRejectReason 为最近一次拒绝原因，便于排查
	// 「脚本已安装、程序却收不到任何请求」这类问题（例如凭据/来源校验把请求挡掉了）。
	rejected         atomic.Int32
	rejectMu         sync.Mutex
	lastRejectReason string
	// startAt 服务启动时刻，用于在完全没有脚本消息时判断等待是否超时。
	startAt time.Time

	// pendingMu/pending 暂存「先到载荷、后到握手」时收到的载荷，供 WaitPayload 取用。
	pendingMu sync.Mutex
	pending   *PackListPayload

	resolveConc atomic.Int32

	taskMu     sync.Mutex
	queue      []bridgeTask
	nextID     int64
	noMoreTask bool
}

// newBridgeToken 生成一次运行使用的一次性凭据。
func newBridgeToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成桥接凭据失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// startBridgeServer 在回环地址上启动桥接服务。
// 端口固定，被占用时依次尝试后续端口，最终端口通过 Port 回报给调用方。
func startBridgeServer(cfg bridgeConfig) (*bridgeServer, error) {
	if cfg.token == "" {
		token, err := newBridgeToken()
		if err != nil {
			return nil, err
		}
		cfg.token = token
	}
	if cfg.port == 0 {
		cfg.port = defaultBridgePort
	}
	if cfg.portAttempts <= 0 {
		cfg.portAttempts = bridgePortAttempts
	}
	if len(cfg.allowedOrigins) == 0 {
		cfg.allowedOrigins = []string{osuSiteOrigin}
	}

	ln, port, err := listenLoopback(cfg.port, cfg.portAttempts)
	if err != nil {
		return nil, err
	}

	s := &bridgeServer{
		token:      cfg.token,
		allowed:    make(map[string]bool, len(cfg.allowedOrigins)),
		port:       port,
		ln:         ln,
		done:       make(chan struct{}),
		handshakeC: make(chan bridgeHandshake, 1),
		packsC:     make(chan PackListPayload, 1),
		failC:      make(chan bridgeFailure, 4),
		resolveC:   make(chan bridgeResolveResult, 256),
	}
	for _, o := range cfg.allowedOrigins {
		s.allowed[strings.ToLower(strings.TrimSpace(o))] = true
	}
	if cfg.resolveConcurrency <= 0 {
		cfg.resolveConcurrency = defaultLookupConcurrency
	}
	s.SetResolveConcurrency(cfg.resolveConcurrency)
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	s.startAt = time.Now()
	s.beat()
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// listenLoopback 只在回环地址上监听，优先使用 preferPort，被占用时回退到后续端口。
func listenLoopback(preferPort, attempts int) (net.Listener, int, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		port := preferPort + i
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			return ln, port, nil
		}
		lastErr = err
	}
	return nil, 0, fmt.Errorf("回环地址 %d~%d 端口均不可用: %w", preferPort, preferPort+attempts-1, lastErr)
}

// Port 返回最终选定的监听端口。
func (s *bridgeServer) Port() int { return s.port }

// BaseURL 返回脚本访问桥接服务的地址。
func (s *bridgeServer) BaseURL() string {
	return "http://127.0.0.1:" + strconv.Itoa(s.port)
}

// Close 停止桥接服务；重复调用安全。
func (s *bridgeServer) Close() error {
	var err error
	s.once.Do(func() {
		close(s.done)
		err = s.srv.Close()
	})
	return err
}

// Done 在服务关闭时关闭。
func (s *bridgeServer) Done() <-chan struct{} { return s.done }

// SetResolveConcurrency 设置下发给脚本的解析并发上限，并收敛到 1~8。
func (s *bridgeServer) SetResolveConcurrency(n int) {
	if n < 1 {
		n = 1
	}
	if n > maxLookupConcurrency {
		n = maxLookupConcurrency
	}
	s.resolveConc.Store(int32(n))
}

// beat 记录一次心跳。
func (s *bridgeServer) beat() { s.lastBeat.Store(time.Now().UnixNano()) }

// LastHeartbeatAge 返回距最近一次心跳的时长；从未收到心跳时返回 0。
func (s *bridgeServer) LastHeartbeatAge() time.Duration {
	last := s.lastBeat.Load()
	if last == 0 {
		return 0
	}
	return time.Since(time.Unix(0, last))
}

// HeartbeatStale 判断脚本心跳是否已经超时（页面被关闭或脚本停止）。
func (s *bridgeServer) HeartbeatStale() bool {
	age := s.LastHeartbeatAge()
	return age > bridgeHeartbeatStale
}

// ServeHTTP 实现脚本侧的全部协议端点：先做来源与凭据校验，再按路径分发。
func (s *bridgeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBridgeError(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	if !s.authorized(r) {
		// 校验失败的请求必须在解析载荷之前拒绝，保证没有任何副作用。
		writeBridgeError(w, http.StatusForbidden, "凭据或来源校验失败")
		return
	}
	// 能走到这里说明请求确实来自本次运行的脚本端。
	s.sawTraffic.Store(true)
	body, err := readBridgeBody(r)
	if err != nil {
		writeBridgeError(w, http.StatusBadRequest, err.Error())
		return
	}

	switch r.URL.Path {
	case "/v1/hello":
		s.handleHello(w, body)
	case "/v1/heartbeat":
		s.handleHeartbeat(w, body)
	case "/v1/progress":
		s.handleProgress(w, body)
	case "/v1/packs":
		s.handlePacks(w, body)
	case "/v1/fail":
		s.handleFail(w, body)
	case "/v1/poll":
		s.handlePoll(w, body)
	case "/v1/resolve":
		s.handleResolve(w, body)
	default:
		writeBridgeError(w, http.StatusNotFound, "未知路径: "+r.URL.Path)
	}
}

// authorized 校验来源与一次性凭据。
//
// 一次性凭据是主判据：它每次运行随机生成，只经 URL 片段交给脚本、从不经过站点，
// 因此只有本机脚本拿得到。来源（Origin）只作为针对「网页」的辅助防线。
//
// 带外请求（GM_xmlhttpRequest）的 Origin 并不可靠：它可能缺失，也可能不是页面源，
// 而是浏览器扩展来源（chrome-extension://… / moz-extension://…）或在不透明上下文里的
// "null"。这些都不是网页来源，用网页白名单去卡只会把合法脚本请求全部拒掉，表现为
// 「脚本已装好、程序却一直等不到任何请求」。因此：
//   - 来源是网页来源（http/https）且不在白名单 → 拒绝；
//   - 来源缺失、为 "null"、或是扩展等非网页来源 → 以一次性凭据为准放行。
func (s *bridgeServer) authorized(r *http.Request) bool {
	origin := strings.ToLower(strings.TrimSpace(r.Header.Get("Origin")))
	if isWebOrigin(origin) && !s.allowed[origin] {
		s.noteRejected(rejectReasonOrigin + "：" + origin)
		return false
	}
	token := strings.TrimSpace(r.Header.Get(bridgeTokenHeader))
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
		s.noteRejected(rejectReasonToken)
		return false
	}
	return true
}

// isWebOrigin 判断来源是否为普通网页来源（http/https）。
// "null"、扩展来源等非网页来源返回 false。
func isWebOrigin(origin string) bool {
	return strings.HasPrefix(origin, "http://") || strings.HasPrefix(origin, "https://")
}

// noteRejected 记录一次被拒绝的请求（绝不记录凭据内容），并即时打印原因，
// 避免「脚本已装但请求被拒」表现为无声的「未检测到脚本」而无法自查。
func (s *bridgeServer) noteRejected(reason string) {
	s.rejected.Add(1)
	s.rejectMu.Lock()
	s.lastRejectReason = reason
	s.rejectMu.Unlock()
	msgf("      [桥接] 已拒绝一个请求：%s", reason)
}

// Rejections 返回被拒绝请求的次数与最近一次原因。
func (s *bridgeServer) Rejections() (int, string) {
	s.rejectMu.Lock()
	defer s.rejectMu.Unlock()
	return int(s.rejected.Load()), s.lastRejectReason
}

// readBridgeBody 读取请求体并强制上限。
func readBridgeBody(r *http.Request) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBridgePayloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取请求体失败: %w", err)
	}
	if len(raw) > maxBridgePayloadBytes {
		return nil, fmt.Errorf("载荷超过上限 %d 字节", maxBridgePayloadBytes)
	}
	return raw, nil
}

// checkProtocol 校验载荷协议版本，版本不匹配时给出可读提示。
func checkProtocol(v int) error {
	if v != bridgeProtocolVersion {
		return fmt.Errorf("协议版本不匹配：脚本为 %d，本程序支持 %d，请更新脚本或程序", v, bridgeProtocolVersion)
	}
	return nil
}

func (s *bridgeServer) handleHello(w http.ResponseWriter, body []byte) {
	var hs bridgeHandshake
	if err := json.Unmarshal(body, &hs); err != nil {
		writeBridgeError(w, http.StatusBadRequest, "握手载荷不是合法 JSON")
		return
	}
	if err := checkProtocol(hs.Protocol); err != nil {
		writeBridgeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.beat()
	select {
	case s.handshakeC <- hs:
	default:
	}
	writeBridgeOK(w, map[string]any{"ok": true})
}

func (s *bridgeServer) handleHeartbeat(w http.ResponseWriter, body []byte) {
	var ev bridgeProgress
	if err := json.Unmarshal(body, &ev); err != nil {
		writeBridgeError(w, http.StatusBadRequest, "心跳载荷不是合法 JSON")
		return
	}
	if err := checkProtocol(ev.Protocol); err != nil {
		writeBridgeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.beat()
	writeBridgeOK(w, map[string]any{"ok": true})
}

func (s *bridgeServer) handleProgress(w http.ResponseWriter, body []byte) {
	var ev bridgeProgress
	if err := json.Unmarshal(body, &ev); err != nil {
		writeBridgeError(w, http.StatusBadRequest, "进度载荷不是合法 JSON")
		return
	}
	if err := checkProtocol(ev.Protocol); err != nil {
		writeBridgeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.beat()
	if ev.Message != "" {
		msgf("      [脚本] %s", ev.Message)
	} else if ev.Total > 0 {
		msgf("      [脚本] %s：%d/%d", ev.Stage, ev.Done, ev.Total)
	}
	writeBridgeOK(w, map[string]any{"ok": true})
}

func (s *bridgeServer) handlePacks(w http.ResponseWriter, body []byte) {
	payload, err := ParsePackListPayload(body)
	if err != nil {
		writeBridgeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.beat()
	select {
	case s.packsC <- payload:
	default:
	}
	writeBridgeOK(w, map[string]any{"ok": true, "count": len(payload.Packs)})
}

func (s *bridgeServer) handleFail(w http.ResponseWriter, body []byte) {
	var f bridgeFailure
	if err := json.Unmarshal(body, &f); err != nil {
		writeBridgeError(w, http.StatusBadRequest, "终止载荷不是合法 JSON")
		return
	}
	if err := checkProtocol(f.Protocol); err != nil {
		writeBridgeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.beat()
	select {
	case s.failC <- f:
	default:
	}
	writeBridgeOK(w, map[string]any{"ok": true})
}

func (s *bridgeServer) handlePoll(w http.ResponseWriter, body []byte) {
	var req bridgeProgress
	if err := json.Unmarshal(body, &req); err != nil {
		writeBridgeError(w, http.StatusBadRequest, "轮询载荷不是合法 JSON")
		return
	}
	if err := checkProtocol(req.Protocol); err != nil {
		writeBridgeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.beat()
	tasks, done := s.takeTasks()
	writeBridgeOK(w, map[string]any{
		"ok":          true,
		"tasks":       tasks,
		"done":        done,
		"concurrency": int(s.resolveConc.Load()),
	})
}

func (s *bridgeServer) handleResolve(w http.ResponseWriter, body []byte) {
	var res bridgeResolveResult
	if err := json.Unmarshal(body, &res); err != nil {
		writeBridgeError(w, http.StatusBadRequest, "解析回执不是合法 JSON")
		return
	}
	s.beat()
	select {
	case s.resolveC <- res:
	default:
	}
	writeBridgeOK(w, map[string]any{"ok": true})
}

// takeTasks 取出当前待解析任务；noMoreTask 置位后同时告知脚本可以收工。
func (s *bridgeServer) takeTasks() ([]bridgeTask, bool) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	tasks := s.queue
	s.queue = nil
	return tasks, s.noMoreTask && len(tasks) == 0
}

// WaitHandshake 等待脚本握手，超时返回「未检测到脚本」，且服务继续存活、可重试。
//
// 等待不是「固定 timeout 后放弃」：只要脚本还在持续上报（心跳/进度），就继续等待，
// 避免一次完整抓取（数千个曲包、上百秒）被误判成脚本没装。只有在完全没有脚本消息、
// 或脚本长时间静默时才给出结论——后者会明确指出「脚本连上了但没握手（多半是旧版脚本）」。
func (s *bridgeServer) WaitHandshake(ctx context.Context, timeout time.Duration) (bridgeHandshake, error) {
	if timeout <= 0 {
		timeout = defaultHandshakeTimeout
	}
	// 检查间隔最多 1 秒，短超时（单测）也能及时返回。
	tick := timeout
	if tick > time.Second {
		tick = time.Second
	}
	idle := time.NewTicker(tick)
	defer idle.Stop()

	for {
		select {
		case hs := <-s.handshakeC:
			return hs, nil
		case p := <-s.packsC:
			// 脚本没有握手却直接回传了列表：说明它已经跑通抓取（登录态没问题），
			// 只是脚本版本与本程序不一致。接受载荷并如实说明，避免用户卡在这一步。
			s.stashPayload(p)
			return bridgeHandshake{
				Protocol: bridgeProtocolVersion,
				Type:     p.Type,
				LoggedIn: true,
				Message:  "脚本未完成握手但已回传列表（脚本版本可能偏旧，建议更新脚本）",
			}, nil
		case f := <-s.failC:
			if f.Kind == bridgeFailNeedLogin {
				return bridgeHandshake{
					Protocol: bridgeProtocolVersion,
					LoggedIn: false,
					Message:  f.Message,
				}, nil
			}
			return bridgeHandshake{}, fmt.Errorf("%w: %s", errScriptHalted, f.Message)
		case <-s.done:
			return bridgeHandshake{}, errScriptNotDetected
		case <-ctx.Done():
			return bridgeHandshake{}, ctx.Err()
		case <-idle.C:
			if s.sawTraffic.Load() {
				// 收到过脚本请求：只要它没有静默超过 timeout 就继续等。
				if s.LastHeartbeatAge() <= timeout {
					continue
				}
				return bridgeHandshake{}, errHandshakeIncomplete
			}
			if time.Since(s.startAt) > timeout {
				return bridgeHandshake{}, errScriptNotDetected
			}
		}
	}
}

// stashPayload 暂存先到的载荷。
func (s *bridgeServer) stashPayload(p PackListPayload) {
	s.pendingMu.Lock()
	s.pending = &p
	s.pendingMu.Unlock()
}

// takePendingPayload 取出暂存的载荷（如有）。
func (s *bridgeServer) takePendingPayload() (PackListPayload, bool) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if s.pending == nil {
		return PackListPayload{}, false
	}
	p := *s.pending
	s.pending = nil
	return p, true
}

// WaitPayload 等待脚本回传曲包列表；脚本主动终止或心跳中断时返回可读错误。
func (s *bridgeServer) WaitPayload(ctx context.Context) (PackListPayload, error) {
	if p, ok := s.takePendingPayload(); ok {
		return p, nil
	}
	watch := time.NewTicker(bridgeHeartbeatStale)
	defer watch.Stop()
	for {
		select {
		case p := <-s.packsC:
			return p, nil
		case f := <-s.failC:
			return PackListPayload{}, fmt.Errorf("%w: %s", errScriptHalted, f.Message)
		case <-s.done:
			return PackListPayload{}, errScriptHalted
		case <-watch.C:
			// 页面被关闭或脚本被停用：抓取永远不会再有结果，立即给出可读结论。
			if s.HeartbeatStale() {
				return PackListPayload{}, fmt.Errorf("%w: 与脚本的连接中断（页面可能已关闭或脚本被停用）", errScriptHalted)
			}
		case <-ctx.Done():
			return PackListPayload{}, ctx.Err()
		}
	}
}

// SubmitResolve 把一批曲包交给脚本解析并等待回执，结果与输入一一对应。
func (s *bridgeServer) SubmitResolve(ctx context.Context, packs []Pack, timeout time.Duration) ([]bridgeResolveResult, error) {
	results := make([]bridgeResolveResult, len(packs))
	if len(packs) == 0 {
		return results, nil
	}
	if timeout <= 0 {
		timeout = bridgeResolveTimeout
	}

	byID := make(map[int64]int, len(packs))
	s.taskMu.Lock()
	for i, p := range packs {
		s.nextID++
		id := s.nextID
		s.queue = append(s.queue, bridgeTask{ID: id, Tag: p.Tag, PageURL: p.PageURL})
		byID[id] = i
	}
	s.taskMu.Unlock()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	watch := time.NewTicker(bridgeHeartbeatStale)
	defer watch.Stop()
	pending := len(packs)
	for pending > 0 {
		select {
		case res := <-s.resolveC:
			idx, ok := byID[res.ID]
			if !ok {
				continue
			}
			results[idx] = res
			delete(byID, res.ID)
			pending--
		case <-deadline.C:
			return results, fmt.Errorf("等待脚本解析超时（%d/%d 已回执）", len(packs)-pending, len(packs))
		case <-watch.C:
			if s.HeartbeatStale() {
				return results, fmt.Errorf("%w: 与脚本的连接中断（页面可能已关闭或脚本被停用）", errScriptHalted)
			}
		case <-s.done:
			return results, errScriptHalted
		case <-ctx.Done():
			return results, ctx.Err()
		}
	}
	return results, nil
}

// FinishResolve 告知脚本没有新的解析任务，可以停止轮询。
func (s *bridgeServer) FinishResolve() {
	s.taskMu.Lock()
	s.noMoreTask = true
	s.taskMu.Unlock()
}

func writeBridgeOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeBridgeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": message})
}

// ParsePackListPayload 解析并校验脚本（或本地列表文件）回传的曲包列表载荷。
func ParsePackListPayload(raw []byte) (PackListPayload, error) {
	if len(raw) == 0 {
		return PackListPayload{}, errors.New("载荷为空")
	}
	if len(raw) > maxBridgePayloadBytes {
		return PackListPayload{}, fmt.Errorf("载荷超过上限 %d 字节", maxBridgePayloadBytes)
	}
	var payload PackListPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return PackListPayload{}, fmt.Errorf("载荷不是合法 JSON: %w", err)
	}
	if err := checkProtocol(payload.Protocol); err != nil {
		return PackListPayload{}, err
	}
	if strings.TrimSpace(payload.Type) == "" {
		return PackListPayload{}, errors.New("载荷缺少分类（type）")
	}
	if len(payload.Packs) == 0 {
		return PackListPayload{}, errors.New("载荷不含任何曲包")
	}
	if payload.Total != len(payload.Packs) {
		return PackListPayload{}, fmt.Errorf("载荷条目总数不一致：头部为 %d，实际为 %d", payload.Total, len(payload.Packs))
	}
	for i, p := range payload.Packs {
		if strings.TrimSpace(p.Tag) == "" || strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.URL) == "" {
			return PackListPayload{}, fmt.Errorf("第 %d 个曲包字段不完整（tag/name/url 均必填）", i+1)
		}
	}
	return payload, nil
}

// LoadPackListFile 读取本地列表文件（脚本不可用时的兜底入口）并复用同一套载荷校验。
func LoadPackListFile(path string) (PackListPayload, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return PackListPayload{}, fmt.Errorf("读取本地列表文件失败: %w", err)
	}
	return ParsePackListPayload(raw)
}

// bridgeJobURL 把一次性凭据、桥接端口与目标分类写入 URL 片段。
// 片段不会随请求发送给站点，因此凭据不会进入站点日志；查询参数中不含任何凭据。
func bridgeJobURL(pageURL, token string, port int, siteType string) (string, error) {
	u, err := url.Parse(pageURL)
	if err != nil {
		return "", fmt.Errorf("解析列表页地址失败: %w", err)
	}
	u.Fragment = "opd=" + url.QueryEscape(token) +
		"&port=" + strconv.Itoa(port) +
		"&type=" + url.QueryEscape(siteType)
	return u.String(), nil
}

// openBrowserFunc 拉起默认浏览器；测试可替换实现。
var openBrowserFunc = openBrowser

// openBrowser 用系统默认浏览器打开给定地址。
func openBrowser(target string) error {
	switch runtime.GOOS {
	case "windows":
		// rundll32 直接转交协议处理程序，避免 cmd start 的引号转义问题。
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
	case "darwin":
		return exec.Command("open", target).Start()
	default:
		return exec.Command("xdg-open", target).Start()
	}
}

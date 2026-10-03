// ==UserScript==
// @name         osu! Pack Bridge
// @namespace    https://github.com/GoneXG/OSUBeatmapPackDownloader
// @version      1.0.2
// @description  在本机浏览器里抓取 osu! 官方曲包列表并解析真实下载链接，经回环地址回传给 osu! 曲包下载器。
// @author       GoneXG
// @license      MIT
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

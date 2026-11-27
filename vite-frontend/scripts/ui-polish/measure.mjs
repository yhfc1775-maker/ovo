// Local measurement harness, not a product test suite. Run against
// VITE_DEMO=true vite dev; recordings stay untouched on disk.
import { chromium } from "playwright-core";
import { readFile, mkdir, writeFile } from "node:fs/promises";

const base = process.env.UI_BASE || "http://127.0.0.1:3115";
const out = process.env.UI_OUT || "/tmp/ovo-ui-measure";
const count = Number(process.env.UI_COUNT || 40);
const fixtures = JSON.parse(await readFile("src/demo/fixtures.json", "utf8"));
const lists = ["node/list", "tunnel/list", "forward/list", "inbound/list",
  "user/list", "speed-limit/list", "monitor/nodes", "monitor/tunnels"];
const sourceCounts = {};
for (const [role, book] of Object.entries(fixtures)) {
  if (!book || typeof book !== "object") continue;
  for (const key of lists) {
    for (const entry of book[key] || []) {
      const seed = entry.res?.data;
      if (!Array.isArray(seed) || !seed.length) continue;
      sourceCounts[`${role}/${key}`] = seed.length;
      entry.res.data = Array.from({ length: count }, (_, i) => ({
        ...structuredClone(seed[i % seed.length]), id: i + 1, inx: i,
        name: `${seed[i % seed.length].name || key} · ${i + 1}`,
      }));
    }
  }
}
await mkdir(out, { recursive: true });
const browser = await chromium.launch({
  executablePath: process.env.CHROME || "/home/adminuser/.cache/ms-playwright/chromium-1187/chrome-linux/chrome",
  headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"],
});

async function open(role = "admin", width = 1366, reduced = false, delay = "") {
  const context = await browser.newContext({ viewport: { width, height: 900 },
    reducedMotion: reduced ? "reduce" : "no-preference", serviceWorkers: "block" });
  await context.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if (!["127.0.0.1", "localhost"].includes(url.hostname)) return route.abort();
    if (url.pathname === "/src/demo/fixtures.json") {
      return route.fulfill({ contentType: "application/javascript",
        body: `export default ${JSON.stringify(fixtures)};` });
    }
    if (url.pathname === "/src/main.tsx") {
      const res = await route.fetch();
      let body = await res.text();
      const jsxRuntime = body.match(/from "([^\"]*react_jsx-dev-runtime\.js\?v=[^\"]+)"/)?.[1];
      if (!jsxRuntime) throw new Error("could not locate the dev React runtime");
      const reactModule = jsxRuntime.replace("react_jsx-dev-runtime.js", "react.js");
      body = `import React from "${reactModule}";\nconst Profiler = React.Profiler;\n${body}`;
      body = body.replace(/_jsxDEV\(App, \{\}, void 0, false, \{[\s\S]*?\}, this\)/, app =>
        `React.createElement(Profiler, { id: "app", onRender: (_id, phase, actualDuration, baseDuration, startTime, commitTime) => window.__commits.push({ at: commitTime, phase, actualDuration, baseDuration, startTime }) }, ${app})`,
      );
      return route.fulfill({ response: res, body });
    }
    if (url.pathname === "/src/demo/index.ts" && delay) {
      const res = await route.fetch();
      let body = await res.text();
      body = body.replace("async function answer(config) {", `async function answer(config) {
        if ((config.url || '').includes(${JSON.stringify(delay)})) {
          window.__slowStarted = performance.now();
          await new Promise(r => setTimeout(r, 8000));
        }`);
      return route.fulfill({ response: res, body });
    }
    return route.continue();
  });
  await context.addInitScript(({ role }) => {
    const payload = { sub: role === "admin" ? "1" : "2", role_id: role === "admin" ? 0 : 1,
      user: role === "admin" ? "admin@user" : "xiaoming", exp: 4102444800 };
    localStorage.setItem("token", `demo.${btoa(JSON.stringify(payload))}.demo`);
    localStorage.setItem("name", payload.user);
    window.__commits = [];
    window.__longTasks = [];
    new PerformanceObserver((list) => {
      window.__longTasks.push(...list.getEntries().map(e => ({ at: e.startTime, ms: e.duration })));
    }).observe({ type: "longtask", buffered: true });
  }, { role });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));
  const cdp = await context.newCDPSession(page);
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: 4 });
  return { context, page, errors };
}

async function snapshot(page) {
  return page.evaluate(() => {
    const w = innerWidth;
    const overflow = [...document.querySelectorAll("body *")].filter(el => {
      const r = el.getBoundingClientRect();
      if (!r.width || !r.height || r.top > innerHeight || r.bottom < 0) return false;
      if (r.left >= -1 && r.right <= w + 1) return false;
      let p = el.parentElement;
      while (p && p !== document.body) {
        if (["auto", "scroll", "hidden", "clip"].includes(getComputedStyle(p).overflowX)) return false;
        p = p.parentElement;
      }
      return true;
    }).map(e => ({ tag: e.tagName, text: e.textContent?.slice(0, 75), width: Math.round(e.getBoundingClientRect().width) }));
    return { elements: document.querySelectorAll("*").length,
      rows: document.querySelectorAll("tbody tr").length,
      textLength: document.body.innerText.length, overflow,
      commits: window.__commits, longTasks: window.__longTasks,
      loading: [...document.querySelectorAll('[role="status"], [aria-busy="true"]')].map(e => e.textContent),
      buttons: [...document.querySelectorAll("button")].map(e => ({ text: e.textContent,
        label: e.getAttribute("aria-label"), title: e.getAttribute("title"),
        icons: [...e.querySelectorAll("svg")].map(svg => svg.getAttribute("class")) })),
    };
  });
}

const result = { count, sourceCounts, browser: browser.version(), cpuSlowdown: 4, pages: [] };
const mode = process.env.UI_MODE || "scan";
if (mode === "scan") {
  for (const width of [1366, 390]) {
    for (const role of ["admin", "user"]) {
      const h = await open(role, width);
      const allPaths = role === "admin"
        ? ["dashboard", "node", "tunnel", "forward", "inbound", "relay", "subscription", "user", "limit", "config", "profile", "change-password"]
        : ["dashboard", "my-subscription", "forward", "monitor", "profile"];
      const mobilePaths = role === "admin"
        ? ["dashboard", "node", "tunnel", "forward"]
        : ["dashboard", "forward", "monitor", "profile"];
      const paths = width === 390 ? mobilePaths : allPaths;
      await h.page.goto(`${base}/#/dashboard`);
      await h.page.waitForTimeout(6500);
      for (const path of paths) {
        await h.page.evaluate(() => { window.__commits = []; window.__longTasks = []; });
        const start = await h.page.evaluate(() => performance.now());
        const beforeCommitCount = await h.page.evaluate(() => window.__commits.length);
        await h.page.evaluate(path => { location.hash = `#/${path}`; }, path);
        await h.page.waitForTimeout(3500);
        const snap = await snapshot(h.page);
        const routeCommits = snap.commits.slice(beforeCommitCount);
        result.pages.push({ role, width, path, elapsed: performance.now() - start,
          firstCommitMs: routeCommits[0]?.at ? routeCommits[0].at - start : null,
          ...snap, errors: [...h.errors] });
        await h.page.screenshot({ path: `${out}/${role}-${width}-${path}.png`, fullPage: false });
        console.log(`${role} ${width} ${path}: ${snap.elements} elements, ${snap.rows} rows, ${snap.overflow.length} overflow, ${h.errors.length} errors`);
        if (role === "admin" && path === "node") {
          const started = await h.page.evaluate(() => performance.now());
          await h.page.getByRole("button", { name: "筛选" }).click();
          await h.page.waitForFunction(() => document.querySelectorAll('[role="dialog"]').length > 0);
          const elapsed = await h.page.evaluate(started => performance.now() - started, started);
          result.pages.push({ role, width, path: "node-filter-open", interactionMs: elapsed,
            dialogs: await h.page.locator('[role="dialog"]').count() });
          await h.page.keyboard.press("Escape");
        }
        if (role === "admin" && path === "limit") {
          const toggle = h.page.locator("button:has(svg.lucide-list)").first();
          const started = await h.page.evaluate(() => performance.now());
          if (await toggle.count()) {
            await toggle.click();
            await h.page.locator("table").waitFor({ state: "visible", timeout: 3000 });
          }
          const elapsed = await h.page.evaluate(started => performance.now() - started, started);
          result.pages.push({ role, width, path: "limit-list", interactionMs: elapsed, ...await snapshot(h.page) });
          await h.page.locator("button:has(svg.lucide-layout-grid)").first().click();
          await h.page.screenshot({ path: `${out}/admin-${width}-limit-list.png`, fullPage: false });
        }
      }
      await h.context.close();
    }
  }
} else if (mode === "single") {
  const role = process.env.UI_ROLE || "admin";
  const path = process.env.UI_PATH || "node";
  const width = Number(process.env.UI_WIDTH || 1366);
  const h = await open(role, width);
  await h.page.goto(`${base}/#/${path}`);
  await h.page.waitForTimeout(5000);
  const snap = await snapshot(h.page);
  result.pages.push({ role, width, path, ...snap, errors: [...h.errors] });
  console.log(JSON.stringify(result.pages[0], null, 2));
  await h.page.screenshot({ path: `${out}/debug-${role}-${width}-${path}.png` });
  await h.context.close();
} else if (mode === "interact") {
  const h = await open("admin", 1366);
  const interactions = [];
  await h.page.goto(`${base}/#/dashboard`);
  await h.page.waitForTimeout(6500);
  await h.page.evaluate(() => { window.__commits = []; });
  await h.page.evaluate(() => { location.hash = "#/node"; });
  await h.page.waitForTimeout(5000);
  let started = await h.page.evaluate(() => performance.now());
  await h.page.getByRole("button", { name: "筛选" }).click();
  await h.page.waitForFunction(() => document.querySelectorAll('[role="dialog"]').length > 0);
  interactions.push({ path: "node", action: "open-filter", ms: await h.page.evaluate(started => performance.now() - started, started) });
  await h.page.keyboard.press("Escape");
  await h.page.evaluate(() => { location.hash = "#/limit"; });
  await h.page.waitForTimeout(5000);
  started = await h.page.evaluate(() => performance.now());
  await h.page.locator("button:has(svg.lucide-list)").first().click();
  await h.page.locator("table").waitFor({ state: "visible" });
  interactions.push({ path: "limit", action: "switch-to-list", ms: await h.page.evaluate(started => performance.now() - started, started) });
  result.interactions = interactions;
  console.log(JSON.stringify(interactions, null, 2));
  await h.context.close();
} else if (mode === "slow") {
  const h = await open("user", 390, false, "monitor/access");
  await h.page.goto(`${base}/#/monitor`);
  await h.page.waitForTimeout(4000);
  result.pages.push({ label: "monitor-access-4s", ...await snapshot(h.page) });
  await h.page.screenshot({ path: `${out}/monitor-slow.png` });
  await h.page.waitForTimeout(12000);
  result.pages.push({ label: "monitor-access-settled", slowStarted: await h.page.evaluate(() => window.__slowStarted ?? null), ...await snapshot(h.page) });
  await h.context.close();
} else if (mode === "motion") {
  for (const reduced of [false, true]) {
    const h = await open("admin", 1366, reduced);
    await h.page.goto(`${base}/#/limit`);
    await h.page.waitForTimeout(5000);
    // Remount the list after route chunks and fixtures are warm.
    await h.page.evaluate(() => { location.hash = "#/profile"; });
    await h.page.waitForTimeout(800);
    await h.page.evaluate(() => {
      window.__motionSamples = [];
      const start = performance.now();
      const sample = () => {
        const cards = [...document.querySelectorAll(".glass-card")].filter(e => /Mbps/.test(e.textContent));
        const visible = cards.filter(e => { const r = e.getBoundingClientRect(); return r.bottom > 0 && r.top < innerHeight; });
        const moving = [...document.querySelectorAll('[style*="transform"]')].filter(e => getComputedStyle(e).transform !== "none");
        window.__motionSamples.push({ at: performance.now() - start, cards: cards.length,
          visibleCards: visible.length, activeAnimations: document.getAnimations().filter(a => a.playState === "running").length,
          movingNodes: moving.length, movingText: moving.filter(e => /Mbps/.test(e.textContent)).length });
        if (performance.now() - start < 4000) requestAnimationFrame(sample);
      };
      location.hash = "#/limit";
      requestAnimationFrame(sample);
    });
    await h.page.waitForTimeout(4200);
    result.pages.push({ reduced, samples: await h.page.evaluate(() => window.__motionSamples), ...await snapshot(h.page) });
    await h.context.close();
  }
}
await writeFile(`${out}/results.json`, JSON.stringify(result, null, 2));
await browser.close();

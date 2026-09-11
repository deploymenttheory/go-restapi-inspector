import { chromium } from "playwright";
import assert from "node:assert/strict";
import { readFile, mkdir } from "node:fs/promises";
import { resolve, join } from "node:path";
import { pathToFileURL } from "node:url";

const [current, baseline] = process.argv.slice(2).map(p => resolve(p));
if (!current || !baseline) throw new Error("Usage: node tests/report-browser/verify-saved.mjs CURRENT_RUN_DIR BASELINE_RUN_DIR");
const output = resolve("tmp/report-review");
await mkdir(output, { recursive: true });
const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, colorScheme: "light" });
  const errors = [], requests = [];
  page.on("pageerror", e => errors.push(e.message));
  page.on("request", q => { if (/^https?:/.test(q.url())) requests.push(q.url()); });
  await page.route(/^https?:/, route => route.abort());
  for (const [name, dir, file] of [["current", current, "report.html"], ["baseline", baseline, "report.html"], ["comparison", current, "comparison.html"]]) {
    const path = join(dir, file), html = await readFile(path, "utf8");
    const encoded = html.match(/type="application\/octet-stream">([^<]+)<\/script>/)[1];
    const view = JSON.parse(Buffer.from(encoded, "base64").toString("utf8"));
    const started = performance.now();
    await page.goto(pathToFileURL(path).href);
    await page.getByRole("heading", { name: "What the API revealed" }).waitFor();
    const total = page.locator(".metric").filter({ hasText: "Total HTTP requests" }).locator("strong");
    assert.equal(await total.textContent(), view.current.metrics.http.toLocaleString("en-GB"));
    await page.screenshot({ path: join(output, name + "-overview.png"), fullPage: true });
    await page.getByRole("link", { name: "HTTP requests" }).click();
    await page.getByRole("heading", { name: "The HTTP request ledger" }).waitFor();
    assert.equal(await page.locator("tbody tr").count(), Math.min(50, view.current.requests.length));
    if (view.current.requests.length > 50) { await page.getByRole("button", { name: "Next page" }).click(); assert.match(await page.locator(".table-count").textContent(), /^51/); }
    await page.getByRole("link", { name: "Field findings" }).click();
    await page.getByRole("heading", { name: "Field findings", exact: true }).waitFor();
    await page.locator(".field-summary button").first().click();
    await page.getByRole("dialog").locator("tbody button").first().click();
    assert.match(await page.getByRole("dialog").textContent(), /Request input/);
    await page.keyboard.press("Escape");
    await page.getByRole("link", { name: "Dependencies" }).click();
    await page.getByRole("heading", { name: "How fields depend on each other" }).waitFor();
    await page.getByRole("checkbox").check();
    await page.screenshot({ path: join(output, name + "-dependencies.png"), fullPage: true });
    if (view.comparison) {
      await page.getByRole("link", { name: "Compare runs" }).click();
      await page.getByRole("heading", { name: "Compare the evidence" }).waitFor();
      assert.match(await page.locator("#view").textContent(), /change in recorded knowledge/);
      await page.screenshot({ path: join(output, name + "-findings.png"), fullPage: true });
    }
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    console.log(JSON.stringify({ snapshot: name, http: view.current.metrics.http, supported: view.current.metrics.supported, elapsedMs: Math.round(performance.now() - started) }));
  }
  assert.deepEqual(errors, []); assert.deepEqual(requests, []);
  console.log("Saved reports: browser interactions passed; no page errors or network requests.");
} finally { await browser.close(); }

import { test, expect } from "playwright/test";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const fixtures = resolve(process.env.REPORT_BROWSER_DIR || "../../tmp/report-browser");
const fixture = (file = "current/report.html") => pathToFileURL(resolve(fixtures, file)).href;

test.beforeEach(async ({ page }) => {
  page.on("pageerror", error => { throw error; });
  await page.route(/^https?:/, route => { throw new Error("Unexpected network request: " + route.request().url()); });
});

test("opens offline, accounts for traffic, and changes theme", async ({ page }) => {
  await page.goto(fixture());
  await expect(page.getByRole("heading", { name: "What the API revealed" })).toBeVisible();
  await expect(page.locator(".metric").filter({ hasText: "Total HTTP requests" }).locator("strong")).toHaveText("126");
  await expect(page.locator(".metric").filter({ hasText: "Discovery cases" }).locator("strong")).toHaveText("121");
  await expect(page.locator(".metric").filter({ hasText: "Operations inspected" }).locator("strong")).toHaveText("1 / 2");
  await page.getByRole("button", { name: "Latest session", exact: true }).click();
  await expect(page.locator("figcaption")).toContainText("Latest session");
  await page.screenshot({ path: "test-results/overview-light.png", fullPage: true });
  await page.getByRole("button", { name: "Switch colour theme" }).click();
  await page.getByRole("button", { name: "Switch colour theme" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await page.screenshot({ path: "test-results/overview-dark.png", fullPage: true });
  await page.emulateMedia({ media: "print" });
  await expect(page.locator(".sidebar")).toBeHidden();
});

test("filters and paginates the HTTP ledger without removing legacy traffic", async ({ page }) => {
  await page.goto(fixture() + "#requests");
  await expect(page.locator("tbody tr")).toHaveCount(50);
  await page.getByRole("button", { name: "Next page" }).click();
  await expect(page.locator(".table-count")).toHaveText("51–100 of 126 records");
  await page.getByRole("searchbox").fill("validation-control");
  await expect(page.locator("tbody tr")).toHaveCount(1);
  await expect(page.locator(".table-count")).toHaveText("1–1 of 1 records");
  await page.getByRole("searchbox").fill("auth");
  await page.locator("tbody button").first().click();
  await expect(page.getByRole("dialog")).toContainText("no recorded response payload or status");
});

test("follows field, rule, control and stored-state evidence safely", async ({ page }) => {
  await page.goto(fixture() + "#fields");
  await page.getByRole("button", { name: "body:/name", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText("Observed value matrix");
  await page.getByRole("dialog").getByRole("button", { name: "When /mode equals" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "evidence-002", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText("900719925474099312345");
  await expect(page.getByRole("dialog")).toContainText("Stored state before");
  await expect(page.getByRole("dialog")).toContainText("[REDACTED]");
  await expect(page.getByRole("dialog")).not.toContainText("private-value");
  await expect(page.getByRole("dialog")).not.toContainText("credential-value");
  await page.getByRole("button", { name: "Matched control →", exact: true }).click();
  await expect(page.locator("#detail-kicker")).toContainText("evidence-003");
  await page.getByRole("button", { name: "Back", exact: true }).click();
  await page.getByRole("button", { name: "Experiment & lifecycle →", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText("Associated HTTP requests");
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toBeHidden();
  expect(await page.evaluate(() => window.reportInjected)).toBeUndefined();
  await expect(page.locator("img")).toHaveCount(0);
});

test("keeps conditional gates and collapses implied OR rules", async ({ page }) => {
  await page.goto(fixture() + "#dependencies");
  await expect(page.locator(".dependency")).toHaveCount(1);
  await expect(page.locator(".dependency-gate")).toHaveText("IF / THEN");
  await expect(page.locator(".dependency-rule")).toContainText('When /mode equals "advanced", /name is present');
  await page.getByRole("checkbox").check();
  await expect(page.locator(".dependency")).toHaveCount(2);
  await page.locator(".dependency").filter({ hasText: "OR (" }).getByRole("button", { name: "Evidence →" }).click();
  await expect(page.getByRole("dialog")).toContainText("does not establish a dependency on its other field");
});

test("shows comparisons and opens baseline evidence", async ({ page }) => {
  await page.goto(fixture("current/comparison.html") + "#comparison");
  await expect(page.getByRole("heading", { name: "Compare the evidence" })).toBeVisible();
  await expect(page.locator(".comparison-pair")).toContainText("partial");
  await expect(page.locator(".comparison-pair")).toContainText("complete");
  await expect(page.locator("#view")).toContainText("Newly supported");
  await page.getByRole("button", { name: 'candidate: When /mode equals "advanced", /name is present', exact: true }).click();
  await expect(page.locator("#detail-kicker")).toContainText("BASELINE");
});

test("remains usable on a narrow screen with keyboard navigation", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(fixture());
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.keyboard.press("Tab");
  await expect(page.getByRole("link", { name: "Skip to report" })).toBeFocused();
  await page.keyboard.press("Enter");
  await page.getByRole("link", { name: "Field findings" }).click();
  await page.getByRole("button", { name: "body:/name", exact: true }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  expect(await page.getByRole("dialog").evaluate(n => n.getBoundingClientRect().width <= innerWidth)).toBe(true);
  await page.screenshot({ path: "test-results/field-mobile.png", fullPage: true });
});

test("downloads the embedded observed contract", async ({ page }) => {
  await page.goto(fixture());
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download contract" }).click();
  expect((await download).suggestedFilename()).toBe("observed-contract-with-the-facts.openapi.json");
});

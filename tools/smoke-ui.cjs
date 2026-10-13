// Requires Playwright: npm install playwright
// VIEWER_URL can select the running viewer; default http://127.0.0.1:8080.
const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs');

(async () => {
  const base = process.env.VIEWER_URL || 'http://127.0.0.1:8080';
  const executablePath = process.env.BROWSER_EXE;
  const browser = await chromium.launch({ headless: true, ...(executablePath ? { executablePath } : {}) });
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 920 }, acceptDownloads: true });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
    const info = await (await context.request.get(base + '/api/info')).json();
    assert.equal(info.open, true, 'Start the viewer with a valid database before the smoke test.');
    const roots = await (await context.request.get(base + '/api/buckets')).json();
    assert.ok(roots.items.length > 0);
    await page.goto(base);
    await page.locator('#entries tr').first().waitFor();
    await page.waitForFunction(() => document.querySelector('#status').textContent.startsWith('已连接'));
    assert.equal(await page.locator('#file-name').textContent(), info.name);
    assert.equal(await page.locator('.readonly').textContent(), '只读');
    assert.equal(await page.locator('#root-count').textContent(), String(info.buckets));
    // Choose a top-level bucket containing a value, when available.
    let chosen;
    for (const root of roots.items) {
      const p = [root.name];
      const entries = await (await context.request.get(base + '/api/entries?path=' + encodeURIComponent(JSON.stringify(p)))).json();
      if (entries.items.some(item => item.kind === 'value')) { chosen = { root, entries }; break; }
    }
    assert.ok(chosen, 'Smoke fixture needs a top-level bucket with a value.');
    await page.locator('.tree-select').filter({ hasText: chosen.root.display }).first().click();
    await page.waitForFunction(label => document.querySelector('#bucket-title').textContent === label, chosen.root.display);
    await page.locator('#detail:not([hidden])').waitFor();
    const first = chosen.entries.items.find(item => item.kind === 'value');
    await page.getByRole('row', { name: '查看记录 ' + first.display, exact: true }).click();
    await page.waitForFunction(key => document.querySelector('#selected-key').textContent === key, first.display);
    await page.getByRole('tab', { name: 'HEX', exact: true }).click();
    await page.getByRole('tab', { name: 'Base64', exact: true }).click();
    assert.equal(await page.getByRole('tab', { name: 'Base64', exact: true }).getAttribute('aria-selected'), 'true');
    const [valueDownload] = await Promise.all([page.waitForEvent('download'), page.locator('#download-value').click()]);
    const downloaded = await valueDownload.path();
    assert.equal(fs.statSync(downloaded).size, first.size);
    const [bucketDownload] = await Promise.all([page.waitForEvent('download'), page.locator('#export').click()]);
    const exported = JSON.parse(fs.readFileSync(await bucketDownload.path(), 'utf8'));
    assert.equal(exported.format, 'boltdb-viewer/v1');
    assert.equal(exported.entries.length, chosen.entries.total);
    const out = path.join(__dirname, '..', 'test-results'); fs.mkdirSync(out, { recursive: true });
    await page.getByRole('tab', { name: first.format === 'json' ? 'JSON' : '文本', exact: true }).click();
    await page.screenshot({ path: path.join(out, 'desktop.png'), fullPage: true });
    const child = chosen.entries.items.find(item => item.kind === 'bucket');
    if (child) {
      await page.locator('.tree-row.selected .tree-toggle').click();
      await page.locator('#tree').getByRole('button', { name: child.display, exact: true }).waitFor();
      await page.getByRole('row', { name: '打开 bucket ' + child.display, exact: true }).click();
      await page.waitForFunction(label => document.querySelector('#bucket-title').textContent === label, child.display);
      await page.waitForFunction(() => document.querySelector('#status').textContent.startsWith('已连接'));
      assert.equal(await page.locator('#breadcrumbs button').count(), 2);
      await page.locator('#refresh').click();
      await page.waitForFunction(label => document.querySelector('#bucket-title').textContent === label, child.display);
      await page.waitForFunction(() => document.querySelector('#status').textContent.startsWith('已连接'));
      assert.equal(await page.locator('#breadcrumbs button').count(), 2);
      await page.locator('#breadcrumbs').getByRole('button', { name: chosen.root.display, exact: true }).click();
      await page.waitForFunction(label => document.querySelector('#bucket-title').textContent === label, chosen.root.display);
      await page.locator('#entries tr').first().waitFor();
    }
    await page.keyboard.press('Control+k');
    assert.equal(await page.locator('#search').evaluate(el => el === document.activeElement), true);
    await page.locator('#search').fill('__no_such_key_68dba6__');
    await page.waitForFunction(() => document.querySelector('#list-empty h2').textContent === '没有匹配的键');
    assert.equal(await page.locator('#entries tr').count(), 0);
    // Refresh should retain the bucket and search query.
    await page.locator('#refresh').click();
    await page.waitForFunction(() => document.querySelector('#list-empty h2').textContent === '没有匹配的键');
    assert.equal(await page.locator('#search').inputValue(), '__no_such_key_68dba6__');
    await page.locator('#search').fill('');
    await page.locator('#entries tr').first().waitFor();
    await page.keyboard.press('Control+o');
    await page.locator('#open-dialog[open]').waitFor();
    await page.locator('#path-input').fill('E:\\boltdbviewer\\does-not-exist.db');
    await page.locator('#path-submit').click();
    await page.locator('#open-error:not([hidden])').waitFor();
    assert.ok((await page.locator('#open-error').textContent()).length > 0);
    await page.locator('#close-dialog').click();
    // Open a copied upload, then restore the original path so the test leaves the viewer as found.
    assert.equal(info.uploaded, false, 'Use a database opened by local path for the smoke test.');
    await page.locator('#open-db').click();
    await page.locator('#file-input').setInputFiles(info.path);
    await page.waitForFunction(() => !document.querySelector('#open-dialog').open);
    await page.waitForFunction(() => document.querySelector('#db-path').textContent === '上传的只读副本');
    await page.locator('#open-db').click();
    await page.locator('#path-input').fill(info.path);
    await page.locator('#path-submit').click();
    await page.waitForFunction(() => !document.querySelector('#open-dialog').open);
    await page.waitForFunction(original => document.querySelector('#db-path').textContent === original, info.path);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: path.join(out, 'mobile.png'), fullPage: true });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'Mobile viewport overflows horizontally.');
    // Invalid-path testing intentionally produces one HTTP 400 console message.
    const expectedError = 'Failed to load resource: the server responded with a status of 400 (Bad Request)';
    assert.equal(errors.filter(message => message === expectedError).length, 1);
    assert.deepEqual(errors.filter(message => message !== expectedError), [], 'Browser reported unexpected errors.');
    console.log('Browser smoke passed: real database, views, downloads, search, refresh, errors, upload and mobile layout.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });

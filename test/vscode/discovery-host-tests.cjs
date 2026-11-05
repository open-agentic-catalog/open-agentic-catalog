const vscode = require("vscode");
const assert = require("node:assert/strict");
const { writeFile } = require("node:fs/promises");
const { join } = require("node:path");
// Presentation-only controls use the real packaged renderer and a captured
// host projection. Actual artwork delivery/integrity owns a separate HTTP test.
async function presentation(web) {
  const original = await web.evaluate(
    "(()=>{window.removeEventListener('message',window.__catalogPresentationProbe);const value=window.__catalogPresentation;delete window.__catalogPresentationProbe;delete window.__catalogPresentation;return value;})()",
  );
  assert.ok(
    original.detail,
    "Capture actual host detail before renderer controls",
  );
  const push = (value) =>
    web.evaluate(
      `window.dispatchEvent(new MessageEvent('message',{data:{type:'state',value:${JSON.stringify(value)}}}))`,
    );
  const image = {
    index: 0,
    value:
      "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jQ1kAAAAASUVORK5CYII=",
    alt: "Owned <script>image()</script> alternative",
    caption: "Owned caption · Owned attribution",
  };
  const loaded = { ...original, image };
  loaded.detail = {
    ...original.detail,
    artwork: [{ label: "Load icon", index: 0 }],
  };
  try {
    await web.evaluate(
      "document.querySelector('#return-results').focus();document.scrollingElement.scrollTop=120",
    );
    const scroll = await web.evaluate("document.scrollingElement.scrollTop");
    assert.ok(scroll > 0, "Exercise a scrolled detail rather than page top");
    await push(loaded);
    await web.until(
      "document.querySelector('#selected-artwork img')?.naturalWidth===1",
    );
    assert.deepEqual(
      await web.evaluate(
        "(()=>{const n=document.querySelector('#selected-artwork img');return {alt:n.alt,caption:n.closest('figure').querySelector('figcaption').textContent,detail:!!n.closest('.detail'),scripts:n.closest('figure').querySelectorAll('script').length,active:document.activeElement.id,scroll:document.scrollingElement.scrollTop};})()",
      ),
      {
        alt: image.alt,
        caption: image.caption,
        detail: true,
        scripts: 0,
        active: "return-results",
        scroll,
      },
    );
    await push({ ...loaded, busy: true });
    assert.equal(
      await web.evaluate("document.activeElement.id"),
      "cancel-read",
    );
    await push(loaded);
    await web.until(
      "document.querySelector('#selected-artwork img')?.naturalWidth===1",
    );
    assert.equal(
      await web.evaluate("document.activeElement.id"),
      "return-results",
    );
    assert.equal(
      await web.evaluate("document.scrollingElement.scrollTop"),
      scroll,
    );
    assert.equal(
      await web.evaluate("document.querySelector('#selected-artwork img').src"),
      image.value,
    );
    await push({
      ...original,
      detail: undefined,
      image: undefined,
      items: [],
      error: "service_unavailable",
      guidance: {
        phase: "failed",
        message: "The Catalog is temporarily unavailable. Retry the read.",
        actions: ["restart", "help"],
      },
    });
    assert.deepEqual(
      await web.evaluate(
        "({active:document.activeElement.id,role:document.activeElement.getAttribute('role'),image:!!document.querySelector('#selected-artwork img')})",
      ),
      { active: "read-error", role: "alert", image: false },
    );
    assert.ok(
      (await web.accessibility()).some(
        (node) => !node.ignored && node.role?.value === "alert",
      ),
      "Read refusal reaches the actual accessibility tree",
    );
    await web.key("Tab");
    assert.equal(
      await web.evaluate(
        "document.getElementById('catalog').contains(document.activeElement) && document.activeElement.tagName==='BUTTON' && !document.activeElement.disabled",
      ),
      true,
      "Keyboard users can reach a recovery action after the alert",
    );
  } finally {
    await push(original);
  }
}
exports.run = async (api) => {
  await api.select("qualification");
  await api.authenticate("reader");
  await api.browse({});
  await vscode.commands.executeCommand("openAgenticCatalog.open");
  const web = await require("./webview.cjs").webview();
  try {
    await web.until(
      "document.querySelectorAll('.cards article').length===4 && !document.querySelector('#search-submit').disabled",
    );
    const accessibility = (await web.accessibility()).filter(
      (node) => !node.ignored,
    );
    assert.ok(
      accessibility.some(
        (node) =>
          node.role?.value === "textbox" && node.name?.value === "Search",
      ),
    );
    assert.ok(
      accessibility.some(
        (node) =>
          node.role?.value === "combobox" &&
          node.name?.value === "Resource kind",
      ),
    );
    assert.ok(
      accessibility.some(
        (node) =>
          node.role?.value === "button" &&
          node.name?.value.includes("Search / refresh"),
      ),
    );
    const status = accessibility.find((node) => node.role?.value === "status");
    assert.equal(
      status?.properties.find((property) => property.name === "live")?.value
        .value,
      "polite",
    );
    assert.equal(
      await web.evaluate(
        "document.querySelectorAll('[role=status]').length===1 && document.querySelector('[role=status]').textContent.length<=512",
      ),
      true,
    );
    assert.equal(
      accessibility.filter(
        (node) =>
          ["button", "textbox", "combobox"].includes(node.role?.value) &&
          !node.name?.value.trim(),
      ).length,
      0,
      "Exposed Catalog controls need accessible names",
    );
    await web.click("document.getElementById('text')");
    await web.key("Tab");
    assert.equal(
      await web.evaluate(
        "(()=>{const node=document.activeElement,style=getComputedStyle(node);return node.id==='kind' && style.outlineStyle!=='none' && parseFloat(style.outlineWidth)>=2;})()",
      ),
      true,
      "Actual keyboard navigation retains a visible focus outline",
    );
    assert.ok(
      (await web.accessibility()).some(
        (node) =>
          !node.ignored &&
          node.name?.value === "Resource kind" &&
          node.properties?.some(
            (property) =>
              property.name === "focused" && property.value.value === true,
          ),
      ),
    );
    assert.equal(
      await web.evaluate(
        "[...document.querySelectorAll('.title')].every(n=>n.querySelector('svg[aria-hidden=true]') && n.textContent.includes('W'.repeat(96)))",
      ),
      true,
    );
    assert.equal(
      await web.evaluate(
        "document.querySelectorAll('article svg script,article svg image,article svg use').length",
      ),
      0,
    );
    assert.equal(
      await web.evaluate(
        "[...document.querySelectorAll('article .description')].some(n=>n.textContent.includes('\\\\u000c'))",
      ),
      true,
    );
    await web.evaluate(
      "(()=>{const n=document.getElementById('text');n.value='draft caret';n.dispatchEvent(new Event('input',{bubbles:true}));n.focus();n.setSelectionRange(2,7);})()",
    );
    await web.evaluate(
      "window.__catalogPresentationProbe=(event)=>{if(event.data.type==='state')window.__catalogPresentation=event.data.value;};window.addEventListener('message',window.__catalogPresentationProbe)",
    );
    await api.explore({ text: "owned" });
    await web.until("document.querySelector('#facets') !== null");
    assert.deepEqual(
      await web.evaluate(
        "({value:document.getElementById('text').value,active:document.activeElement.id,start:document.getElementById('text').selectionStart,end:document.getElementById('text').selectionEnd})",
      ),
      { value: "draft caret", active: "text", start: 2, end: 7 },
    );
    // Establish actual iframe focus before native key dispatch.
    await web.click("document.getElementById('text')");
    // Keyboard selection focuses detail; Return restores the exact card.
    await web.evaluate("document.querySelector('#result-2').focus()");
    await web.key("Enter");
    await web
      .until("document.activeElement.id==='detail-heading'")
      .catch(async (error) => {
        throw new Error(
          `${error.message}; owned DOM ${JSON.stringify(await web.evaluate("({active:document.activeElement.id,detail:document.querySelector('.detail')?.textContent,error:document.querySelector('[role=alert]')?.textContent})"))}`,
        );
      });
    assert.equal(
      await web.evaluate("document.querySelector('#detail-documents').open"),
      true,
    );
    await presentation(web);
    await web.evaluate(
      "document.querySelector('#detail-source summary').focus()",
    );
    await web.key("Enter");
    await web.until("document.querySelector('#detail-source').open");
    await web.evaluate("document.querySelector('#return-results').focus()");
    await web.key("Enter");
    await web.until(
      "!document.querySelector('.detail') && document.activeElement.id==='result-2'",
    );
    // A hidden editor discards its renderer but retains one host-owned draft, never disk state.
    const hiddenFile = join(
      process.env.CATALOG_VSCODE_DEBUG_DIR,
      "hidden-draft.txt",
    );
    await writeFile(hiddenFile, "Owned temporary editor");
    const text = await vscode.workspace.openTextDocument(
      vscode.Uri.file(hiddenFile),
    );
    await vscode.window.showTextDocument(text);
    await vscode.commands.executeCommand("openAgenticCatalog.open");
    web.close();
    const restored = await require("./webview.cjs").webview();
    try {
      await restored.until(
        "document.getElementById('text')?.value==='draft caret'",
      );
      await restored.evaluate(
        "document.querySelector('#clear-filters').click()",
      );
      await restored.until("document.getElementById('text').value===''");
      assert.equal(api.browsing().facetQuery.text, "owned");
      await restored.evaluate(
        "document.querySelector('#advanced').open=true;document.querySelector('#explore').click()",
      );
      await restored.until(
        `document.querySelector('#facets button') && document.querySelector('#facets').textContent.includes('"text":""') && !document.querySelector('#explore').disabled`,
      );
      assert.equal(api.browsing().facetQuery.text, "");
      assert.match(
        await restored.evaluate(
          "document.querySelector('#facets').textContent",
        ),
        /authorized local releases/,
      );
      await restored.evaluate(
        "(()=>{const b=[...document.querySelectorAll('#facets button')].find(b=>b.textContent.startsWith('agent:'));b.click();})()",
      );
      await restored.until("document.getElementById('kind').value==='agent'");
      assert.equal(
        api.browsing().query.kind,
        "",
        "Facet selection must edit draft without fetching results",
      );
      await restored.evaluate("document.querySelector('form').requestSubmit()");
      await restored.until(
        "document.querySelectorAll('article').length===1 && !document.querySelector('#search-submit').disabled",
      );
      assert.equal(api.browsing().query.kind, "agent");
      assert.equal(
        await restored.evaluate(
          "(()=>{const n=document.createElement('input');n.value='line\\nbreak';return n.value;})()",
        ),
        "linebreak",
        "single-line inputs would silently change the exact filter",
      );
      await restored.evaluate(
        "[...document.querySelectorAll('article .tags button')].find(b=>b.textContent==='line\\nbreak').click()",
      );
      await restored.until("document.getElementById('tag').readOnly");
      assert.equal(
        await restored.evaluate("document.getElementById('tag').value"),
        JSON.stringify("line\nbreak"),
      );
      await restored.evaluate("document.querySelector('form').requestSubmit()");
      await restored.until(
        "!document.querySelector('#search-submit').disabled && !document.querySelector('#active-filters').textContent.includes('Draft changed.')",
      );
      assert.equal(api.browsing().query.tag, "line\nbreak");
      assert.equal(api.browsing().page.items.length, 1);
      await restored.evaluate("document.getElementById('clear-tag').click()");
      assert.equal(
        await restored.evaluate("document.getElementById('tag').readOnly"),
        false,
      );

      await restored.evaluate(
        "document.querySelector('#clear-filters').click()",
      );
      // Cancel peer disclosure before any request, keeping the user's visible edits.
      await restored.evaluate(
        "(()=>{const n=document.querySelector('#origin');n.value='live';n.dispatchEvent(new Event('change',{bubbles:true}));const text=document.querySelector('#text');text.value='unsubmitted owned';text.dispatchEvent(new Event('input',{bubbles:true}));document.querySelector('form').requestSubmit();})()",
      );
      const native = await require("./webview.cjs").webview(true);
      try {
        await native.until(
          "document.querySelector('.monaco-dialog-box')?.textContent.includes('administrator-approved')",
        );
        await native.evaluate(
          "[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(b=>b.textContent==='Cancel').click()",
        );
      } finally {
        native.close();
      }
      assert.equal(
        await restored.evaluate("document.querySelector('#text').value"),
        "unsubmitted owned",
      );
      assert.equal(
        api.browsing().query.kind,
        "agent",
        "Declined peer request must not replace submitted query",
      );
      await restored.evaluate(
        "document.querySelector('#clear-filters').click()",
      );
      await api.browse({});
      await restored.until("document.querySelectorAll('article').length===4");
      // Real workbench zoom, theme changes and renderer font tokens are separate controls.
      await vscode.commands.executeCommand("workbench.action.closePanel");
      await vscode.commands.executeCommand(
        "workbench.action.closeAuxiliaryBar",
      );
      await vscode.commands.executeCommand(
        "workbench.action.editorLayoutTwoColumns",
      );
      assert.equal(
        vscode.window.tabGroups.all.length,
        2,
        "Qualify a real split editor layout",
      );
      await vscode.workspace
        .getConfiguration("workbench")
        .update(
          "productIconTheme",
          "Default",
          vscode.ConfigurationTarget.Global,
        );
      const originalRatio = await restored.evaluate("window.devicePixelRatio");
      await vscode.workspace
        .getConfiguration("window")
        .update("zoomLevel", 4, vscode.ConfigurationTarget.Global);
      await restored.until(`window.devicePixelRatio >= ${originalRatio * 2}`);
      for (const [theme, cls] of [
        ["Default Light Modern", "vscode-light"],
        ["Default Dark Modern", "vscode-dark"],
        ["Default High Contrast", "vscode-high-contrast"],
      ]) {
        await vscode.workspace
          .getConfiguration("workbench")
          .update("colorTheme", theme, vscode.ConfigurationTarget.Global);
        await restored.until(
          `document.body.classList.contains(${JSON.stringify(cls)})`,
        );
        await restored.evaluate(
          "document.getElementById('catalog').style.maxWidth='328px';document.body.style.boxSizing='border-box';document.documentElement.style.setProperty('--vscode-font-size','28px')",
        );
        assert.equal(
          await restored.evaluate(
            "getComputedStyle(document.getElementById('text')).fontSize",
          ),
          "28px",
        );
        assert.equal(
          await restored.evaluate(
            "document.getElementById('catalog').scrollWidth <= document.getElementById('catalog').clientWidth+1 && document.documentElement.scrollWidth<=innerWidth+1",
          ),
          true,
          theme,
        );
        await restored.evaluate("document.querySelector('#result-0').focus()");
        await restored.key("Enter");
        await restored.until("document.activeElement.id==='detail-heading'");
        await restored.evaluate(
          "document.querySelector('#all-documents').open=true;document.querySelector('#detail-source').open=true",
        );
        assert.equal(
          await restored.evaluate(
            "document.getElementById('catalog').scrollWidth <= document.getElementById('catalog').clientWidth+1",
          ),
          true,
          `${theme} detail`,
        );
        if (cls === "vscode-high-contrast") {
          const screenshot = await require("./webview.cjs").webview(true);
          try {
            await screenshot.screenshot(
              join(
                process.env.CATALOG_VSCODE_SCREENSHOTS,
                "discovery-narrow.png",
              ),
            );
          } finally {
            screenshot.close();
          }
        }
        await restored.evaluate(
          "document.querySelector('#return-results').click()",
        );
        await restored.until("!document.querySelector('.detail')");
      }
      await vscode.workspace
        .getConfiguration("window")
        .update("zoomLevel", 0, vscode.ConfigurationTarget.Global);
      await restored.evaluate(
        "document.getElementById('catalog').style.maxWidth='';document.body.style.boxSizing='';document.documentElement.style.removeProperty('--vscode-font-size')",
      );
      await vscode.commands.executeCommand(
        "workbench.action.editorLayoutSingle",
      );
    } finally {
      restored.close();
    }
    await api.browse({});
    await vscode.commands.executeCommand("vscode.moveViews", {
      viewIds: ["openAgenticCatalog.catalog", "openAgenticCatalog.tools"],
      destinationId: "workbench.view.explorer",
    });
    await vscode.commands.executeCommand("openAgenticCatalog.catalog.focus");
    const moved = await require("./webview.cjs").webview(true);
    try {
      await moved.until(
        "[...document.querySelectorAll('[role=tree]')].some(n=>n.getAttribute('aria-label')?.includes('Catalog'))",
      );
      assert.equal(
        await moved.evaluate(
          "['robot','server','book','extensions'].every(icon=>document.querySelector('.codicon-'+icon))",
        ),
        true,
      );
      await vscode.workspace
        .getConfiguration("workbench")
        .update(
          "productIconTheme",
          "oac-owned-icons",
          vscode.ConfigurationTarget.Global,
        );
      await moved.until(
        "getComputedStyle(document.querySelector('.codicon-robot'),'::before').fontFamily.includes('owned') && [...document.fonts].some(font=>font.family.includes('owned') && font.status==='loaded')",
      );
      assert.equal(
        await moved.evaluate(
          "['robot','server','book','extensions'].every(icon=>{const node=document.querySelector('.codicon-'+icon);return node && getComputedStyle(node,'::before').fontFamily.includes('owned') && node.getBoundingClientRect().width>0;})",
        ),
        true,
        "Actual local product-icon theme replaces native glyphs without losing resource labels",
      );
      assert.ok(
        (await moved.accessibility()).some(
          (node) =>
            !node.ignored &&
            node.role?.value === "tree" &&
            node.name?.value.includes("Catalog"),
        ),
      );
      await vscode.workspace
        .getConfiguration("workbench")
        .update(
          "productIconTheme",
          "Default",
          vscode.ConfigurationTarget.Global,
        );
    } finally {
      moved.close();
    }
    // Native commands share page replacement with the cards, including moved-view identity.
    await api.browse({ limit: 1 });
    const pagingDraft = await require("./webview.cjs").webview();
    await pagingDraft.until(
      "document.querySelectorAll('.cards article').length===1 && document.getElementById('text') && !document.getElementById('search-submit').disabled",
    );
    await pagingDraft.evaluate(
      "(()=>{const n=document.getElementById('text');n.value='paging draft';n.dispatchEvent(new Event('input',{bubbles:true}));})()",
    );
    const first = api.browsing().page.items[0].key;
    await vscode.commands.executeCommand("openAgenticCatalog.nextPage");
    assert.notEqual(api.browsing().page.items[0].key, first);
    assert.equal(api.browsing().page.items.length, 1);
    await vscode.window.showTextDocument(text);
    await vscode.commands.executeCommand("openAgenticCatalog.open");
    pagingDraft.close();
    const pagingRestored = await require("./webview.cjs").webview();
    try {
      await pagingRestored.until(
        "document.getElementById('text')?.value==='paging draft'",
      );
    } finally {
      pagingRestored.close();
    }

    await vscode.commands.executeCommand("openAgenticCatalog.refresh");
    assert.equal(api.browsing().page.items[0].key, first);
    await vscode.commands.executeCommand("openAgenticCatalog.detail", {
      item: api.browsing().page.items[0],
    });
    assert.equal(api.browsing().detail.item.key, first);
    await vscode.commands.executeCommand("vscode.moveViews", {
      viewIds: ["openAgenticCatalog.catalog", "openAgenticCatalog.tools"],
      destinationId: "workbench.view.extension.openAgenticCatalog",
    });
  } finally {
    web.close();
    await vscode.commands.executeCommand("workbench.action.closeAllEditors");
  }
};

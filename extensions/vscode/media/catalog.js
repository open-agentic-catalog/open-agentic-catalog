/* Remote strings are text nodes. Only these code-owned SVG paths enter the DOM. */
(() => {
  const vscode = acquireVsCodeApi();
  const root = document.getElementById("catalog");
  let state,
    draft,
    scope,
    submitted,
    focus,
    selectedResult,
    resultsPosition = 0;
  const opened = new Map();
  const kinds = {
    agent: "Agent",
    mcp_server: "MCP Server",
    skill: "Skill",
    plugin: "Plugin",
    unknown: "Resource",
  };
  // Small local line icons mirror the native robot/server/book/extensions concepts.
  const paths = {
    agent: ["M8 4V2h8v2M5 5h14v14H5zM2 9h3m14 0h3M9 10h.01M15 10h.01M9 15h6"],
    mcp_server: ["M3 3h18v7H3zM3 14h18v7H3zM7 7h.01M7 18h.01M11 7h6M11 18h6"],
    skill: [
      "M12 5C8 2 4 3 2 4v16c3-2 7-2 10 0 3-2 7-2 10 0V4c-3-1-7-2-10 1zm0 0v15",
    ],
    plugin: ["M3 3h7v7H3zM14 3h7v7h-7zM3 14h7v7H3zM14 14h7v7h-7z"],
    unknown: ["M4 4h16v16H4zM8 8h8M8 12h8M8 16h4"],
    search: ["M10 3a7 7 0 1 0 0 14 7 7 0 0 0 0-14m5 12 6 6"],
    inspect: ["M5 2h10l4 4v16H5zM14 2v5h5M8 11h8M8 15h8"],
    next: ["M4 12h16m-6-6 6 6-6 6"],
    back: ["M20 12H4m6-6-6 6 6 6"],
    cancel: ["M5 5l14 14M19 5 5 19"],
    export: ["M12 2v13m-5-5 5 5 5-5M4 17v5h16v-5"],
    account: ["M12 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8M4 22v-3a8 6 0 0 1 16 0v3"],
  };
  function icon(name, parent) {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("aria-hidden", "true");
    svg.setAttribute("focusable", "false");
    svg.classList.add("icon");
    for (const d of paths[name] ?? paths.unknown) {
      const p = document.createElementNS(svg.namespaceURI, "path");
      p.setAttribute("d", d);
      svg.append(p);
    }
    parent.prepend(svg);
  }
  // Controls other than layout whitespace are visibly escaped, never executed.
  function display(value) {
    return String(value).replace(/\p{Cc}/gu, (c) => {
      const n = c.codePointAt(0);
      return [9, 10, 13].includes(n)
        ? c
        : `\\u${n.toString(16).padStart(4, "0")}`;
    });
  }
  function el(tag, text, parent = root) {
    const n = document.createElement(tag);
    if (text !== undefined) n.textContent = display(text);
    parent.append(n);
    return n;
  }
  function send(type, fields = {}) {
    vscode.postMessage({ type, revision: state.revision, ...fields });
  }
  function button(
    label,
    type,
    fields,
    parent = root,
    disabled = false,
    treatment = "secondary",
    id,
  ) {
    const b = el("button", label, parent);
    b.type = "button";
    b.className = treatment;
    b.id =
      id ??
      `action-${type}-${fields?.key ?? fields?.index ?? fields?.action ?? ""}`;
    b.disabled =
      disabled ||
      (type !== "help" &&
        type !== "recover" &&
        type !== "cancel" &&
        (state.busy || !state.canBrowse));
    b.title = display(label);
    b.addEventListener("click", () => send(type, fields));
    if (paths[type]) icon(type, b);
    return b;
  }
  function disclosure(label, id, parent, initial = false) {
    const d = el("details", undefined, parent);
    d.id = id;
    d.open = opened.get(id) ?? initial;
    el("summary", label, d);
    d.addEventListener("toggle", () => {
      opened.set(id, d.open);
    });
    return d;
  }
  function saveDraft() {
    if (
      Object.entries({
        text: 500,
        tag: 80,
        namespace: 36,
        publisher: 256,
        license: 256,
        category: 80,
      }).some(
        ([k, max]) =>
          [...String(draft[k])].length > 2 * max ||
          String(draft[k]).includes("\0") ||
          /[\uD800-\uDFFF]/u.test(String(draft[k])),
      )
    )
      return;
    send("draft", { query: draft, scope: state.scope });
  }
  function field(label, name, options, form) {
    const wrapper = el("div", undefined, form),
      labelNode = el("label", label, wrapper),
      input = el(options ? "select" : "input", undefined, wrapper);
    labelNode.htmlFor = name;
    input.id = name;
    input.name = name;
    if (options)
      for (const [value, title] of options) {
        const o = el("option", title, input);
        o.value = value;
      }
    else {
      input.type = "text";
      // UTF-16 editor bound admits every contract-valid supplementary character.
      input.maxLength =
        2 *
        ({
          text: 500,
          tag: 80,
          publisher: 256,
          license: 256,
          category: 80,
          namespace: 36,
        }[name] ?? 500);
    }
    const exact = !options && /\p{Cc}/u.test(String(draft[name]));
    input.value = exact ? JSON.stringify(draft[name]) : String(draft[name]);
    if (exact) {
      input.readOnly = true;
      const hint = el(
        "span",
        "Exact value shown as JSON text. Search uses the original value. Clear it to type a replacement.",
        wrapper,
      );
      hint.id = `exact-${name}`;
      hint.className = "exact-value-hint";
      input.setAttribute("aria-describedby", hint.id);
      const clear = el("button", `Clear ${label}`, wrapper);
      clear.type = "button";
      clear.className = "secondary";
      clear.id = `clear-${name}`;
      clear.disabled = !state.canBrowse;
      clear.addEventListener("click", () => {
        draft[name] = "";
        saveDraft();
        draw(state);
        document.getElementById(name)?.focus();
      });
    }
    input.disabled = !state.canBrowse;
    input.addEventListener(options ? "change" : "input", () => {
      if (input.readOnly) return;
      const multi = {
        origin: "origins",
        kind: "kinds",
        tag: "tags",
        category: "categories",
      }[name];
      if (multi) delete draft[multi];
      draft[name] = name === "limit" ? Number(input.value) : input.value;
      saveDraft();
      constraints();
      activeFilters();
    });
    return input;
  }
  function constraints() {
    for (const n of root.querySelectorAll(".field-error, .field-hint"))
      n.remove();
    let invalid = false;
    function issue(name, text) {
      const input = document.getElementById(name);
      if (!input) return;
      const existing = document.getElementById(`error-${name}`);
      if (existing) {
        existing.textContent += ` ${text}`;
        invalid = true;
        return;
      }
      const n = el("span", text, input.parentElement);
      n.className = "field-error";
      n.id = `error-${name}`;
      input.setAttribute("aria-describedby", n.id);
      input.setAttribute("aria-invalid", "true");
      invalid = true;
    }
    for (const n of root.querySelectorAll("[aria-invalid]")) {
      n.removeAttribute("aria-invalid");
      n.removeAttribute("aria-describedby");
    }
    for (const [name, max] of Object.entries({
      text: 500,
      tag: 80,
      publisher: 256,
      license: 256,
      category: 80,
      namespace: 36,
    })) {
      const v = String(draft[name]);
      if (
        [...v].length > max ||
        v.includes("\0") ||
        /[\uD800-\uDFFF]/u.test(v) ||
        (["text", "publisher"].includes(name) &&
          new TextEncoder().encode(v).length > max)
      )
        issue(
          name,
          `Use at most ${max} ${["text", "publisher"].includes(name) ? "UTF-8 bytes" : "characters"}; NUL and malformed Unicode are not allowed.`,
        );
    }
    if (
      draft.namespace &&
      !/^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/iu.test(draft.namespace)
    )
      issue("namespace", "Enter an exact namespace UUID or clear this filter.");
    const local =
        (draft.origins ?? [draft.origin]).length === 1 &&
        (draft.origins ?? [draft.origin])[0] === "local",
      published = state.mode === "published",
      list = published && !draft.text.trim();
    for (const name of ["namespace"]) {
      const n = document.getElementById(name);
      if (n) n.disabled = !state.canBrowse || !local;
    }
    if (!local && draft.namespace)
      issue(
        "origin",
        "The local namespace filter is retained. Choose Local or Clear filters before searching this origin.",
      );
    if (
      list &&
      (draft.tag ||
        draft.license ||
        draft.category ||
        draft.federation !== "none")
    )
      issue(
        "text",
        "published_search_needs_text: Enter search text for tag, license, category or federated search, or Clear filters to use List.",
      );
    if (!local && document.getElementById("origin")) {
      const hint = el(
        "span",
        "facets_are_local: Select Local to explore facet counts.",
        document.getElementById("origin").parentElement,
      );
      hint.className = "field-hint";
    }
    for (const name of ["license", "category"])
      if (draft[name] && !draft[name].trim())
        issue(name, "Enter a value containing text, or clear this filter.");
    const submit = document.querySelector("button[type=submit]");
    if (submit) submit.disabled = state.busy || !state.canBrowse || invalid;
    const explore = document.getElementById("explore");
    if (explore) {
      explore.disabled = state.busy || !state.canBrowse || !local;
      explore.title = !local
        ? "facets_are_local: Select Local for facet counts."
        : "Explore uses the visible filter draft. Counts cover this authorized local population.";
    }
  }
  function activeFilters() {
    const n = document.getElementById("active-filters");
    if (!n) return;
    n.replaceChildren();
    const values = Object.entries(draft).filter(
      ([k, v]) =>
        v &&
        (!Array.isArray(v) || v.length) &&
        !["limit", "federation"].includes(k) &&
        !(k === "origin" && v === "local"),
    );
    if (draft.federation === "auto")
      values.push(["federation", "approved peers"]);
    el(
      "span",
      values.length
        ? `Active draft: ${values.map(([k, v]) => `${k}: ${Array.isArray(v) ? JSON.stringify(v) : v}`).join(" · ")}`
        : "No filters selected",
      n,
    );
    if (JSON.stringify(draft) !== JSON.stringify(state.query))
      el(
        "span",
        " Draft changed. Displayed results and Next page still use the last submitted filters.",
        n,
      );
  }
  function capture() {
    const n = document.activeElement;
    if (n?.id && n.id !== "cancel-read" && root.contains(n))
      focus = { id: n.id, start: n.selectionStart, end: n.selectionEnd };
    return document.scrollingElement.scrollTop;
  }
  function draw(previous) {
    const scroll = capture(),
      oldDetail = previous?.detail?.item.key,
      newDetail = state.detail?.item.key;
    root.replaceChildren();
    el("h1", "Catalog");
    button("Search and filter help", "help", undefined);
    if (state.account) {
      const account = el(
        "p",
        `${state.account.label} · ${state.account.mode === "control" ? "Control API" : "Published ARD"} · ${state.account.state.replaceAll("_", " ")}`,
      );
      account.id = "catalog-account-state";
      icon(
        "account",
        button("Account and connection", "recover", { action: "account" }),
      );
    }
    const recovery = el("section");
    recovery.className = "guidance";
    recovery.setAttribute("aria-label", "Connection and read status");
    const status = el(
      "p",
      state.busy ? "Loading…" : (state.summary ?? state.guidance.message),
      recovery,
    );
    status.setAttribute("role", "status");
    if (state.error) {
      const n = el(
        "p",
        `Catalog: ${state.error}. ${state.guidance.message}`,
        recovery,
      );
      n.setAttribute("role", "alert");
      n.id = "read-error";
      n.tabIndex = -1;
    } else if (state.summary && state.guidance.message)
      el("p", state.guidance.message, recovery);
    const titles = {
      addConnection: "Add connection",
      connect: "Select connection",
      signIn: "Sign in",
      testConnection: "Test connection",
      settings: "Connection settings",
      trust: "Review workspace trust",
      restart:
        state.guidance.phase === "not_loaded"
          ? "Load first page"
          : "Start new search",
      help: "Sign-in and trust help",
    };
    for (const a of state.guidance.actions)
      button(
        titles[a],
        "recover",
        { action: a },
        recovery,
        false,
        "secondary",
        `recover-${a}`,
      );
    if (!state.guidance.actions.includes("help"))
      button("Sign-in and trust help", "recover", { action: "help" }, recovery);
    if (state.busy)
      button(
        "Cancel read",
        "cancel",
        {},
        recovery,
        false,
        "secondary",
        "cancel-read",
      );
    if (state.mode === "control") {
      for (const [action, title] of [
        ["combinedFilters", "Combine filters"],
        ["myRatings", "My ratings"],
        ["myFeedback", "My private feedback"],
        ["recoverOpinion", "Recover opinion command"],
      ])
        button(
          title,
          "recover",
          { action },
          recovery,
          state.busy,
          "secondary",
          `catalog-${action}`,
        );
    }
    el("p", "Metadata does not grant trust or execution permission.");
    const form = el("form");
    form.id = "search-form";
    const controls = el("div", undefined, form);
    controls.className = "filters";
    field("Search", "text", undefined, controls);
    field(
      "Resource kind",
      "kind",
      [
        ["", "All kinds"],
        ...Object.entries(kinds).filter(([k]) => k !== "unknown"),
      ],
      controls,
    );
    const advanced = disclosure("Advanced filters", "advanced", form);
    const fields = el("div", undefined, advanced);
    fields.className = "filters";
    field("Tag (exact)", "tag", undefined, fields);
    field("Publisher", "publisher", undefined, fields);
    field("License (exact)", "license", undefined, fields);
    field("Category", "category", undefined, fields);
    if (state.mode !== "published") {
      field(
        "Origin",
        "origin",
        [
          ["local", "Local governed releases"],
          ["indexed", "Indexed external metadata"],
          ["live", "Live external search"],
        ],
        fields,
      );
      const ns = field("Local namespace UUID", "namespace", undefined, fields);
      ns.setAttribute("list", "namespace-labels");
      const labels = el("datalist", undefined, fields);
      labels.id = "namespace-labels";
      const known = new Map(
        state.items
          .filter((i) => i.namespaceId && i.namespace)
          .map((i) => [i.namespaceId, i.namespace]),
      );
      for (const [id, name] of known) {
        const o = el("option", name, labels);
        o.value = id;
      }
      field(
        "Visibility",
        "visibility",
        [
          ["", "All visible"],
          ["public", "Public"],
          ["tenant", "Tenant"],
          ["private", "Private"],
        ],
        fields,
      );
      el(
        "p",
        "Namespace names come from these authorized results. Exact UUID entry is always available. Namespace and facet counts apply to local releases. Live search sends the query to approved peers.",
        advanced,
      );
    } else {
      field(
        "Federation",
        "federation",
        [
          ["none", "Local publication only"],
          ["auto", "Approved live sources (confirmation required)"],
        ],
        fields,
      );
      el(
        "p",
        "Empty searches use ARD List: kind and publisher only. Enter search text for tags, license or categories. Explore counts the local publication, never global peer totals.",
        advanced,
      );
    }
    field(
      "Page size",
      "limit",
      [
        ...([10, 20, 50, 100].includes(draft.limit)
          ? []
          : [[String(draft.limit), String(draft.limit)]]),
        ["10", "10"],
        ["20", "20"],
        ["50", "50"],
        ["100", "100"],
      ],
      fields,
    );
    const active = el("p", undefined, form);
    active.id = "active-filters";
    const submit = el("button", "Search / refresh", form);
    submit.type = "submit";
    submit.id = "search-submit";
    submit.className = "primary";
    icon("search", submit);
    form.addEventListener("submit", (e) => {
      e.preventDefault();
      send("search", { query: draft });
    });
    // Read controls at submission as well: native accessibility/programmatic changes may not emit input.
    const collect = () => {
      for (const n of form.elements)
        if (n.name && !n.readOnly)
          draft[n.name] = n.name === "limit" ? Number(n.value) : n.value;
      saveDraft();
    };
    form.addEventListener("submit", collect, { capture: true });
    const clear = el("button", "Clear filters", form);
    clear.type = "button";
    clear.id = "clear-filters";
    clear.className = "secondary";
    clear.disabled = !state.canBrowse;
    clear.addEventListener("click", () => {
      draft = {
        ...state.query,
        text: "",
        kind: "",
        tag: "",
        namespace: "",
        origin: "local",
        publisher: "",
        license: "",
        category: "",
        visibility: "",
        federation: "none",
      };
      for (const key of ["origins", "kinds", "tags", "categories"])
        delete draft[key];
      saveDraft();
      draw(state);
    });
    const explore = button(
      "Explore visible filters",
      "explore",
      {},
      form,
      false,
      "secondary",
      "explore",
    );
    explore.addEventListener(
      "click",
      (e) => {
        e.stopImmediatePropagation();
        collect();
        send("explore", { query: draft });
      },
      { capture: true },
    );
    if (state.mode === "published")
      button("Inspect manifest", "manifest", {}, form);
    if (state.facets) {
      const section = disclosure(
        "Local facets for the explored filters",
        "facets",
        root,
        true,
      );
      el(
        "p",
        `Counts cover authorized ${state.mode === "published" ? "local published entries" : "local releases"} matching the explored filters: ${JSON.stringify(state.facetQuery)}. They are not global peer totals. Selecting a bucket edits the draft; Search applies it.`,
        section,
      );
      if (state.mode === "published")
        el(
          "p",
          "Type counts describe exact media types. Selecting a type applies its resource kind, which can include several supported formats.",
          section,
        );
      for (const f of state.facets) {
        el("h3", f.field, section);
        for (const b of f.buckets) {
          const field = b.filter?.field,
            value = b.filter?.value;
          const name =
            f.field === "namespace"
              ? state.items.find((i) => i.namespaceId === b.value)?.namespace
              : undefined;
          const control = el(
            field ? "button" : "span",
            `${name && name !== b.value ? `${name} (${b.value})` : b.value}: ${b.count}`,
            section,
          );
          if (!field)
            control.title =
              "No supported filter matches this bucket. Its value and count remain inspectable.";
          if (field) {
            control.type = "button";
            control.className = "tag";
            control.disabled = state.busy || !state.canBrowse;
            control.addEventListener("click", () => {
              draft[field] = value;
              const multi = {
                kind: "kinds",
                tag: "tags",
                category: "categories",
                origin: "origins",
              }[field];
              if (multi) delete draft[multi];
              saveDraft();
              opened.set("advanced", true);
              draw(state);
            });
          }
        }
        if (f.other) el("p", `${f.other} other bucket occurrences`, section);
      }
    }
    if (state.detail) {
      const d = state.detail,
        section = el("section");
      section.className = "detail";
      button(
        "Return to results",
        "back",
        {},
        section,
        false,
        "navigation",
        "return-results",
      );
      const heading = el("h2", d.item.name, section);
      heading.id = "detail-heading";
      heading.tabIndex = -1;
      icon(d.item.kind, heading);
      metadata(d.item, section);
      el("p", d.item.description, section).className = "description";
      el("code", d.identifier, section);
      button("Copy exact reference", "copy", {}, section);
      if (d.bookmark) button("Save reference", "bookmark", {}, section);
      if (state.mode === "control" && d.item.origin === "local") {
        button("Rate this release", "recover", { action: "rate" }, section);
        button(
          "Private item feedback",
          "recover",
          { action: "feedback" },
          section,
        );
      }
      const docs = disclosure(
        "Documents and package",
        "detail-documents",
        section,
        true,
      );
      el(
        "p",
        {
          agent:
            "Inspect the Agent Card before a conversation. Target consent and credentials are separate from Catalog login.",
          mcp_server:
            "Inspect the MCP document before registration. Native trust and tool approvals still apply.",
          skill:
            "Review the complete immutable Skill package before installation in a trusted workspace.",
          plugin:
            "Review the manifest and components. Installation is disabled; activation requires separate consent.",
          unknown: "Inspect the declared resource metadata.",
        }[d.item.kind],
        docs,
      );
      if (!d.documents.length)
        el(
          "p",
          "Complete native evidence is unavailable. Inspect declared references; no package or runtime is inferred.",
          docs,
        );
      const primary = d.documents.findIndex((doc) => doc.readable);
      if (primary >= 0)
        button(
          `Inspect ${kinds[d.item.kind]} metadata`,
          "inspect",
          { index: primary },
          docs,
          false,
          "primary",
        );
      for (const p of d.packages ?? []) {
        const reason = !state.environment?.trusted
          ? "A trusted workspace is required for package management."
          : !state.environment.filesystem
            ? "Open a filesystem workspace before package management."
            : !["linux", "darwin", "win32"].includes(state.environment.platform)
              ? "Package management requires Linux, macOS or Windows."
              : "";
        if (reason) el("p", reason, docs);
        button(p.label, p.type, { index: p.index }, docs, !!reason);
        button(
          "Compare installed inventory",
          "compare",
          { index: p.index },
          docs,
          !!reason,
        );
        if (d.item.kind === "plugin")
          button(
            "Review and activate installed Plugin…",
            "activatePlugin",
            { index: p.index },
            docs,
            !!reason,
          );
      }
      if (["skill", "plugin"].includes(d.item.kind) && !d.packages?.length)
        el(
          "p",
          "Install and compare are unavailable: this result has no complete locally inspected package. Retained documents and declared references remain available for inspection.",
          docs,
        );
      const evidence = disclosure(
        `All documents (${d.documents.length})`,
        "all-documents",
        docs,
      );
      d.documents.forEach((doc, index) => {
        const row = el("div", undefined, evidence);
        row.className = "document-row";
        el("h4", doc.label, row);
        if (doc.action === "a2a")
          button(
            "Talk to Agent…",
            "a2a",
            { index },
            row,
            !state.environment?.trusted || state.environment?.virtual,
          );
        if (doc.action === "mcp")
          button(
            "Use MCP server…",
            "mcp",
            { index },
            row,
            !state.environment?.trusted || state.environment?.virtual,
          );
        if (doc.reason) el("p", doc.reason, row);
        if (doc.notice) el("p", doc.notice, row);
        if (doc.action && !state.environment?.trusted)
          el(
            "p",
            "Trust this workspace before contacting a resource target. Inspection remains available.",
            row,
          );
        if (doc.action && state.environment?.virtual)
          el(
            "p",
            "Virtual workspace folders do not support native resource consumption. Inspection remains available; use a trusted filesystem workspace or an empty window.",
            row,
          );
        if (doc.readable) {
          button("Inspect document", "inspect", { index }, row);
          button(
            doc.exact ? "Export exact bytes" : "Export metadata JSON",
            "export",
            { index },
            row,
          );
        } else el("p", "External document: use its declared link below.", row);
      });
      const artwork = el("div", undefined, section);
      artwork.id = "selected-artwork";
      for (const a of d.artwork)
        button(a.label, "image", { index: a.index }, artwork);
      if (state.image) {
        const f = el("figure", undefined, artwork),
          img = el("img", undefined, f);
        img.src = state.image.value;
        img.alt = display(state.image.alt);
        img.width = 640;
        img.height = 360;
        el("figcaption", state.image.caption, f);
      }
      const source = disclosure(
        "Source and declarations",
        "detail-source",
        section,
      );
      for (const f of d.facts) el("p", `${f.label}: ${f.value}`, source);
      d.references.forEach((r, index) => {
        const row = el("p", `${r.label}: ${r.value}`, source);
        if (r.open) button("Open link…", "open", { index }, row);
      });
    }
    const cards = el("section");
    cards.className = "cards";
    cards.setAttribute("aria-label", "Catalog results");
    if (state.guidance.phase === "empty" && !state.busy)
      el(
        "p",
        "No resources on this page. A continuation can still be available.",
        cards,
      );
    for (const [index, item] of state.items.entries()) {
      const card = el("article", undefined, cards);
      const title = button(
        item.name,
        "select",
        { key: item.key },
        card,
        false,
        "title navigation",
        `result-${index}`,
      );
      icon(item.kind, title);
      title.addEventListener("click", () => {
        selectedResult = title.id;
        resultsPosition = document.scrollingElement.scrollTop;
      });
      metadata(item, card);
      el("p", item.description, card).className = "description";
      const tags = el("div", undefined, card);
      tags.className = "tags";
      for (const [n, tag] of item.tags.entries()) {
        const b = el("button", tag, tags);
        b.type = "button";
        b.className = "tag";
        b.id = `tag-${index}-${n}`;
        b.disabled = state.busy || !state.canBrowse;
        b.addEventListener("click", () => {
          draft.tag = tag;
          delete draft.tags;
          saveDraft();
          opened.set("advanced", true);
          draw(state);
        });
      }
    }
    button(
      state.error && state.next ? "Retry next page" : "Next page",
      "next",
      {},
      root,
      !state.next,
      "secondary",
      "next-page",
    );
    if (state.bookmarks.length) {
      const s = disclosure("Saved references", "saved-references", root);
      for (const b of state.bookmarks) {
        const row = el("div", undefined, s);
        button(b.label, "reopen", { index: b.index }, row);
        button(
          "Remove reference",
          "removeBookmark",
          { index: b.index },
          row,
          false,
          "destructive",
        );
      }
    }
    constraints();
    activeFilters();
    if (newDetail && newDetail !== oldDetail && !state.busy) {
      resultsPosition = scroll;
      document.getElementById("detail-heading").focus();
    } else if (oldDetail && !newDetail && !state.busy && !state.error) {
      document
        .getElementById(selectedResult ?? "result-0")
        ?.focus({ preventScroll: true });
      document.scrollingElement.scrollTop = resultsPosition;
    } else {
      const target = document.getElementById(focus?.id);
      if (target && !target.disabled) {
        target.focus({ preventScroll: true });
        if (typeof focus.start === "number" && target.setSelectionRange)
          target.setSelectionRange(focus.start, focus.end);
      } else if (state.busy)
        document.getElementById("cancel-read")?.focus({ preventScroll: true });
      else if (focus)
        (
          document.getElementById("read-error") ??
          root.querySelector(".guidance button:not(:disabled)") ??
          document.getElementById("search-submit")
        )?.focus({ preventScroll: true });
      document.scrollingElement.scrollTop = scroll;
    }
  }
  function metadata(item, parent) {
    el(
      "p",
      `${kinds[item.kind]} · ${item.origin} · ${item.versionPresent ? `Version: ${item.version === "" ? "(empty)" : item.version}` : "Version not supplied"}`,
      parent,
    ).className = "metadata";
    for (const k of [
      "publisher",
      "namespace",
      "license",
      "visibility",
      "ratings",
    ])
      if (item[k]) el("p", `${k}: ${item[k]}`, parent);
    if (item.sourceCount !== undefined)
      el(
        "p",
        `${item.sourceCount} indexed sources · ${item.conflict ? "Conflicting declarations; inspect evidence" : "No indexed conflict reported"}${item.sameSourceConflict ? "; conflicting entries from the same source" : ""}`,
        parent,
      );
  }
  window.addEventListener("message", ({ data: m }) => {
    if (m.type === "state") {
      const previous = state;
      state = m.value;
      if (scope !== state.scope || submitted !== JSON.stringify(state.query)) {
        if (scope !== state.scope) {
          focus = undefined;
          selectedResult = undefined;
          resultsPosition = 0;
          opened.clear();
        }
        draft = { ...(state.draft ?? state.query) };
        scope = state.scope;
        submitted = JSON.stringify(state.query);
      }
      draw(previous);
    } else if (m.type === "account" && state) {
      state.account = m.value;
      const account = document.getElementById("catalog-account-state");
      if (account && state.account)
        account.textContent = `${state.account.label} · ${state.account.mode === "control" ? "Control API" : "Published ARD"} · ${state.account.state.replaceAll("_", " ")}`;
    }
  });
  vscode.postMessage({ type: "ready" });
})();

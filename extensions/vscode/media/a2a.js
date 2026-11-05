(() => {
  const api = acquireVsCodeApi();
  const root = document.getElementById("catalog");
  root.replaceChildren();
  let model;
  let initialized = false;
  const send = (type, extra = {}) =>
    api.postMessage({ type, revision: model.revision, ...extra });
  const node = (tag, text, parent = root) => {
    const element = document.createElement(tag);
    element.textContent = text;
    parent.append(element);
    return element;
  };
  const title = node("h1", "Agent conversation");
  const destination = node("p", "");
  const status = node("p", "Preparing conversation.");
  status.setAttribute("role", "status");
  const task = node("p", "");
  const request = node("p", "");
  node(
    "p",
    "Only your prompt is sent. Drafts and transcripts stay in this conversation's memory. The prompt stays after sending; edit or clear it yourself. Closing or replacing this view does not cancel a remote task.",
  );
  const transcript = node("section", "");
  transcript.className = "conversation-transcript";
  transcript.setAttribute("aria-label", "Conversation transcript");
  transcript.setAttribute("role", "region");
  transcript.tabIndex = 0;
  const entries = new Map();
  const label = node("label", "Prompt");
  label.htmlFor = "prompt";
  const input = node("textarea", "");
  input.id = "prompt";
  input.readOnly = true;
  input.maxLength = 16384;
  input.setAttribute("aria-describedby", "prompt-budget prompt-error");
  const budget = node("p", "");
  budget.id = "prompt-budget";
  const error = node("p", "");
  error.id = "prompt-error";
  error.className = "field-error";
  const streaming = node("label", "");
  streaming.className = "conversation-stream";
  streaming.htmlFor = "stream-reply";
  const checkbox = node("input", "", streaming);
  checkbox.id = "stream-reply";
  checkbox.type = "checkbox";
  checkbox.disabled = true;
  node("span", "Stream reply", streaming);
  const streamHint = node("p", "");
  streamHint.id = "stream-hint";
  checkbox.setAttribute("aria-describedby", streamHint.id);
  const actions = node("div", "");
  actions.className = "actions";
  const button = (label, type, extra) => {
    const b = node("button", label, actions);
    b.type = "button";
    b.disabled = true;
    b.className =
      type === "send"
        ? "primary"
        : type === "cancel"
          ? "destructive"
          : "secondary";
    b.addEventListener("click", () => send(type, extra?.()));
    return b;
  };
  const submit = button("Send prompt", "send", () => ({
    prompt: input.value,
    stream: checkbox.checked,
  }));
  const read = button("Read task", "read");
  const cancel = button("Cancel remote task", "cancel");
  const disconnect = button("Disconnect", "disconnect");
  const navigation = [
    button("Export transcript", "export"),
    button("New conversation / reselect", "new"),
    button("Return to Catalog", "catalog"),
    button("Agent help", "help"),
  ];
  const terminal = (s) =>
    ["completed", "failed", "canceled", "rejected"].includes(s);
  const validate = () => {
    if (!model) return;
    const bytes = new TextEncoder().encode(input.value).length;
    budget.textContent = `${bytes.toLocaleString("en-US")} / ${model.promptLimit.toLocaleString("en-US")} UTF-8 bytes`;
    const problem = !input.value.trim()
      ? "Enter a prompt."
      : bytes > model.promptLimit
        ? "Prompt exceeds the UTF-8 byte limit. Shorten it before sending."
        : /[\0\ud800-\udfff]/u.test(input.value)
          ? "Remove null characters or incomplete Unicode characters."
          : "";
    if (error.textContent !== problem) error.textContent = problem;
    input.setAttribute("aria-invalid", String(Boolean(problem)));
    const s = model.state;
    submit.disabled =
      Boolean(problem) ||
      !s?.connected ||
      s.busy ||
      s.uncertain ||
      !(
        s.status === "ready" ||
        s.status === "input-required" ||
        terminal(s.status)
      );
  };
  const draft = () => {
    validate();
    // Host memory only: never use setState or browser storage for private input.
    api.postMessage({
      type: "draft",
      prompt: input.value,
      stream: checkbox.checked,
    });
  };
  input.addEventListener("input", draft);
  checkbox.addEventListener("change", draft);
  const text = (element, value) => {
    if (element.textContent !== value) element.textContent = value;
  };
  window.addEventListener("message", ({ data }) => {
    if (data.type !== "state") return;
    model = data;
    if (!initialized) {
      input.value = data.draft.prompt;
      checkbox.checked = data.draft.stream;
      initialized = true;
    }
    text(title, model.name);
    text(destination, model.destination);
    const s = model.state;
    if (!s) return;
    for (const button of navigation) button.disabled = false;
    const guidance = s.uncertain
      ? "Outcome uncertain. Do not resend. Read the known task if connected, or check with the agent before starting over."
      : !s.connected
        ? "Disconnected locally. This action does not cancel remote work."
        : s.activity === "preparing"
          ? "Preparing: checking current Catalog access and the Agent Card."
          : s.activity === "sending"
            ? "Sending prompt; awaiting the agent."
            : s.activity === "streaming"
              ? "Streaming reply."
              : s.activity === "reading"
                ? "Reading task."
                : s.status === "input-required"
                  ? "Input required. Send another prompt to continue this task."
                  : s.status === "authentication-required"
                    ? "Authentication required by the agent. This client cannot complete an agent OAuth flow; reselect with supported target credentials."
                    : s.status === "completed"
                      ? "Completed. Another prompt keeps this conversation's context."
                      : s.status === "ready"
                        ? "Ready. Sending requires your confirmation."
                        : s.status === "working" || s.status === "submitted"
                          ? "Task in progress. Read task to check it; there is no automatic polling."
                          : `Task ${s.status}.`;
    text(
      status,
      `Status: ${s.status}${s.connected ? "" : " (disconnected)"}. ${guidance}`,
    );
    text(task, s.task ? `Task: ${s.task}` : "");
    text(request, s.error ? `Request: ${s.error}` : "");
    // Reuse unchanged entries and never move the reader to the latest response.
    const top = transcript.scrollTop;
    const current = new Set();
    let cursor = transcript.firstElementChild;
    for (const [key, heading, content] of [
      ...s.messages.map((m) => [
        `message:${m.id}`,
        m.role === "user" ? "You" : "Agent",
        m.text,
      ]),
      ...s.artifacts.map((a) => [`artifact:${a.id}`, a.name, a.parts.join("")]),
    ]) {
      current.add(key);
      let entry = entries.get(key);
      if (!entry) {
        const article = document.createElement("article");
        entry = {
          article,
          heading: node("h2", heading, article),
          content: node("pre", content, article),
        };
        entries.set(key, entry);
      } else {
        text(entry.heading, heading);
        text(entry.content, content);
      }
      if (entry.article !== cursor)
        transcript.insertBefore(entry.article, cursor);
      cursor = entry.article.nextElementSibling;
    }
    for (const [key, entry] of entries)
      if (!current.has(key)) {
        entry.article.remove();
        entries.delete(key);
      }
    transcript.scrollTop = top;
    // Read-only preserves caret and keyboard focus while a request is active.
    input.readOnly = s.busy;
    checkbox.disabled = !model.streaming;
    text(
      streamHint,
      model.streaming
        ? "Streaming is advertised by this Agent Card."
        : "This Agent Card does not advertise streaming. Use a regular reply.",
    );
    read.disabled = !s.connected || s.busy || !s.task;
    cancel.disabled = !s.connected || !s.task || terminal(s.status);
    disconnect.disabled = !s.connected;
    validate();
  });
  api.postMessage({ type: "ready" });
})();

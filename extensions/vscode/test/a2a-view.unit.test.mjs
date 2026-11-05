import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { runInNewContext } from "node:vm";

// Minimal isolated DOM: layout and actual editor hide/show belong to the host fixture.
function view() {
  const elements = [];
  class Element {
    constructor(tag) {
      this.tagName = tag;
      this.children = [];
      this.listeners = {};
      this.attributes = {};
      this.scrollTop = 0;
      this.value = "";
      elements.push(this);
    }
    append(child) {
      this.children.push(child);
      child.parent = this;
    }
    get firstElementChild() {
      return this.children[0] ?? null;
    }
    get nextElementSibling() {
      return (
        this.parent?.children[this.parent.children.indexOf(this) + 1] ?? null
      );
    }
    insertBefore(child, reference) {
      if (child === reference) return;
      if (child.parent) child.remove();
      const index =
        reference === null
          ? this.children.length
          : this.children.indexOf(reference);
      assert.ok(index >= 0, "The reference belongs to this parent");
      this.children.splice(index, 0, child);
      child.parent = this;
    }
    setAttribute(key, value) {
      this.attributes[key] = value;
    }
    addEventListener(key, value) {
      this.listeners[key] = value;
    }
    remove() {
      this.parent.children = this.parent.children.filter((c) => c !== this);
    }
    replaceChildren() {
      this.children = [];
    }
  }
  const root = new Element("main"),
    sent = [];
  const placeholder = new Element("h1");
  placeholder.textContent = "Catalog startup placeholder";
  root.append(placeholder);
  let receive;
  return {
    elements,
    root,
    sent,
    context: {
      acquireVsCodeApi: () => ({ postMessage: (m) => sent.push(m) }),
      document: {
        getElementById: () => root,
        createElement: (tag) => new Element(tag),
      },
      window: {
        addEventListener: (type, listener) => {
          assert.equal(type, "message");
          receive = listener;
        },
      },
      TextEncoder,
    },
    state: (state, extra = {}) =>
      receive({
        data: {
          type: "state",
          revision: 1,
          name: "Agent",
          destination: "https://agent.example",
          streaming: true,
          promptLimit: 16384,
          draft: { prompt: "unsent", stream: true },
          state: {
            status: "ready",
            connected: true,
            messages: [],
            artifacts: [],
            busy: false,
            ...state,
          },
          ...extra,
        },
      }),
    find: (id) => elements.find((e) => e.id === id),
    button: (name) =>
      elements.find((e) => e.tagName === "button" && e.textContent === name),
  };
}
const script = await readFile(
  new URL("../media/a2a.js", import.meta.url),
  "utf8",
);
test("Conversation input and actions wait for the first current host snapshot", () => {
  const v = view();
  runInNewContext(script, v.context);
  assert.equal(
    v.find("prompt").readOnly,
    true,
    "An early edit must not be overwritten by the initial host draft",
  );
  assert.equal(v.find("stream-reply").disabled, true);
  assert.ok(
    v.elements.filter((e) => e.tagName === "button").every((e) => e.disabled),
  );
  assert.equal(JSON.stringify(v.sent), '[{"type":"ready"}]');
  v.state({});
  assert.equal(v.find("prompt").value, "unsent");
  assert.equal(v.find("prompt").readOnly, false);
  assert.equal(v.find("stream-reply").disabled, false);
  assert.equal(v.button("Send prompt").disabled, false);
  for (const name of [
    "Export transcript",
    "New conversation / reselect",
    "Return to Catalog",
  ])
    assert.equal(v.button(name).disabled, false);
});
test("Conversation updates preserve controls, draft, caret and transcript nodes without replay announcements", () => {
  const v = view();
  runInNewContext(script, v.context);
  const messages = [{ id: "u", role: "user", text: "hello" }];
  v.state({ messages });
  assert.equal(v.root.children.filter((e) => e.tagName === "h1").length, 1);
  assert.equal(v.root.children[0].textContent, "Agent");
  const prompt = v.find("prompt"),
    checkbox = v.find("stream-reply"),
    transcript = v.elements.find(
      (e) => e.className === "conversation-transcript",
    );
  assert.equal(prompt.value, "unsent");
  assert.equal(checkbox.checked, true);
  prompt.value = "still drafting";
  prompt.selectionStart = 3;
  prompt.selectionEnd = 6;
  prompt.listeners.input();
  assert.equal(v.sent.at(-1).type, "draft");
  assert.equal(v.sent.at(-1).prompt, "still drafting");
  const first = transcript.children[0];
  transcript.scrollTop = 12;
  v.state({
    messages,
    busy: true,
    activity: "streaming",
    artifacts: [
      { id: "a", name: "Reply", parts: ["<script>inert()</script>"] },
    ],
  });
  assert.equal(v.find("prompt"), prompt);
  assert.equal(prompt.value, "still drafting");
  assert.equal(prompt.readOnly, true);
  assert.equal(prompt.selectionStart, 3);
  assert.equal(prompt.selectionEnd, 6);
  assert.equal(transcript.children[0], first);
  assert.equal(transcript.scrollTop, 12);
  assert.equal(transcript.attributes["aria-live"], undefined);
  assert.equal(
    transcript.children[1].children[1].textContent,
    "<script>inert()</script>",
  );
  for (const state of [
    { messages, status: "working", task: "t", busy: true, activity: "reading" },
    { messages, status: "canceled", task: "t", busy: true },
    { messages, status: "canceled", task: "t", busy: false },
  ]) {
    v.state(state);
    assert.equal(v.find("prompt"), prompt);
    assert.equal(prompt.selectionStart, 3);
    assert.equal(prompt.selectionEnd, 6);
    assert.equal(transcript.children[0], first);
    assert.equal(transcript.scrollTop, 12);
    assert.equal(checkbox.checked, true);
  }
  v.state({ messages, status: "completed" });
  assert.equal(prompt.readOnly, false);
  assert.equal(checkbox.checked, true);
  assert.equal(transcript.children.length, 1);
});
test("Incremental transcript updates keep snapshot order and reuse existing entries", () => {
  const v = view();
  runInNewContext(script, v.context);
  const user = { id: "u", role: "user", text: "question" };
  const a = { id: "a", name: "A", parts: ["first artifact"] };
  const b = { id: "b", name: "B", parts: ["second artifact"] };
  v.state({ messages: [user], artifacts: [a, b] });
  const transcript = v.elements.find(
    (e) => e.className === "conversation-transcript",
  );
  const [userEntry, aEntry, bEntry] = transcript.children;
  assert.deepEqual(
    transcript.children.map((e) => e.children[0].textContent),
    ["You", "A", "B"],
  );
  transcript.scrollTop = 12;
  v.state({
    messages: [user, { id: "m", role: "agent", text: "later status message" }],
    artifacts: [b, a],
  });
  assert.deepEqual(
    transcript.children.map((e) => e.children[0].textContent),
    ["You", "Agent", "B", "A"],
  );
  assert.equal(transcript.children[0], userEntry);
  assert.equal(transcript.children[2], bEntry);
  assert.equal(transcript.children[3], aEntry);
  assert.equal(transcript.scrollTop, 12);
  const stable = [...transcript.children];
  v.state({
    messages: [user, { id: "m", role: "agent", text: "later status message" }],
    artifacts: [b, a],
  });
  assert.deepEqual(transcript.children, stable);
});
test("Prompt validation uses actual UTF-8 bytes, exposes field errors, and refuses unsafe sends", () => {
  const v = view();
  runInNewContext(script, v.context);
  v.state({});
  const prompt = v.find("prompt"),
    send = v.button("Send prompt");
  for (const value of ["", " ", "é".repeat(8193), "\0", "\ud800"]) {
    prompt.value = value;
    prompt.listeners.input();
    assert.equal(send.disabled, true);
    assert.equal(prompt.attributes["aria-invalid"], "true");
    assert.ok(v.find("prompt-error").textContent);
  }
  prompt.value = "😀".repeat(4096);
  prompt.listeners.input();
  assert.equal(
    v.find("prompt-budget").textContent,
    "16,384 / 16,384 UTF-8 bytes",
  );
  assert.equal(send.disabled, false);
  send.listeners.click();
  assert.equal(v.sent.at(-1).type, "send");
  assert.equal(v.sent.at(-1).prompt, prompt.value);
  for (const state of [
    { status: "authentication-required" },
    { uncertain: true },
    { connected: false },
    { busy: true },
  ]) {
    v.state(state);
    assert.equal(send.disabled, true);
  }
  const status = v.elements.find((e) => e.attributes.role === "status");
  for (const [state, expected] of [
    [{ activity: "preparing", busy: true }, /Preparing/],
    [{ activity: "sending", busy: true }, /Sending prompt/],
    [{ activity: "streaming", busy: true }, /Streaming reply/],
    [{ activity: "reading", busy: true }, /Reading task/],
    [{ status: "input-required" }, /continue this task/],
    [{ status: "authentication-required" }, /Authentication required/],
    [{ uncertain: true, connected: false }, /Outcome uncertain.*Do not resend/],
    [{ connected: false }, /Disconnected/],
    [{ status: "completed" }, /Completed/],
  ]) {
    v.state(state);
    assert.match(status.textContent, expected);
    assert.equal(prompt.value, "😀".repeat(4096));
  }
  v.state({}, { streaming: false });
  assert.equal(v.find("stream-reply").disabled, true);
  assert.match(
    v.find("stream-hint").textContent,
    /does not advertise streaming/,
  );
  v.button("New conversation / reselect").listeners.click();
  assert.equal(v.sent.at(-1).type, "new");
  v.button("Return to Catalog").listeners.click();
  assert.equal(v.sent.at(-1).type, "catalog");
});

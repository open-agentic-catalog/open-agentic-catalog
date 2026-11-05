import * as vscode from "vscode";
import { ClientError } from "./config.js";
import {
  Opinions,
  feedbackCategories,
  feedbackText,
  type OpinionFamily,
} from "./opinions.js";
import type { Browser } from "./browse.js";
import type { Session } from "./session.js";

/** Native private editing; no comment or credential crosses the webview bridge. */
export class CatalogOpinions implements vscode.Disposable {
  private opinions: Opinions;
  private subscription: vscode.Disposable;
  constructor(
    private browser: Browser,
    private session: Session,
    private preview: (text: string, title: string) => Promise<void>,
  ) {
    this.opinions = new Opinions(session);
    this.subscription = session.onChange(() => this.opinions.clear());
  }
  private guard() {
    return this.session.guard(this.session.connection());
  }
  private async pick<T extends vscode.QuickPickItem | string>(
    items: T[],
    options: vscode.QuickPickOptions,
  ): Promise<T | undefined> {
    const check = this.guard(),
      cancellation = new vscode.CancellationTokenSource();
    const listener = this.session.onChange(() => cancellation.cancel());
    try {
      const value =
        typeof items[0] === "string"
          ? await vscode.window.showQuickPick(
              items as string[],
              options,
              cancellation.token,
            )
          : await vscode.window.showQuickPick(
              items as vscode.QuickPickItem[],
              options,
              cancellation.token,
            );
      check();
      return value as T | undefined;
    } finally {
      listener.dispose();
      cancellation.dispose();
    }
  }
  private item() {
    const item = this.browser.state.detail?.item;
    if (!item) throw new ClientError("select_local_release");
    return item;
  }
  private async ratingEdit(
    context: Awaited<ReturnType<Opinions["rating"]>>,
    current: () => void = () => {},
  ) {
    const account = this.guard();
    const check = () => {
      account();
      current();
    };
    const score = await this.pick(
      [1, 2, 3, 4, 5].map((value) => ({ label: `${value} / 5`, value })),
      {
        title: "Your personal rating",
        placeHolder: `${context.target.identifier} · ${context.target.version} · a human opinion, not trust or approval`,
      },
    );
    check();
    if (!score) return;
    const prior =
      context.own?.value.state === "active" &&
      context.own.value.consent === true;
    const sharing = await this.pick(
      [
        {
          label: "Private",
          description: "Do not include my rating in a public publisher summary",
          value: false,
          picked: !prior,
        },
        {
          label: "Allow publisher summary",
          description: "Consent to the Catalog's aggregate sharing rules",
          value: true,
          picked: prior,
        },
      ].sort((a, b) => Number(b.picked) - Number(a.picked)),
      {
        title: "Rating sharing",
        placeHolder: prior
          ? "Currently allows publisher summaries"
          : "Currently private (default for new/withdrawn opinions)",
      },
    );
    check();
    if (!sharing) return;
    await this.opinions.submitRating(context, score.value, sharing.value);
    void vscode.window.showInformationMessage(
      "Rating saved. Refresh the release to read its current aggregate.",
    );
  }
  async rate() {
    const item = this.item(),
      detail = this.browser.state.detail;
    const check = () => {
      if (this.browser.state.detail !== detail)
        throw new ClientError("selection_changed");
    };
    const context = await this.opinions.rating(item);
    check();
    await this.ratingEdit(context, check);
  }
  private async text(
    initial: string,
    editing: boolean,
  ): Promise<string | undefined> {
    const check = this.guard(),
      input = vscode.window.createInputBox();
    input.title = editing
      ? "Edit feedback (reopens and clears administrator response)"
      : "Private item feedback";
    input.prompt =
      "Private to you and authorized Catalog administrators. Type one paragraph, or use the clipboard button for multiline text. Maximum 4000 code points.";
    input.ignoreFocusOut = true;
    const paste = {
      iconPath: new vscode.ThemeIcon("clippy"),
      tooltip: "Use full clipboard text (preserves newlines)",
    };
    const keep = {
      iconPath: new vscode.ThemeIcon("history"),
      tooltip: "Keep the current feedback text",
    };
    input.buttons = editing ? [paste, keep] : [paste];
    let full: string | undefined,
      changing = false;
    const retain = (value: string) => {
      feedbackText(value);
      full = value;
      changing = true;
      input.value = `[${Array.from(value).length} code points, ${value.split(/\r\n|\n|\r/u).length} lines retained; Enter to accept, type to replace]`;
      changing = false;
      input.validationMessage = undefined;
    };
    if (initial) retain(initial);
    const subscriptions: vscode.Disposable[] = [];
    try {
      return await new Promise<string | undefined>((resolve, reject) => {
        const fail = (error: unknown) => {
          reject(error);
          input.hide();
        };
        subscriptions.push(
          this.session.onChange(() => {
            full = undefined;
            input.value = "";
            fail(new ClientError("connection_changed"));
          }),
          input.onDidHide(() => resolve(undefined)),
          input.onDidChangeValue(() => {
            if (!changing) full = undefined;
          }),
          input.onDidTriggerButton(async (button) => {
            try {
              check();
              const value =
                button === keep
                  ? initial
                  : await vscode.env.clipboard.readText();
              check();
              retain(value);
            } catch (error) {
              if (
                error instanceof ClientError &&
                error.code === "connection_changed"
              )
                fail(error);
              else
                input.validationMessage =
                  "Enter 1–4000 Unicode code points containing text.";
            }
          }),
          input.onDidAccept(() => {
            try {
              check();
              const value = feedbackText(full ?? input.value);
              resolve(value);
              input.hide();
            } catch {
              input.validationMessage =
                "Enter 1–4000 Unicode code points containing text.";
            }
          }),
        );
        input.show();
      });
    } finally {
      full = undefined;
      subscriptions.forEach((value) => value.dispose());
      input.dispose();
    }
  }
  private async comment(
    context: Record<string, unknown>,
    existing?: { value: Record<string, unknown>; etag: string },
    current: () => void = () => {},
  ) {
    const account = this.guard();
    const check = () => {
      account();
      current();
    };
    const category = await this.pick(
      feedbackCategories
        .map((label) => ({ label }))
        .sort(
          (a, b) =>
            Number(b.label === existing?.value.category) -
            Number(a.label === existing?.value.category),
        ),
      { title: "Private feedback category" },
    );
    check();
    if (!category) return;
    const comment = await this.text(
      (existing?.value.comment as string) ?? "",
      !!existing,
    );
    check();
    if (comment === undefined) return;
    await this.opinions.submitFeedback(
      context,
      category.label,
      comment,
      existing
        ? { id: existing.value.id as string, etag: existing.etag }
        : undefined,
    );
    void vscode.window.showInformationMessage(
      "Private feedback saved. Use My Feedback to read administrator responses.",
    );
  }
  async feedback() {
    const item = this.item(),
      detail = this.browser.state.detail;
    const check = () => {
      if (this.browser.state.detail !== detail)
        throw new ClientError("selection_changed");
    };
    const context = await this.opinions.feedbackContext(item);
    check();
    await this.comment(context, undefined, check);
  }
  async list(family: OpinionFamily) {
    const check = this.guard();
    let cursor: string | undefined;
    for (;;) {
      const page = await this.opinions.page(family, cursor);
      check();
      const choice = await this.pick(
        [
          ...page.items.map((value) => ({
            label:
              family === "ratings"
                ? `${value.score} / 5 · ${value.state}`
                : `${value.category} · ${value.state}`,
            description: String(value.id),
            value,
          })),
          ...(page.next
            ? [
                {
                  label: "Next page",
                  description: "25 items per page; private cursor is not saved",
                  value: undefined,
                },
              ]
            : []),
        ],
        {
          title: family === "ratings" ? "My ratings" : "My private feedback",
          placeHolder:
            "Reads and withdrawal remain available after resource access is lost",
        },
      );
      check();
      if (!choice) return;
      if (!choice.value) {
        cursor = page.next;
        continue;
      }
      const current = await this.opinions.get(
        family,
        choice.value.id as string,
      );
      check();
      const action = await this.pick(
        [
          { label: "Read current item", value: "read" },
          ...(!["withdrawn", "removed"].includes(current.value.state as string)
            ? [
                ...(family === "feedback" ||
                (current.value.target as { origin?: string })?.origin ===
                  "local"
                  ? [{ label: "Edit", value: "edit" }]
                  : []),
                { label: "Withdraw", value: "withdraw" },
              ]
            : []),
        ],
        {
          title:
            family === "ratings"
              ? "Manage my rating"
              : "Manage my private feedback",
        },
      );
      check();
      if (!action) return;
      if (action.value === "read")
        await this.preview(
          JSON.stringify(current.value, null, 2),
          family === "ratings"
            ? "My rating"
            : "My private feedback and administrator response",
        );
      else if (action.value === "withdraw") {
        if (
          (await vscode.window.showWarningMessage(
            "Withdraw this personal item? Its text or score will be cleared. This does not require resource access.",
            { modal: true },
            "Withdraw",
          )) !== "Withdraw"
        )
          return;
        check();
        await this.opinions.withdraw(family, current);
        void vscode.window.showInformationMessage("Personal item withdrawn.");
      } else if (family === "feedback") await this.comment({}, current);
      else {
        // Use retained exact context, never require the target to remain in search results.
        await this.ratingEdit({
          target: current.value.target as Awaited<
            ReturnType<Opinions["rating"]>
          >["target"],
          releaseId: current.value.releaseId as string,
          own: current,
        });
      }
      return;
    }
  }
  async recover() {
    if (!this.opinions.hasPending()) {
      void vscode.window.showInformationMessage(
        "No pending opinion command. Read My Ratings or My Feedback before retrying a new submission.",
      );
      return;
    }
    const check = this.guard();
    const action = await this.pick(
      [
        "Recover exact command",
        "Forget pending command after checking my list",
      ],
      { title: "Uncertain personal command" },
    );
    check();
    if (!action) return;
    if (action.startsWith("Forget")) {
      if (
        (await vscode.window.showWarningMessage(
          "Have you checked your personal list? Forget only clears local recovery; the server may have completed the command.",
          { modal: true },
          "Forget pending command",
        )) === "Forget pending command"
      ) {
        check();
        this.opinions.clear();
      }
      return;
    }
    if (
      (await vscode.window.showWarningMessage(
        "The previous command's outcome is uncertain. Retry the exact command with its original idempotency key? Read your personal list first if needed. Closing or changing the Catalog session clears this in-memory command.",
        { modal: true },
        "Recover exact command",
      )) !== "Recover exact command"
    )
      return;
    check();
    await this.opinions.recover();
    void vscode.window.showInformationMessage(
      "Command recovered. Read your personal list for current state.",
    );
  }
  dispose() {
    this.opinions.clear();
    this.subscription.dispose();
  }
}

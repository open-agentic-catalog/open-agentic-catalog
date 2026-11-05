// Explicit clipboard actions copy only the bounded reference displayed beside them.
import { useLayoutEffect, useRef, useState } from "react";
import { safeLink } from "../api/client";
import { controlIcons } from "../shell/Icon";

/** Keep references selectable when clipboard permission or secure-context support is absent. */
export function CopyReference({
  label,
  value,
  open = false,
  openLabel,
}: {
  label: string;
  value: string;
  open?: boolean;
  openLabel?: string;
}) {
  const href =
    open &&
    value.length <= 8192 &&
    !Array.from(value).some(
      (char) => char.charCodeAt(0) <= 32 || char.charCodeAt(0) === 127,
    )
      ? safeLink(value)
      : undefined;
  return (
    <div className="handoff-reference">
      <span>{label}</span>
      <code>{value}</code>
      {href && (
        <a
          href={href}
          target="_blank"
          rel="noopener noreferrer"
          referrerPolicy="no-referrer"
          title={href}
        >
          {controlIcons["external"]}
          {openLabel ?? `Open ${label}`}
        </a>
      )}
      <CopyText
        label={`Copy ${label}`}
        text={value}
        disabled={value.length > 8192}
        fallback="Copy unavailable. Select the reference and copy it manually."
      />
    </div>
  );
}

/** Copy only supplied, already bounded text; never fetch, format or reconstruct it. */
export function CopyText({
  label,
  text,
  disabled = false,
  fallback = "Copy unavailable. Select the text and copy it manually.",
}: {
  label: string;
  text: string;
  disabled?: boolean;
  fallback?: string;
}) {
  const [status, setStatus] = useState("");
  const generation = useRef(0);
  // Fence the displayed text before interaction. A delayed mount effect must
  // not invalidate a clipboard action already started by an early click.
  useLayoutEffect(() => {
    setStatus("");
    const current = ++generation.current;
    return () => {
      generation.current = current + 1;
    };
  }, [text]);
  return (
    <>
      <button
        type="button"
        disabled={disabled}
        onClick={() => {
          void (async () => {
            const current = generation.current;
            try {
              await navigator.clipboard.writeText(text);
              if (current === generation.current) setStatus("Copied.");
            } catch {
              if (current === generation.current) setStatus(fallback);
            }
          })();
        }}
      >
        {controlIcons["copy"]}
        {label}
      </button>
      {status && <p role="status">{status}</p>}
    </>
  );
}

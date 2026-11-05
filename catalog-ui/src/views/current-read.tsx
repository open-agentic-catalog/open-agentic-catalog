// Deliberate metadata readback keeps acknowledgements separate from last-loaded state.
import { useRef, useState, type ReactNode } from "react";
import { CommandReceipt } from "./authoring";
import { ReadState, useRead } from "./workspace";
import { controlIcons } from "../shell/Icon";

/** A read started before acknowledgement cannot make that command's view current. */
export function useCurrentRead<T>(
  key: string,
  load: (signal: AbortSignal) => Promise<T>,
  onFailure: (error: unknown) => void,
) {
  const epoch = useRef(0);
  const [committed, setCommitted] = useState<{
    key: string;
    message: string;
    epoch: number;
    serial: number;
  }>();
  const read = useRead(
    key,
    async (signal) => {
      const started = ++epoch.current;
      return { value: await load(signal), epoch: started };
    },
    onFailure,
    true,
  );
  const receipt = committed?.key === key ? committed : undefined;
  return {
    ...read,
    data: read.data?.value,
    epoch: read.data?.epoch ?? 0,
    message: receipt?.message,
    acknowledgements: receipt?.serial ?? 0,
    stale: !!receipt && (!read.data || read.data.epoch <= receipt.epoch),
    acknowledge: (message: string) => {
      const acknowledgedEpoch = epoch.current;
      setCommitted((previous) => ({
        key,
        message,
        epoch: acknowledgedEpoch,
        serial: (previous?.serial ?? 0) + 1,
      }));
    },
  };
}

/** Reload only the owning metadata read; private evidence and commands stay explicit. */
export function CurrentRead<T>({
  read,
  label,
  reload,
  refresh = read.refresh,
  children,
}: {
  read: ReturnType<typeof useCurrentRead<T>>;
  label: string;
  reload: string;
  refresh?: () => void;
  children?: ReactNode;
}) {
  return (
    <>
      <button type="button" disabled={read.busy} onClick={refresh}>
        {controlIcons["refresh"]}
        {reload}
      </button>
      <ReadState {...read} refresh={refresh} />
      {read.message && (
        <CommandReceipt key={read.acknowledgements} message={read.message}>
          <span>
            {" "}
            This acknowledgement can be a historical replay. Reload to read
            current state.
          </span>
          {children}
        </CommandReceipt>
      )}
      {read.data !== undefined && (
        <p className="eyebrow">Last loaded {label}</p>
      )}
      {read.stale && (
        <p className="notice">
          This view has not been reread since the command acknowledgement. Use{" "}
          {reload} to check current state.
        </p>
      )}
    </>
  );
}

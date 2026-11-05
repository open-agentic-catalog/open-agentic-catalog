export function networkObserver(
  origin: string,
  basePath?: string,
): {
  request(method: string, url: string): void;
  finished(method: string, url: string): void;
  diagnostics(): {
    assets: {
      asset: "service" | "discovery";
      event: "request" | "response" | "finished" | "failed";
      status?: number;
      reason?:
        | "aborted"
        | "connection-refused"
        | "connection-reset"
        | "network-changed"
        | "other";
    }[];
    transports: {
      family: "api" | "asset" | "document";
      reason:
        "connection-refused" | "connection-reset" | "network-changed" | "other";
    }[];
    truncated: boolean;
  };
  expectFailure(
    method: string,
    path: string,
    status: number,
    count?: number,
  ): void;
  observe(method: string, url: string, status: number): void;
  failure(method: string, url: string, reason: string | undefined): void;
  results(): string[];
};

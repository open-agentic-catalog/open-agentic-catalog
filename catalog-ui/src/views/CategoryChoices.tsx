// Local category counts supplement explicit filters; external totals are separate.
import { useEffect, useId, useRef, useState } from "react";
import { result } from "../api/client";
import type { Client } from "../api/client";
import type { components } from "../api/schema";
import * as checks from "./responses";

type Query = components["schemas"]["CatalogExploreQuery"];
type Response = components["schemas"]["CatalogExploreResponse"];

export function CategoryChoices({
  client,
  query,
  identityKey,
  selected,
  onChange,
  onFailure,
}: {
  client: Client;
  query: Query;
  identityKey: string;
  selected: string[];
  onChange: (values: string[]) => void;
  onFailure: (error: unknown) => void;
}) {
  const key = JSON.stringify(query);
  const countId = useId();
  const failure = useRef(onFailure);
  failure.current = onFailure;
  const [state, setState] = useState<{
    client: Client;
    key: string;
    identityKey: string;
    data?: Response;
    failed?: boolean;
  }>();
  useEffect(() => {
    const controller = new AbortController();
    let current = true;
    const context = { client, key, identityKey };
    void client.api
      .POST("/api/v1/catalog/explore", {
        body: {
          query: JSON.parse(key) as Query,
          facets: [{ field: "category", limit: 24, minCount: 1 }],
        },
        signal: controller.signal,
      })
      .then((reply) => {
        const data = result(reply, checks.CatalogExploreResponse);
        if (!data.facets.category) throw new Error("Missing category facet");
        if (current) setState({ ...context, data });
      })
      .catch((error: unknown) => {
        if (current) {
          setState({ ...context, failed: true });
          failure.current(error);
        }
      });
    return () => {
      current = false;
      controller.abort();
    };
  }, [client, key, identityKey]);
  const current =
    state?.client === client &&
    state.key === key &&
    state.identityKey === identityKey
      ? state
      : undefined;
  const data = current?.data;
  const facet = data?.facets.category;
  const choices = [
    ...new Set([
      ...selected,
      ...(facet?.buckets.map((bucket) => bucket.value) ?? []),
    ]),
  ];
  return (
    <>
      <p className="filter-help">
        Local Catalog matches for the applied search. External results are not
        counted.
      </p>
      {!current && <p role="status">Loading local categories...</p>}
      {current?.failed && (
        <p role="status">
          Local category suggestions are unavailable. You can still enter
          categories.
        </p>
      )}
      {facet && !facet.buckets.length && (
        <p className="filter-help">
          No local category suggestions. You can still enter categories.
        </p>
      )}
      {data?.partialIndex && (
        <p className="filter-help">
          Some local records can match only by keywords.
        </p>
      )}
      <div className="filter-options">
        {choices.map((value, index) => {
          const bucket = facet?.buckets.find((item) => item.value === value);
          const checked = selected.includes(value);
          const invalid =
            !value.trim() || value.includes("\0") || [...value].length > 80;
          return (
            <label className="filter-option" key={value}>
              <input
                type="checkbox"
                aria-label={value}
                aria-describedby={bucket ? `${countId}-${index}` : undefined}
                checked={checked}
                disabled={!checked && (invalid || selected.length >= 16)}
                onChange={(event) =>
                  onChange(
                    event.target.checked
                      ? [...selected, value]
                      : selected.filter((item) => item !== value),
                  )
                }
              />
              <span>{value}</span>
              {bucket && (
                <small
                  id={`${countId}-${index}`}
                  className="category-count"
                  aria-label={`${bucket.count} local ${bucket.count === 1 ? "match" : "matches"}`}
                >
                  {bucket.count}
                </small>
              )}
            </label>
          );
        })}
      </div>
      {!!facet?.otherCount && (
        <p className="filter-help">
          More local categories are not shown. Enter another label below.
        </p>
      )}
    </>
  );
}

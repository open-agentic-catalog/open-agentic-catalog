// One tab-local owner survives saved-search, Catalog and exact-detail navigation.
import { useState } from "react";
import Catalog from "./Catalog";
import { PersonalLibrary } from "./Personal";
import type { components } from "../api/schema";
import type { WorkspaceProps } from "./workspace";
import { permits } from "../api/permissions";

export default function DiscoveryWorkspace(props: WorkspaceProps) {
  const [saved, setSaved] = useState<{
    path: string;
    criteria: components["schemas"]["PersonalCriteria"];
  }>();
  const library = location.pathname.startsWith(
    props.runtime.basePath + "/saved-items",
  );
  const libraryReadable =
    props.access.actorType === "human" &&
    permits(props.access, "library.own", "catalog.read");
  // A different library item starts a fresh workspace. No private result cache survives it.
  if (
    saved &&
    library &&
    (location.pathname !== saved.path || !libraryReadable)
  )
    setSaved(undefined);
  if (
    library &&
    (!saved || location.pathname !== saved.path || !libraryReadable)
  )
    return (
      <PersonalLibrary
        key={props.id}
        {...props}
        onOpenSearch={(criteria) => {
          if (libraryReadable) setSaved({ path: location.pathname, criteria });
        }}
      />
    );
  return (
    <>
      {saved && library && (
        <p>
          <a href={`${props.runtime.basePath}/saved-items`}>
            Back to saved items
          </a>
        </p>
      )}
      <Catalog
        key={JSON.stringify(props.access)}
        client={props.client}
        runtime={props.runtime}
        access={props.access}
        onFailure={props.onFailure}
        managedNavigation
        {...(saved
          ? { stagedSearch: saved.criteria, searchPath: saved.path }
          : {})}
      />
    </>
  );
}

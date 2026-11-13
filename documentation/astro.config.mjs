import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import { unified } from '@astrojs/markdown-remark';
import starlightOpenAPI, { openAPISidebarGroups } from 'starlight-openapi';
import { base, remarkRepositoryLinks } from './scripts/site.mjs';
import { openAPIAnchors } from './scripts/openapi-anchors.mjs';
import {edition} from './scripts/edition.mjs';
import { contentFocus } from './scripts/content-focus.mjs';

const publicEdition = edition();

export default defineConfig({
  base,
  site: publicEdition?.site,
  output: 'static',
  cacheDir: './.astro/cache',
  trailingSlash: 'always',
  server: {host: '127.0.0.1', port: 4321},
  vite: {define: {'import.meta.env.CATALOG_DOCS_EDITION': JSON.stringify(JSON.stringify(publicEdition))}, build: {sourcemap: false}, preview: {strictPort: true}},
  markdown: {processor: unified({remarkPlugins: [remarkRepositoryLinks]})},
  integrations: [starlight({
    title: 'Open Agentic Catalog',
    favicon: '/generated/favicon.svg',
    description: 'Governed authoring, publication, discovery and federation for Agents, MCP Servers, Skills and Plugins.',
    editLink: undefined,
    social: [],
    customCss: ['./src/styles/navigation.css', './src/styles/content.css', './src/styles/openapi.css'],
    // Keep shell comments and heredoc file content intact when copying examples.
    expressiveCode: { frames: { removeCommentsWhenCopyingTerminalFrames: false } },
    sidebar: [
      { label: 'Get started', items: [
        { label: 'Welcome', slug: '', attrs: { 'data-nav-icon': 'information' } },
        { label: 'Connect as a reader', slug: 'guides/reader-quickstart', attrs: { 'data-nav-icon': 'map' } },
        { label: 'Load a sample Catalog', slug: 'guides/sample-catalog', attrs: { 'data-nav-icon': 'map' } },
        { label: 'Concepts and lifecycle', slug: 'guides/concepts', attrs: { 'data-nav-icon': 'open-book' } },
        { label: 'Local quickstart and API walkthrough', slug: 'guides/local-development', attrs: { 'data-nav-icon': 'map' } },
        { label: 'Glossary', slug: 'reference/glossary', attrs: { 'data-nav-icon': 'open-book' } },
      ] },
      { label: 'Use the Catalog', collapsed: true, items: [
        { label: 'Browse and manage with the UI', slug: 'guides/catalog-ui', attrs: { 'data-nav-icon': 'window' } },
        { label: 'Search cookbook', slug: 'guides/search-cookbook', attrs: { 'data-nav-icon': 'open-book' } },
        { label: 'Understand results and evidence', slug: 'guides/understand-results', attrs: { 'data-nav-icon': 'open-book' } },
        { label: 'Add and update resources', slug: 'guides/resource-authoring', attrs: { 'data-nav-icon': 'pen' } },
        { label: 'Move drafts between Catalogs', slug: 'guides/draft-transfer', attrs: { 'data-nav-icon': 'open-book' } },
        { label: 'Review, publish and maintain', slug: 'guides/publishing', attrs: { 'data-nav-icon': 'approve-check-circle' } },
        { label: 'Manage scoped access', slug: 'guides/access-management', attrs: { 'data-nav-icon': 'padlock' } },
        { label: 'Catalog Agent Plugins', slug: 'guides/agent-plugins', attrs: { 'data-nav-icon': 'puzzle' } },
        { label: 'Private item feedback', slug: 'guides/feedback', attrs: { 'data-nav-icon': 'comment' } },
        { label: 'Resource ratings', slug: 'guides/ratings', attrs: { 'data-nav-icon': 'comment' } },
      ] },
      { label: 'Build integrations', collapsed: true, items: [
        { label: 'SDKs', collapsed: true, items: [
          { label: 'SDK overview', slug: 'sdk/overview', attrs: { 'data-nav-icon': 'information' } },
          { label: 'Go SDK', slug: 'sdk/go', attrs: { 'data-nav-icon': 'link' } },
          { label: 'Python SDK', slug: 'sdk/python', attrs: { 'data-nav-icon': 'link' } },
          { label: 'JavaScript/TypeScript SDK', slug: 'sdk/typescript', attrs: { 'data-nav-icon': 'link' } },
          { label: 'Rust SDK', slug: 'sdk/rust', attrs: { 'data-nav-icon': 'link' } },
        ] },
        { label: 'VS Code client', slug: 'guides/vscode', attrs: { 'data-nav-icon': 'link' } },
        { label: 'LangGraph discovery assistant', slug: 'guides/langgraph-agent', attrs: { 'data-nav-icon': 'link' } },
        { label: 'Rust Catalog report', slug: 'guides/rust-catalog-report', attrs: { 'data-nav-icon': 'link' } },
        { label: 'Go webhook publication report', slug: 'guides/go-publication-report', attrs: { 'data-nav-icon': 'link' } },
        { label: 'TypeScript published discovery', slug: 'guides/typescript-ard-list', attrs: { 'data-nav-icon': 'link' } },
        { label: 'Catalog MCP server', slug: 'guides/mcp-server', attrs: { 'data-nav-icon': 'link' } },
        { label: 'Connect with ContextForge', slug: 'guides/contextforge', attrs: { 'data-nav-icon': 'link' } },
        { label: 'Scan MCP tool definitions', slug: 'guides/mcp-scanner', attrs: { 'data-nav-icon': 'link' } },
        { label: 'Integrate a Catalog client', slug: 'guides/integrating', attrs: { 'data-nav-icon': 'link' } },
        { label: 'Discover resources with an assistant', slug: 'guides/assistant-discovery', attrs: { 'data-nav-icon': 'link' } },
        { label: 'Standards and interoperability', slug: 'reference/standards', attrs: { 'data-nav-icon': 'open-book' } },
        { label: 'Pinned standards profiles', slug: 'reference/profiles', attrs: { 'data-nav-icon': 'open-book' } },
      ] },
      { label: 'Deploy and operate', collapsed: true, items: [
        { label: 'Operations overview', slug: 'operations/overview', attrs: { 'data-nav-icon': 'information' } },
        { label: 'Two-Catalog federation tutorial', slug: 'guides/two-catalog-federation', attrs: { 'data-nav-icon': 'code-branch' } },
        { label: 'Connect and manage federation sources', slug: 'operations/federation', attrs: { 'data-nav-icon': 'code-branch' } },
        { label: 'Install and configure', collapsed: true, items: [
          { label: 'Installation, upgrades and credential rotation', slug: 'operations/maintenance', attrs: { 'data-nav-icon': 'map' } },
          { label: 'Deploy the service', slug: 'operations/deployment', attrs: { 'data-nav-icon': 'map' } },
          { label: 'Optional Vault secret delivery', slug: 'operations/vault', attrs: { 'data-nav-icon': 'padlock' } },
          { label: 'Run with Docker and PostgreSQL', slug: 'operations/docker', attrs: { 'data-nav-icon': 'map' } },
          { label: 'Managed container platforms', collapsed: true, items: [
            { label: 'IBM Cloud Code Engine', slug: 'operations/ibm-code-engine', attrs: { 'data-nav-icon': 'map' } },
            { label: 'Azure Container Apps', slug: 'operations/azure-container-apps', attrs: { 'data-nav-icon': 'map' } },
            { label: 'Google Cloud Run', slug: 'operations/google-cloud-run', attrs: { 'data-nav-icon': 'map' } },
          ] },
          { label: 'Database roles and identity setup', slug: 'operations/database-roles', attrs: { 'data-nav-icon': 'database' } },
          { label: 'UI host configuration', slug: 'reference/ui-host-configuration', attrs: { 'data-nav-icon': 'setting' } },
          { label: 'Configuration reference', slug: 'reference/configuration', attrs: { 'data-nav-icon': 'setting' } },
          { label: 'Helm chart and values', slug: 'reference/helm', attrs: { 'data-nav-icon': 'map' } },
        ] },
        { label: 'Authentication and sign-in', collapsed: true, items: [
          { label: 'Authentication setup', slug: 'operations/sign-in', attrs: { 'data-nav-icon': 'padlock' } },
          { label: 'Keycloak', slug: 'operations/oidc-keycloak', attrs: { 'data-nav-icon': 'padlock' } },
          { label: 'Microsoft Entra ID', slug: 'operations/oidc-entra', attrs: { 'data-nav-icon': 'padlock' } },
          { label: 'Auth0', slug: 'operations/oidc-auth0', attrs: { 'data-nav-icon': 'padlock' } },
          { label: 'Okta', slug: 'operations/oidc-okta', attrs: { 'data-nav-icon': 'padlock' } },
          { label: 'IBM Security Verify', slug: 'operations/oidc-ibm-security-verify', attrs: { 'data-nav-icon': 'padlock' } },
          { label: 'Amazon Cognito', slug: 'operations/oidc-cognito', attrs: { 'data-nav-icon': 'padlock' } },
        ] },
        { label: 'Monitor and troubleshoot', collapsed: true, items: [
          { label: 'Logs, metrics, traces and worker health', slug: 'operations/observability', attrs: { 'data-nav-icon': 'analytics' } },
          { label: 'Monitoring with Prometheus and Grafana', slug: 'operations/helm-monitoring', attrs: { 'data-nav-icon': 'analytics' } },
          { label: 'Troubleshooting', slug: 'operations/troubleshooting', attrs: { 'data-nav-icon': 'analytics' } },
        ] },
        { label: 'Optional capabilities', collapsed: true, items: [
          { label: 'Hybrid search', slug: 'operations/hybrid-search', attrs: { 'data-nav-icon': 'analytics' } },
          { label: 'Webhook operations', slug: 'operations/webhooks', attrs: { 'data-nav-icon': 'link' } },
          { label: 'Webhook receiver example', slug: 'reference/receiver-example', attrs: { 'data-nav-icon': 'link' } },
        ] },
        { label: 'Secure and maintain', collapsed: true, items: [
          { label: 'Security operations', slug: 'operations/security', attrs: { 'data-nav-icon': 'padlock' } },
          { label: 'Audit investigation and export', slug: 'operations/audit', attrs: { 'data-nav-icon': 'list-format' } },
          { label: 'Housekeeping and retention', slug: 'operations/housekeeping', attrs: { 'data-nav-icon': 'setting' } },
        ] },
        { label: 'Recover', collapsed: true, items: [
          { label: 'Backup and restore', slug: 'operations/backup-restore', attrs: { 'data-nav-icon': 'database' } },
          { label: 'Idempotency repair', slug: 'operations/idempotency-repair', attrs: { 'data-nav-icon': 'setting' } },
          { label: 'Search projection repair', slug: 'operations/search-projection-repair', attrs: { 'data-nav-icon': 'setting' } },
          { label: 'Incident response', slug: 'operations/incident-response', attrs: { 'data-nav-icon': 'padlock' } },
        ] },
      ] },
      { label: 'Architecture', collapsed: true, items: [
        { label: 'System context', slug: 'architecture/overview', attrs: { 'data-nav-icon': 'information' } },
        { label: 'Design principles', slug: 'architecture/design', attrs: { 'data-nav-icon': 'open-book' } },
        { label: 'Core design', collapsed: true, items: [
          { slug: 'architecture/domain', attrs: { 'data-nav-icon': 'code-branch' } },
          { slug: 'architecture/components', attrs: { 'data-nav-icon': 'code-branch' } },
          { slug: 'architecture/persistence', attrs: { 'data-nav-icon': 'database' } },
        ] },
        { label: 'Runtime and security', collapsed: true, items: [
          { slug: 'architecture/deployment', attrs: { 'data-nav-icon': 'code-branch' } },
          { slug: 'architecture/api-surfaces', attrs: { 'data-nav-icon': 'link' } },
          { slug: 'architecture/security', attrs: { 'data-nav-icon': 'padlock' } },
          { slug: 'architecture/catalog-ui', attrs: { 'data-nav-icon': 'window' } },
          { slug: 'architecture/operations', attrs: { 'data-nav-icon': 'analytics' } },
        ] },
        { label: 'Publication and federation', collapsed: true, items: [
          { slug: 'architecture/publication', attrs: { 'data-nav-icon': 'approve-check-circle' } },
          { slug: 'architecture/federation', attrs: { 'data-nav-icon': 'code-branch' } },
        ] },
        { label: 'Architecture decisions', collapsed: true, items: [
          { autogenerate: { directory: 'architecture/decisions', attrs: { 'data-nav-icon': 'document' } } },
        ] },
      ] },
      { label: 'Develop and contribute', collapsed: true, items: [
        { label: 'Contribution workflow', slug: 'contributing/workflow', attrs: { 'data-nav-icon': 'code-branch' } },
        { label: 'Make targets and development tools', slug: 'contributing/make-targets', attrs: { 'data-nav-icon': 'setting' } },
        { label: 'Build and release', slug: 'contributing/build-and-release', attrs: { 'data-nav-icon': 'approve-check-circle' } },
        { label: 'Testing overview', slug: 'contributing/testing-map', attrs: { 'data-nav-icon': 'list-format' } },
        { label: 'Tests and quality checks', slug: 'contributing/testing', attrs: { 'data-nav-icon': 'list-format' } },
        { label: 'Performance testing and diagnostics', slug: 'guides/performance-and-monitoring', attrs: { 'data-nav-icon': 'analytics' } },
        { label: 'Discovery quality benchmark', slug: 'guides/discovery-quality', attrs: { 'data-nav-icon': 'analytics' } },
        { label: 'Dependency and tooling policy', slug: 'reference/dependencies', attrs: { 'data-nav-icon': 'setting' } },
        { label: 'Maintain the documentation', slug: 'contributing/documentation', attrs: { 'data-nav-icon': 'pen' } },
        { label: 'Report a vulnerability', slug: 'reference/reporting-security', attrs: { 'data-nav-icon': 'padlock' } },
      ] },
      { label: 'Reference', collapsed: true, items: [
        { label: 'CLI reference', slug: 'reference/commands', attrs: { 'data-nav-icon': 'setting' } },
        { label: 'API conventions', slug: 'reference/api-conventions', attrs: { 'data-nav-icon': 'link' } },
        { label: 'Contracts and examples', slug: 'reference/api-assets', attrs: { 'data-nav-icon': 'document' } },
        ...openAPISidebarGroups,
      ] },
    ],
    plugins:[starlightOpenAPI([{schema:'../api/openapi/control-v1.yaml',base:'api',sidebar:{label:'HTTP API',collapsed:true,operations:{badges:true}}}])],
  }), openAPIAnchors(), contentFocus()],
});

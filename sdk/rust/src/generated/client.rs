//! Generated operations from the complete Control OpenAPI.
//! Regenerate with make sdk-generate SDK_LANGUAGE=rust; do not edit.
#![allow(
    clippy::format_in_format_args,
    clippy::let_unit_value,
    clippy::too_many_arguments
)]
use super::types::*;
use crate::{ApiError, ApiOpError, Client, Error as HttpError, Response};
fn __pct_encode_path_segment(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for &b in s.as_bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => {
                out.push(b as char);
            }
            _ => {
                out.push('%');
                out.push_str(&format!("{:02X}", b));
            }
        }
    }
    out
}
///Allowed values for the `audience` query parameter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub enum GetRatingSharingAudience {
    #[serde(rename = "anonymous")]
    Anonymous,
    #[serde(rename = "authenticated")]
    Authenticated,
}
impl GetRatingSharingAudience {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Anonymous => "anonymous",
            Self::Authenticated => "authenticated",
        }
    }
}
impl std::fmt::Display for GetRatingSharingAudience {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
impl AsRef<str> for GetRatingSharingAudience {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}
///Allowed values for the `state` query parameter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub enum ListAccessGroupsState {
    #[serde(rename = "active")]
    Active,
    #[serde(rename = "suspended")]
    Suspended,
    #[serde(rename = "archived")]
    Archived,
}
impl ListAccessGroupsState {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Active => "active",
            Self::Suspended => "suspended",
            Self::Archived => "archived",
        }
    }
}
impl std::fmt::Display for ListAccessGroupsState {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
impl AsRef<str> for ListAccessGroupsState {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}
///Allowed values for the `status` query parameter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub enum ListFederationSourcesStatus {
    #[serde(rename = "active")]
    Active,
    #[serde(rename = "paused")]
    Paused,
    #[serde(rename = "error")]
    Error,
}
impl ListFederationSourcesStatus {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Active => "active",
            Self::Paused => "paused",
            Self::Error => "error",
        }
    }
}
impl std::fmt::Display for ListFederationSourcesStatus {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
impl AsRef<str> for ListFederationSourcesStatus {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}
///Allowed values for the `state` query parameter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub enum ListJobsState {
    #[serde(rename = "queued")]
    Queued,
    #[serde(rename = "running")]
    Running,
    #[serde(rename = "waiting")]
    Waiting,
    #[serde(rename = "succeeded")]
    Succeeded,
    #[serde(rename = "failed")]
    Failed,
    #[serde(rename = "cancelled")]
    Cancelled,
    #[serde(rename = "dead_letter")]
    DeadLetter,
}
impl ListJobsState {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Queued => "queued",
            Self::Running => "running",
            Self::Waiting => "waiting",
            Self::Succeeded => "succeeded",
            Self::Failed => "failed",
            Self::Cancelled => "cancelled",
            Self::DeadLetter => "dead_letter",
        }
    }
}
impl std::fmt::Display for ListJobsState {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
impl AsRef<str> for ListJobsState {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}
///Allowed values for the `state` query parameter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub enum ListMembershipsState {
    #[serde(rename = "active")]
    Active,
    #[serde(rename = "suspended")]
    Suspended,
    #[serde(rename = "offboarded")]
    Offboarded,
}
impl ListMembershipsState {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Active => "active",
            Self::Suspended => "suspended",
            Self::Offboarded => "offboarded",
        }
    }
}
impl std::fmt::Display for ListMembershipsState {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
impl AsRef<str> for ListMembershipsState {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}
///Allowed values for the `freshness` query parameter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub enum ListObservationsFreshness {
    #[serde(rename = "current")]
    Current,
    #[serde(rename = "all")]
    All,
}
impl ListObservationsFreshness {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Current => "current",
            Self::All => "all",
        }
    }
}
impl std::fmt::Display for ListObservationsFreshness {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
impl AsRef<str> for ListObservationsFreshness {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}
///Allowed values for the `state` query parameter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub enum ListOwnRatingsState {
    #[serde(rename = "active")]
    Active,
    #[serde(rename = "withdrawn")]
    Withdrawn,
    #[serde(rename = "removed")]
    Removed,
}
impl ListOwnRatingsState {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Active => "active",
            Self::Withdrawn => "withdrawn",
            Self::Removed => "removed",
        }
    }
}
impl std::fmt::Display for ListOwnRatingsState {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
impl AsRef<str> for ListOwnRatingsState {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}
///Allowed values for the `type` query parameter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub enum ListPersonalItemsType {
    #[serde(rename = "resource")]
    Resource,
    #[serde(rename = "search")]
    Search,
}
impl ListPersonalItemsType {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Resource => "resource",
            Self::Search => "search",
        }
    }
}
impl std::fmt::Display for ListPersonalItemsType {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
impl AsRef<str> for ListPersonalItemsType {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}
///Allowed values for the `queue` query parameter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub enum ListPersonalWorkQueue {
    #[serde(rename = "drafts")]
    Drafts,
    #[serde(rename = "submitted")]
    Submitted,
    #[serde(rename = "changes_required")]
    ChangesRequired,
    #[serde(rename = "approved")]
    Approved,
}
impl ListPersonalWorkQueue {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Drafts => "drafts",
            Self::Submitted => "submitted",
            Self::ChangesRequired => "changes_required",
            Self::Approved => "approved",
        }
    }
}
impl std::fmt::Display for ListPersonalWorkQueue {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
impl AsRef<str> for ListPersonalWorkQueue {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}
///Allowed values for the `state` query parameter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub enum ListPublicationSnapshotsState {
    #[serde(rename = "candidate")]
    Candidate,
    #[serde(rename = "active")]
    Active,
    #[serde(rename = "retired")]
    Retired,
}
impl ListPublicationSnapshotsState {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Candidate => "candidate",
            Self::Active => "active",
            Self::Retired => "retired",
        }
    }
}
impl std::fmt::Display for ListPublicationSnapshotsState {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
impl AsRef<str> for ListPublicationSnapshotsState {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}
///Allowed values for the `state` query parameter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub enum ListRatingInboxState {
    #[serde(rename = "active")]
    Active,
    #[serde(rename = "withdrawn")]
    Withdrawn,
    #[serde(rename = "removed")]
    Removed,
}
impl ListRatingInboxState {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Active => "active",
            Self::Withdrawn => "withdrawn",
            Self::Removed => "removed",
        }
    }
}
impl std::fmt::Display for ListRatingInboxState {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
impl AsRef<str> for ListRatingInboxState {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}
///Allowed values for the `state` query parameter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub enum ListReviewSubmissionsState {
    #[serde(rename = "submitted")]
    Submitted,
    #[serde(rename = "approved")]
    Approved,
    #[serde(rename = "changes_required")]
    ChangesRequired,
    #[serde(rename = "rejected")]
    Rejected,
}
impl ListReviewSubmissionsState {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Submitted => "submitted",
            Self::Approved => "approved",
            Self::ChangesRequired => "changes_required",
            Self::Rejected => "rejected",
        }
    }
}
impl std::fmt::Display for ListReviewSubmissionsState {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
impl AsRef<str> for ListReviewSubmissionsState {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}
///Allowed values for the `state` query parameter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub enum ListRoleDefinitionsState {
    #[serde(rename = "active")]
    Active,
    #[serde(rename = "archived")]
    Archived,
}
impl ListRoleDefinitionsState {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Active => "active",
            Self::Archived => "archived",
        }
    }
}
impl std::fmt::Display for ListRoleDefinitionsState {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
impl AsRef<str> for ListRoleDefinitionsState {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}
///Allowed values for the `state` query parameter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub enum ListWebhookDeliveriesState {
    #[serde(rename = "pending")]
    Pending,
    #[serde(rename = "succeeded")]
    Succeeded,
    #[serde(rename = "waiting")]
    Waiting,
    #[serde(rename = "dead_letter")]
    DeadLetter,
}
impl ListWebhookDeliveriesState {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Pending => "pending",
            Self::Succeeded => "succeeded",
            Self::Waiting => "waiting",
            Self::DeadLetter => "dead_letter",
        }
    }
}
impl std::fmt::Display for ListWebhookDeliveriesState {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
impl AsRef<str> for ListWebhookDeliveriesState {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}
///Allowed values for the `status` query parameter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
pub enum ListWebhooksStatus {
    #[serde(rename = "active")]
    Active,
    #[serde(rename = "disabled")]
    Disabled,
}
impl ListWebhooksStatus {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Active => "active",
            Self::Disabled => "disabled",
        }
    }
}
impl std::fmt::Display for ListWebhooksStatus {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}
impl AsRef<str> for ListWebhooksStatus {
    fn as_ref(&self) -> &str {
        self.as_str()
    }
}
///Typed error responses for `activatePublicationSnapshot`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ActivatePublicationSnapshotApiError {
    Default(Problem),
}
///Typed error responses for `addAccessGroupMember`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum AddAccessGroupMemberApiError {
    Default(Problem),
}
///Typed error responses for `addressFeedback`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum AddressFeedbackApiError {
    Default(Problem),
}
///Typed error responses for `adoptObservation`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum AdoptObservationApiError {
    Default(Problem),
}
///Typed error responses for `approvePublisherPublicationDomain`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ApprovePublisherPublicationDomainApiError {
    Default(Problem),
}
///Typed error responses for `approveReviewSubmission`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ApproveReviewSubmissionApiError {
    Default(Problem),
}
///Typed error responses for `archiveAccessGroup`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ArchiveAccessGroupApiError {
    Default(Problem),
}
///Typed error responses for `archiveRoleDefinition`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ArchiveRoleDefinitionApiError {
    Default(Problem),
}
///Typed error responses for `browseCatalog`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum BrowseCatalogApiError {
    Default(Problem),
}
///Typed error responses for `buildPublicationSnapshot`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum BuildPublicationSnapshotApiError {
    Default(Problem),
}
///Typed error responses for `callCatalogMCP`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CallCatalogMcpApiError {
    Status400(MCPReply),
    Status401(MCPReply),
    Status403(MCPReply),
    Status404(MCPReply),
    Status413(MCPReply),
    Status415(MCPReply),
    Status500(MCPReply),
    Status503(MCPReply),
}
///Typed error responses for `cancelJob`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CancelJobApiError {
    Default(Problem),
}
///Typed error responses for `checkCurrentPermissions`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CheckCurrentPermissionsApiError {
    Default(Problem),
}
///Typed error responses for `checkPublisherDomainVerification`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CheckPublisherDomainVerificationApiError {
    Default(Problem),
}
///Typed error responses for `clearPublisherDomainVerification`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ClearPublisherDomainVerificationApiError {
    Default(Problem),
}
///Typed error responses for `connectFederationSource`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ConnectFederationSourceApiError {
    Default(Problem),
}
///Typed error responses for `createAccessGroup`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CreateAccessGroupApiError {
    Default(Problem),
}
///Typed error responses for `createCatalogListing`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CreateCatalogListingApiError {
    Default(Problem),
}
///Typed error responses for `createFeedback`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CreateFeedbackApiError {
    Default(Problem),
}
///Typed error responses for `createListingRevision`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CreateListingRevisionApiError {
    Default(Problem),
}
///Typed error responses for `createNamespace`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CreateNamespaceApiError {
    Default(Problem),
}
///Typed error responses for `createPersonalItem`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CreatePersonalItemApiError {
    Default(Problem),
}
///Typed error responses for `createPublisher`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CreatePublisherApiError {
    Default(Problem),
}
///Typed error responses for `createResource`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CreateResourceApiError {
    Default(Problem),
}
///Typed error responses for `createResourceVersion`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CreateResourceVersionApiError {
    Default(Problem),
}
///Typed error responses for `createRoleDefinition`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CreateRoleDefinitionApiError {
    Default(Problem),
}
///Typed error responses for `createVersionRevision`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CreateVersionRevisionApiError {
    Default(Problem),
}
///Typed error responses for `createWebhook`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum CreateWebhookApiError {
    Default(Problem),
}
///Typed error responses for `deletePersonalItem`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum DeletePersonalItemApiError {
    Default(Problem),
}
///Typed error responses for `deleteResource`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum DeleteResourceApiError {
    Default(Problem),
}
///Typed error responses for `deprecateListingRelease`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum DeprecateListingReleaseApiError {
    Default(Problem),
}
///Typed error responses for `disableWebhook`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum DisableWebhookApiError {
    Default(Problem),
}
///Typed error responses for `editFeedback`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum EditFeedbackApiError {
    Default(Problem),
}
///Typed error responses for `exploreARDRegistry`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ExploreArdRegistryApiError {
    Default(ARDError),
}
///Typed error responses for `exploreCatalog`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ExploreCatalogApiError {
    Default(Problem),
}
///Typed error responses for `exportAuditEvents`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ExportAuditEventsApiError {
    Default(Problem),
}
///Typed error responses for `exportVersionRevisionDraft`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ExportVersionRevisionDraftApiError {
    Default(Problem),
}
///Typed error responses for `getAICatalog`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetAiCatalogApiError {
    Status401(ARDError),
    Status403(ARDError),
    Status404(ARDError),
    Status503(ARDError),
}
///Typed error responses for `getARDManifest`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetArdManifestApiError {
    Status401(ARDError),
    Status403(ARDError),
    Status404(ARDError),
    Status503(ARDError),
}
///Typed error responses for `getAccessGroup`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetAccessGroupApiError {
    Default(Problem),
}
///Typed error responses for `getAdministrationOverview`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetAdministrationOverviewApiError {
    Default(Problem),
}
///Typed error responses for `getArtifactContent`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetArtifactContentApiError {
    Default(Problem),
}
///Typed error responses for `getArtworkOverride`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetArtworkOverrideApiError {
    Default(Problem),
}
///Typed error responses for `getCapabilities`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetCapabilitiesApiError {
    Default(Problem),
}
///Typed error responses for `getCatalogListing`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetCatalogListingApiError {
    Default(Problem),
}
///Typed error responses for `getCatalogMCPOAuthMetadata`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetCatalogMcpoAuthMetadataApiError {
    Status400(MCPReply),
    Status403(MCPReply),
    Status404(MCPReply),
}
///Typed error responses for `getCatalogRecord`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetCatalogRecordApiError {
    Default(Problem),
}
///Typed error responses for `getCurrentAccess`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetCurrentAccessApiError {
    Default(Problem),
}
///Typed error responses for `getFederationSource`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetFederationSourceApiError {
    Default(Problem),
}
///Typed error responses for `getFederationSourceInspection`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetFederationSourceInspectionApiError {
    Default(Problem),
}
///Typed error responses for `getFederationSourceSnapshot`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetFederationSourceSnapshotApiError {
    Default(Problem),
}
///Typed error responses for `getFeedback`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetFeedbackApiError {
    Default(Problem),
}
///Typed error responses for `getFeedbackOverview`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetFeedbackOverviewApiError {
    Default(Problem),
}
///Typed error responses for `getIndexedCatalogRecord`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetIndexedCatalogRecordApiError {
    Default(Problem),
}
///Typed error responses for `getIndexedOverview`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetIndexedOverviewApiError {
    Default(Problem),
}
///Typed error responses for `getJob`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetJobApiError {
    Default(Problem),
}
///Typed error responses for `getJobOverview`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetJobOverviewApiError {
    Default(Problem),
}
///Typed error responses for `getListingRelease`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetListingReleaseApiError {
    Default(Problem),
}
///Typed error responses for `getListingRevision`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetListingRevisionApiError {
    Default(Problem),
}
///Typed error responses for `getMembership`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetMembershipApiError {
    Default(Problem),
}
///Typed error responses for `getMembershipAccess`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetMembershipAccessApiError {
    Default(Problem),
}
///Typed error responses for `getNamespace`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetNamespaceApiError {
    Default(Problem),
}
///Typed error responses for `getNamespaceOverview`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetNamespaceOverviewApiError {
    Default(Problem),
}
///Typed error responses for `getObservationGroup`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetObservationGroupApiError {
    Default(Problem),
}
///Typed error responses for `getPersonalItem`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetPersonalItemApiError {
    Default(Problem),
}
///Typed error responses for `getPublicArtifact`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetPublicArtifactApiError {
    Default(ARDError),
}
///Typed error responses for `getPublicationOverview`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetPublicationOverviewApiError {
    Default(Problem),
}
///Typed error responses for `getPublicationSnapshot`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetPublicationSnapshotApiError {
    Default(Problem),
}
///Typed error responses for `getPublisher`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetPublisherApiError {
    Default(Problem),
}
///Typed error responses for `getPublisherDomainChallenge`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetPublisherDomainChallengeApiError {
    Default(Problem),
}
///Typed error responses for `getPublisherDomainVerification`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetPublisherDomainVerificationApiError {
    Default(Problem),
}
///Typed error responses for `getPublisherOverview`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetPublisherOverviewApiError {
    Default(Problem),
}
///Typed error responses for `getRating`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetRatingApiError {
    Default(Problem),
}
///Typed error responses for `getRatingOverview`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetRatingOverviewApiError {
    Default(Problem),
}
///Typed error responses for `getRatingSharing`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetRatingSharingApiError {
    Default(Problem),
}
///Typed error responses for `getRawObservation`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetRawObservationApiError {
    Default(Problem),
}
///Typed error responses for `getReadiness`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetReadinessApiError {
    Status503(Problem),
}
///Typed error responses for `getReleaseOverview`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetReleaseOverviewApiError {
    Default(Problem),
}
///Typed error responses for `getResource`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetResourceApiError {
    Default(Problem),
}
///Typed error responses for `getResourceOverview`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetResourceOverviewApiError {
    Default(Problem),
}
///Typed error responses for `getResourceVersion`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetResourceVersionApiError {
    Default(Problem),
}
///Typed error responses for `getReviewOverview`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetReviewOverviewApiError {
    Default(Problem),
}
///Typed error responses for `getReviewSubmission`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetReviewSubmissionApiError {
    Default(Problem),
}
///Typed error responses for `getReviewSubmissionEvidence`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetReviewSubmissionEvidenceApiError {
    Default(Problem),
}
///Typed error responses for `getRoleDefinition`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetRoleDefinitionApiError {
    Default(Problem),
}
///Typed error responses for `getServiceActivity`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetServiceActivityApiError {
    Default(Problem),
}
///Typed error responses for `getServiceInformation`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetServiceInformationApiError {
    Default(Problem),
}
///Typed error responses for `getSourceOverview`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetSourceOverviewApiError {
    Default(Problem),
}
///Typed error responses for `getTenantSettings`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetTenantSettingsApiError {
    Default(Problem),
}
///Typed error responses for `getTrustAssessment`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetTrustAssessmentApiError {
    Status401(Problem),
    Status403(Problem),
    Status404(Problem),
    Status409(Problem),
    Status503(Problem),
}
///Typed error responses for `getVersionRevision`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetVersionRevisionApiError {
    Default(Problem),
}
///Typed error responses for `getWebhook`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetWebhookApiError {
    Default(Problem),
}
///Typed error responses for `getWebhookDeliveryOverview`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetWebhookDeliveryOverviewApiError {
    Default(Problem),
}
///Typed error responses for `getWebhookEndpointOverview`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetWebhookEndpointOverviewApiError {
    Default(Problem),
}
///Typed error responses for `getWebhookReconciliation`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GetWebhookReconciliationApiError {
    Default(Problem),
}
///Typed error responses for `grantAccessAssignment`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GrantAccessAssignmentApiError {
    Default(Problem),
}
///Typed error responses for `grantRoleAssignment`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum GrantRoleAssignmentApiError {
    Default(Problem),
}
///Typed error responses for `headCatalogMCPOAuthMetadata`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum HeadCatalogMcpoAuthMetadataApiError {
    Status403(MCPReply),
    Status404(MCPReply),
}
///Typed error responses for `inspectFederationSource`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum InspectFederationSourceApiError {
    Default(Problem),
}
///Typed error responses for `issuePublisherDomainChallenge`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum IssuePublisherDomainChallengeApiError {
    Default(Problem),
}
///Typed error responses for `listARDRegistryEntries`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListArdRegistryEntriesApiError {
    Default(ARDError),
}
///Typed error responses for `listAccessAssignments`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListAccessAssignmentsApiError {
    Default(Problem),
}
///Typed error responses for `listAccessGroupMembers`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListAccessGroupMembersApiError {
    Default(Problem),
}
///Typed error responses for `listAccessGroups`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListAccessGroupsApiError {
    Default(Problem),
}
///Typed error responses for `listArtworkOverrideHistory`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListArtworkOverrideHistoryApiError {
    Default(Problem),
}
///Typed error responses for `listAuditEvents`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListAuditEventsApiError {
    Default(Problem),
}
///Typed error responses for `listCurrentAccess`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListCurrentAccessApiError {
    Default(Problem),
}
///Typed error responses for `listFederationSourceSnapshots`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListFederationSourceSnapshotsApiError {
    Default(Problem),
}
///Typed error responses for `listFederationSources`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListFederationSourcesApiError {
    Default(Problem),
}
///Typed error responses for `listFeedbackInbox`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListFeedbackInboxApiError {
    Default(Problem),
}
///Typed error responses for `listJobs`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListJobsApiError {
    Default(Problem),
}
///Typed error responses for `listListingReleases`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListListingReleasesApiError {
    Default(Problem),
}
///Typed error responses for `listListingRevisions`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListListingRevisionsApiError {
    Default(Problem),
}
///Typed error responses for `listMemberships`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListMembershipsApiError {
    Default(Problem),
}
///Typed error responses for `listNamespaces`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListNamespacesApiError {
    Default(Problem),
}
///Typed error responses for `listObservationOccurrences`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListObservationOccurrencesApiError {
    Default(Problem),
}
///Typed error responses for `listObservations`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListObservationsApiError {
    Default(Problem),
}
///Typed error responses for `listOwnFeedback`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListOwnFeedbackApiError {
    Default(Problem),
}
///Typed error responses for `listOwnRatings`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListOwnRatingsApiError {
    Default(Problem),
}
///Typed error responses for `listPermissionDescriptors`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListPermissionDescriptorsApiError {
    Default(Problem),
}
///Typed error responses for `listPersonalItems`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListPersonalItemsApiError {
    Default(Problem),
}
///Typed error responses for `listPersonalWork`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListPersonalWorkApiError {
    Default(Problem),
}
///Typed error responses for `listPublicationHosts`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListPublicationHostsApiError {
    Default(Problem),
}
///Typed error responses for `listPublicationSnapshots`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListPublicationSnapshotsApiError {
    Default(Problem),
}
///Typed error responses for `listPublishers`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListPublishersApiError {
    Default(Problem),
}
///Typed error responses for `listRatingInbox`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListRatingInboxApiError {
    Default(Problem),
}
///Typed error responses for `listResourceListings`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListResourceListingsApiError {
    Default(Problem),
}
///Typed error responses for `listResourceVersions`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListResourceVersionsApiError {
    Default(Problem),
}
///Typed error responses for `listResources`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListResourcesApiError {
    Default(Problem),
}
///Typed error responses for `listReviewSubmissions`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListReviewSubmissionsApiError {
    Default(Problem),
}
///Typed error responses for `listRoleAssignments`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListRoleAssignmentsApiError {
    Default(Problem),
}
///Typed error responses for `listRoleDefinitions`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListRoleDefinitionsApiError {
    Default(Problem),
}
///Typed error responses for `listRoleImpact`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListRoleImpactApiError {
    Default(Problem),
}
///Typed error responses for `listVersionRevisions`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListVersionRevisionsApiError {
    Default(Problem),
}
///Typed error responses for `listWebhookDeliveries`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListWebhookDeliveriesApiError {
    Default(Problem),
}
///Typed error responses for `listWebhooks`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ListWebhooksApiError {
    Default(Problem),
}
///Typed error responses for `liveFederatedSearch`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum LiveFederatedSearchApiError {
    Default(Problem),
}
///Typed error responses for `offboardMembership`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum OffboardMembershipApiError {
    Default(Problem),
}
///Typed error responses for `pauseFederationSource`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum PauseFederationSourceApiError {
    Default(Problem),
}
///Typed error responses for `permitRating`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum PermitRatingApiError {
    Default(Problem),
}
///Typed error responses for `preflightCatalogMCP`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum PreflightCatalogMcpApiError {
    Status400(MCPReply),
    Status403(MCPReply),
    Status404(Problem),
}
///Typed error responses for `preflightCatalogMCPOAuthMetadata`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum PreflightCatalogMcpoAuthMetadataApiError {
    Status400(MCPReply),
    Status403(MCPReply),
    Status404(Problem),
}
///Typed error responses for `previewFederationSourceChange`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum PreviewFederationSourceChangeApiError {
    Default(Problem),
}
///Typed error responses for `publishCatalogListing`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum PublishCatalogListingApiError {
    Default(Problem),
}
///Typed error responses for `reactivateAccessGroup`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ReactivateAccessGroupApiError {
    Default(Problem),
}
///Typed error responses for `reactivateMembership`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ReactivateMembershipApiError {
    Default(Problem),
}
///Typed error responses for `readArtwork`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ReadArtworkApiError {
    Default(Problem),
}
///Typed error responses for `readRating`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ReadRatingApiError {
    Default(Problem),
}
///Typed error responses for `refuseCatalogMCPSessionDeletion`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RefuseCatalogMcpSessionDeletionApiError {
    Status400(MCPReply),
    Status403(MCPReply),
    Status404(Problem),
    Status405(MCPReply),
}
///Typed error responses for `refuseCatalogMCPStream`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RefuseCatalogMcpStreamApiError {
    Status403(MCPReply),
    Status404(Problem),
    Status405(MCPReply),
}
///Typed error responses for `reindexCatalog`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ReindexCatalogApiError {
    Default(Problem),
}
///Typed error responses for `rejectReviewSubmission`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RejectReviewSubmissionApiError {
    Default(Problem),
}
///Typed error responses for `removeFederationSource`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RemoveFederationSourceApiError {
    Default(Problem),
}
///Typed error responses for `removeFeedback`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RemoveFeedbackApiError {
    Default(Problem),
}
///Typed error responses for `removeRating`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RemoveRatingApiError {
    Default(Problem),
}
///Typed error responses for `reopenFeedback`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ReopenFeedbackApiError {
    Default(Problem),
}
///Typed error responses for `requestReviewChanges`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RequestReviewChangesApiError {
    Default(Problem),
}
///Typed error responses for `requestTrustAssessment`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RequestTrustAssessmentApiError {
    Status400(Problem),
    Status401(Problem),
    Status403(Problem),
    Status404(Problem),
    Status409(Problem),
    Status412(Problem),
    Status503(Problem),
}
///Typed error responses for `resolvePersonalItem`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ResolvePersonalItemApiError {
    Default(Problem),
}
///Typed error responses for `resumeFederationSource`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ResumeFederationSourceApiError {
    Default(Problem),
}
///Typed error responses for `retireListingRelease`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RetireListingReleaseApiError {
    Default(Problem),
}
///Typed error responses for `retryJob`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RetryJobApiError {
    Default(Problem),
}
///Typed error responses for `revokeAccessAssignment`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RevokeAccessAssignmentApiError {
    Default(Problem),
}
///Typed error responses for `revokeAccessGroupMember`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RevokeAccessGroupMemberApiError {
    Default(Problem),
}
///Typed error responses for `revokeListingRelease`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RevokeListingReleaseApiError {
    Default(Problem),
}
///Typed error responses for `revokePublisherPublicationDomain`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RevokePublisherPublicationDomainApiError {
    Default(Problem),
}
///Typed error responses for `revokeRoleAssignment`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RevokeRoleAssignmentApiError {
    Default(Problem),
}
///Typed error responses for `rollbackPublicationSnapshot`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RollbackPublicationSnapshotApiError {
    Default(Problem),
}
///Typed error responses for `rotateWebhookSecret`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum RotateWebhookSecretApiError {
    Default(Problem),
}
///Typed error responses for `searchARDRegistry`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum SearchArdRegistryApiError {
    Default(ARDError),
}
///Typed error responses for `searchCatalog`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum SearchCatalogApiError {
    Default(Problem),
}
///Typed error responses for `searchObservations`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum SearchObservationsApiError {
    Default(Problem),
}
///Typed error responses for `setArtworkOverride`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum SetArtworkOverrideApiError {
    Default(Problem),
}
///Typed error responses for `setRatingSharing`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum SetRatingSharingApiError {
    Default(Problem),
}
///Typed error responses for `setTenantSettings`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum SetTenantSettingsApiError {
    Default(Problem),
}
///Typed error responses for `submitListingForReview`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum SubmitListingForReviewApiError {
    Default(Problem),
}
///Typed error responses for `submitRating`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum SubmitRatingApiError {
    Default(Problem),
}
///Typed error responses for `supersedeListingRelease`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum SupersedeListingReleaseApiError {
    Default(Problem),
}
///Typed error responses for `suspendAccessGroup`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum SuspendAccessGroupApiError {
    Default(Problem),
}
///Typed error responses for `suspendMembership`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum SuspendMembershipApiError {
    Default(Problem),
}
///Typed error responses for `syncFederationSource`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum SyncFederationSourceApiError {
    Default(Problem),
}
///Typed error responses for `testFederationSource`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum TestFederationSourceApiError {
    Default(Problem),
}
///Typed error responses for `testWebhook`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum TestWebhookApiError {
    Status503(Problem),
    Default(Problem),
}
///Typed error responses for `unpublishCatalog`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum UnpublishCatalogApiError {
    Default(Problem),
}
///Typed error responses for `updateAccessGroup`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum UpdateAccessGroupApiError {
    Default(Problem),
}
///Typed error responses for `updateFederationSource`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum UpdateFederationSourceApiError {
    Default(Problem),
}
///Typed error responses for `updateNamespace`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum UpdateNamespaceApiError {
    Default(Problem),
}
///Typed error responses for `updatePersonalItem`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum UpdatePersonalItemApiError {
    Default(Problem),
}
///Typed error responses for `updatePublisher`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum UpdatePublisherApiError {
    Default(Problem),
}
///Typed error responses for `updateRoleDefinition`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum UpdateRoleDefinitionApiError {
    Default(Problem),
}
///Typed error responses for `updateWebhook`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum UpdateWebhookApiError {
    Default(Problem),
}
///Typed error responses for `validateVersionRevision`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum ValidateVersionRevisionApiError {
    Default(Problem),
}
///Typed error responses for `withdrawFeedback`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum WithdrawFeedbackApiError {
    Default(Problem),
}
///Typed error responses for `withdrawRating`. One variant per declared non-2xx response.
#[derive(Debug, Clone)]
pub enum WithdrawRatingApiError {
    Default(Problem),
}
impl Client {
    /// Activate a publication snapshot
    ///
    /// Activates an eligible candidate for its exact configured host after checking the candidate ETag and retained dependencies.
    ///
    /// `POST /api/v1/publication/snapshots/{snapshotId}:activate`
    pub async fn activate_publication_snapshot(
        &self,
        snapshot_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: PublicationTransition,
    ) -> Result<Response<PublicationSnapshot>, ApiOpError<ActivatePublicationSnapshotApiError>>
    {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/publication/snapshots/{}:activate",
                __pct_encode_path_segment(snapshot_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ActivatePublicationSnapshotApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ActivatePublicationSnapshotApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Add a same-realm direct group member
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Assignment grants, revocations and group-member writes stay within the same identity realm; membership inventory and lifecycle are tenant-wide. Built-in roles are immutable. Custom roles cannot grant access.manage. Writes and private audit evidence commit atomically. Public notifications do not expose the permission graph.
    ///
    /// `POST /api/v1/access/groups/{groupId}/members`
    pub async fn add_access_group_member(
        &self,
        group_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        request: AccessGroupMemberInput,
    ) -> Result<Response<AccessGroupMember>, ApiOpError<AddAccessGroupMemberApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/access/groups/{}/members",
                __pct_encode_path_segment(group_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<AddAccessGroupMemberApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(AddAccessGroupMemberApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Address feedback
    ///
    /// Requires feedback.moderate and tenant.admin scope when enforced. Replaces the optional current response and marks feedback addressed without rewriting the comment. The receipt contains no private text. Read current state separately with GET.
    ///
    /// `POST /api/v1/feedback/{feedbackId}:address`
    pub async fn address_feedback(
        &self,
        feedback_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: FeedbackResponse,
    ) -> Result<Response<FeedbackReceipt>, ApiOpError<AddressFeedbackApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/feedback/{}:address",
                __pct_encode_path_segment(feedback_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<AddressFeedbackApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(AddressFeedbackApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Adopt one exact complete observation occurrence into a new local draft
    ///
    /// Requires tenant-level source.read_evidence and resource.create in the selected namespace, including their required companion reads. Also requires the catalog.write OAuth scope when scope enforcement is enabled. The result is a private-by-default draft, not approval or publication.
    ///
    /// `POST /api/v1/observations/{observationId}:adopt`
    pub async fn adopt_observation(
        &self,
        observation_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        request: AdoptObservation,
    ) -> Result<Response<ObservationAdoption>, ApiOpError<AdoptObservationApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/observations/{}:adopt",
                __pct_encode_path_segment(observation_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<AdoptObservationApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(AdoptObservationApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Approve a publisher domain for publication
    ///
    /// Requires publisher.approve_domain and publishers.manage when OAuth scopes are enforced. Records a reason-bearing approval for a normalized domain already declared by this publisher. When publisherDomainVerification.mode is required_for_approval, fresh stored DNS TXT evidence is required at commit; missing or expired evidence returns PRECONDITION_FAILED. This command performs no DNS lookup. Existing approvals do not expire with their DNS evidence. The approval authorizes the baseline publication workflow only; it does not verify external control, establish publisher authority, or assess legal rights.
    ///
    /// `POST /api/v1/publishers/{publisherId}/publication-domains/{domain}:approve`
    pub async fn approve_publisher_publication_domain(
        &self,
        publisher_id: impl AsRef<str>,
        domain: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<Publisher>, ApiOpError<ApprovePublisherPublicationDomainApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/publishers/{}/publication-domains/{}:approve",
                __pct_encode_path_segment(publisher_id.as_ref()),
                __pct_encode_path_segment(domain.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ApprovePublisherPublicationDomainApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ApprovePublisherPublicationDomainApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Approve a review submission
    ///
    /// Records an approval for the current submission revision after reviewer authorization and separation-of-duties checks. Approval alone does not activate a public snapshot. Independent review resolves immutable submission audit attribution; the originating membership or the same tenant/realm/subject cannot decide. An unrelated equal subject in another realm is eligible with current permissions. Missing or ambiguous attribution refuses the decision.
    ///
    /// `POST /api/v1/review-submissions/{submissionId}:approve`
    pub async fn approve_review_submission(
        &self,
        submission_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<ReviewDecision>, ApiOpError<ApproveReviewSubmissionApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/review-submissions/{}:approve",
                __pct_encode_path_segment(submission_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ApproveReviewSubmissionApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ApproveReviewSubmissionApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Archive group
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Assignment grants, revocations and group-member writes stay within the same identity realm; membership inventory and lifecycle are tenant-wide. Built-in roles are immutable. Custom roles cannot grant access.manage. Writes and private audit evidence commit atomically. Public notifications do not expose the permission graph. Archival requires all active group assignments and member relations to be revoked first; otherwise CONFLICT leaves group state and evidence unchanged. Archived history remains terminal and readable.
    ///
    /// `POST /api/v1/access/groups/{groupId}:archive`
    pub async fn archive_access_group(
        &self,
        group_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<AccessGroup>, ApiOpError<ArchiveAccessGroupApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/access/groups/{}:archive",
                __pct_encode_path_segment(group_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ArchiveAccessGroupApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ArchiveAccessGroupApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Archive role
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Assignment grants, revocations and group-member writes stay within the same identity realm; membership inventory and lifecycle are tenant-wide. Built-in roles are immutable. Custom roles cannot grant access.manage. Writes and private audit evidence commit atomically. Public notifications do not expose the permission graph. Permission updates replace the exact set and require explicit read companions. Revoke incompatible active assignments before a scope change or archival.
    ///
    /// `POST /api/v1/access/roles/{roleId}:archive`
    pub async fn archive_role_definition(
        &self,
        role_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<RoleDefinition>, ApiOpError<ArchiveRoleDefinitionApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/access/roles/{}:archive",
                __pct_encode_path_segment(role_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ArchiveRoleDefinitionApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ArchiveRoleDefinitionApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Browse governed Catalog records
    ///
    /// Returns a bounded page of governed catalog records visible to the caller. Local drafts are not made discoverable through this endpoint.
    ///
    /// `GET /api/v1/catalog/resources`
    pub async fn browse_catalog(
        &self,
        namespace_id: Option<impl AsRef<str>>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
        kind: Option<ResourceKind>,
        visibility: Option<Visibility>,
    ) -> Result<Response<CatalogRecordPage>, ApiOpError<BrowseCatalogApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/catalog/resources");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = namespace_id {
                query_params.push(("namespaceId".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = kind {
                query_params.push(("kind".to_string(), v.to_string()));
            }
            if let Some(v) = visibility {
                query_params.push(("visibility".to_string(), v.to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<BrowseCatalogApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(BrowseCatalogApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Build a publication snapshot candidate
    ///
    /// Builds and returns an immutable candidate for the exact configured discovery host and its audience. The candidate is not public until activated.
    ///
    /// `POST /api/v1/publication/snapshots`
    pub async fn build_publication_snapshot(
        &self,
        idempotency_key: impl AsRef<str>,
        request: PublicationTarget,
    ) -> Result<Response<PublicationSnapshot>, ApiOpError<BuildPublicationSnapshotApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/publication/snapshots");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<BuildPublicationSnapshotApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(BuildPublicationSnapshotApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Read Catalog metadata through stateless MCP
    ///
    /// Available only when mcp.enabled is true. Supports 2026-07-28 per-request metadata and the 2025-11-25 legacy handshake. No sessions, GET stream, execution or mutations. All calls require current Catalog membership. Resource tools require catalog.read when OAuth scope enforcement is enabled; protocol discovery, tool listing, capabilities and self-access do not. Catalog permissions still apply inside each read. Headers must agree with the validated JSON-RPC operation. Duplicate security headers, batches, duplicate JSON keys, invalid Unicode, unsupported methods and over-limit input fail. Bodies have the tighter API limit or 262144 bytes; tool arguments have a 65536-byte limit. A full concurrency gate returns 503 without queuing. Responses are finite JSON and no-store. Tool failures use isError inside a protocol result; OAuth scope denial is HTTP 403.
    ///
    /// `POST /mcp`
    pub async fn call_catalog_mcp(
        &self,
        mcp_protocol_version: Option<impl AsRef<str>>,
        mcp_method: Option<impl AsRef<str>>,
        mcp_name: Option<impl AsRef<str>>,
        origin: Option<impl AsRef<str>>,
        request: MCPRequest,
    ) -> Result<Response<MCPReply>, ApiOpError<CallCatalogMcpApiError>> {
        let request_url = format!("{}{}", self.base_url, "/mcp");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        if let Some(v) = mcp_protocol_version {
            req = req.header("MCP-Protocol-Version", v.as_ref());
        }
        if let Some(v) = mcp_method {
            req = req.header("MCP-Method", v.as_ref());
        }
        if let Some(v) = mcp_name {
            req = req.header("MCP-Name", v.as_ref());
        }
        if let Some(v) = origin {
            req = req.header("Origin", v.as_ref());
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<CallCatalogMcpApiError>;
            let parse_error: Option<String>;
            match status_code {
                400u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CallCatalogMcpApiError::Status400(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                401u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CallCatalogMcpApiError::Status401(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                403u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CallCatalogMcpApiError::Status403(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                404u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CallCatalogMcpApiError::Status404(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                413u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CallCatalogMcpApiError::Status413(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                415u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CallCatalogMcpApiError::Status415(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                500u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CallCatalogMcpApiError::Status500(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                503u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CallCatalogMcpApiError::Status503(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                _ => {
                    typed = None;
                    parse_error = None;
                }
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Cancel an eligible Job
    ///
    /// Requests cancellation of an eligible Job with an audit reason. Cancellation follows Job state and ownership rules; it does not undo already committed effects.
    ///
    /// `POST /api/v1/jobs/{jobId}:cancel`
    pub async fn cancel_job(
        &self,
        job_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<Job>, ApiOpError<CancelJobApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/jobs/{}:cancel",
                __pct_encode_path_segment(job_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<CancelJobApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CancelJobApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Check bounded current Catalog permissions
    ///
    /// Requires a current active membership. This view reports only the caller's Catalog authority. OAuth scope ceilings and operation eligibility remain separate. catalogAllowed follows the input check order. These advisory hints do not admit operations or delegate authority.
    ///
    /// `POST /api/v1/access/capabilities`
    pub async fn check_current_permissions(
        &self,
        request: PermissionChecks,
    ) -> Result<Response<PermissionDecision>, ApiOpError<CheckCurrentPermissionsApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/access/capabilities");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<CheckCurrentPermissionsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CheckCurrentPermissionsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Check the current DNS TXT challenge
    ///
    /// Requires current tenant-level publisher.manage. Requires publishers.manage when OAuth scopes are enforced. Send an Idempotency-Key and the current verification ETag from either verification read in If-Match. Refuses stale, expired, consumed or absent challenges and excess lookup capacity before DNS. Queries DNS once and rechecks current authority before committing evidence. A completed lookup returns 200 with lastCheckOutcome. Only matched establishes proof. An unsuccessful renewal keeps earlier proof until its expiry; read state separately. Replaying the original command returns its retained response without another lookup, including unsuccessful outcomes. Disabled mode returns 404 for a new command. DNS evidence does not approve publication or establish general publisher authority or legal rights.
    ///
    /// `POST /api/v1/publishers/{publisherId}/domains/{domain}/verification:check`
    pub async fn check_publisher_domain_verification(
        &self,
        publisher_id: impl AsRef<str>,
        domain: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<
        Response<PublisherDomainVerification>,
        ApiOpError<CheckPublisherDomainVerificationApiError>,
    > {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/publishers/{}/domains/{}/verification:check",
                __pct_encode_path_segment(publisher_id.as_ref()),
                __pct_encode_path_segment(domain.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<CheckPublisherDomainVerificationApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CheckPublisherDomainVerificationApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Clear publisher DNS verification evidence
    ///
    /// Requires current tenant-level publisher.manage. Requires publishers.manage when OAuth scopes are enforced. Send an Idempotency-Key and the current verification ETag from either verification read in If-Match. Clears the pending challenge and DNS proof without a DNS lookup. Publication approval remains unchanged. Disabled mode returns 404 for a new command. Historical commands replay under current management authority after replacement, clearing or domain removal.
    ///
    /// `POST /api/v1/publishers/{publisherId}/domains/{domain}/verification:clear`
    pub async fn clear_publisher_domain_verification(
        &self,
        publisher_id: impl AsRef<str>,
        domain: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<
        Response<PublisherDomainVerification>,
        ApiOpError<ClearPublisherDomainVerificationApiError>,
    > {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/publishers/{}/domains/{}/verification:clear",
                __pct_encode_path_segment(publisher_id.as_ref()),
                __pct_encode_path_segment(domain.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ClearPublisherDomainVerificationApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ClearPublisherDomainVerificationApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Connect an inspected federation source
    ///
    /// Connects a source using a completed inspection and administrator-selected scope, retrieval and update policy. Credential settings are opaque bindings, not secret values.
    ///
    /// `POST /api/v1/federation/sources`
    pub async fn connect_federation_source(
        &self,
        idempotency_key: impl AsRef<str>,
        request: ConnectSource,
    ) -> Result<Response<FederationSource>, ApiOpError<ConnectFederationSourceApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/federation/sources");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<ConnectFederationSourceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ConnectFederationSourceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Create group
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Assignment grants, revocations and group-member writes stay within the same identity realm; membership inventory and lifecycle are tenant-wide. Built-in roles are immutable. Custom roles cannot grant access.manage. Writes and private audit evidence commit atomically. Public notifications do not expose the permission graph.
    ///
    /// `POST /api/v1/access/groups`
    pub async fn create_access_group(
        &self,
        idempotency_key: impl AsRef<str>,
        request: AccessGroupInput,
    ) -> Result<Response<AccessGroup>, ApiOpError<CreateAccessGroupApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/access/groups");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<CreateAccessGroupApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CreateAccessGroupApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Create a Catalog listing
    ///
    /// Creates a listing identity for a local Resource in an authorized namespace. Presentation, publisher and visibility metadata are supplied through a separate listing revision.
    ///
    /// `POST /api/v1/catalog-listings`
    pub async fn create_catalog_listing(
        &self,
        idempotency_key: impl AsRef<str>,
        request: CreateListing,
    ) -> Result<Response<CatalogListing>, ApiOpError<CreateCatalogListingApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/catalog-listings");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<CreateCatalogListingApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CreateCatalogListingApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Submit private item feedback
    ///
    /// Requires active human membership with feedback.own and feedback.write scope when enforced, plus resource.read in the selected namespace for a new submission. Derives submitter identity and exact release context. All four local kinds qualify; indexed/live observations do not. Text is limited to 4000 Unicode characters and bodies to 64 KiB. Eight concurrent writes per API process are admitted; busy attempts return 503 and can be retried. This is not a shared quota.
    ///
    /// `POST /api/v1/feedback`
    pub async fn create_feedback(
        &self,
        idempotency_key: impl AsRef<str>,
        request: FeedbackCreate,
    ) -> Result<Response<FeedbackReceipt>, ApiOpError<CreateFeedbackApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/feedback");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<CreateFeedbackApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CreateFeedbackApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Create an immutable listing revision
    ///
    /// Adds immutable listing metadata and advances the listing's current revision. Requires the current listing ETag and preserves earlier revisions.
    ///
    /// `POST /api/v1/catalog-listings/{listingId}/revisions`
    pub async fn create_listing_revision(
        &self,
        listing_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: CreateListingRevision,
    ) -> Result<Response<ListingRevisionCreated>, ApiOpError<CreateListingRevisionApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/catalog-listings/{}/revisions",
                __pct_encode_path_segment(listing_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<CreateListingRevisionApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CreateListingRevisionApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Create a namespace
    ///
    /// Creates a namespace with a stable slug and display metadata. The returned revision and ETag identify its initial state.
    ///
    /// `POST /api/v1/namespaces`
    pub async fn create_namespace(
        &self,
        idempotency_key: impl AsRef<str>,
        request: CreateNamespace,
    ) -> Result<Response<Namespace>, ApiOpError<CreateNamespaceApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/namespaces");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<CreateNamespaceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CreateNamespaceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Save a resource or named search
    ///
    /// Requires an active human membership. Requires library.own and exact tenant/realm/membership ownership; administrators cannot select another owner. Owner reads, rename and removal remain available after target access loss. OAuth scope when enforced: personal.write. Responses are no-store. Mutations require Idempotency-Key; updates and removals also require If-Match. Required audit contains identity/revision only; no public notification is created. At most 200 items per owner, labels up to 120 characters, bodies up to 16 KiB.
    ///
    /// `POST /api/v1/personal/items`
    pub async fn create_personal_item(
        &self,
        idempotency_key: impl AsRef<str>,
        request: PersonalInput,
    ) -> Result<Response<PersonalReceipt>, ApiOpError<CreatePersonalItemApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/personal/items");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<CreatePersonalItemApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CreatePersonalItemApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Create a publisher
    ///
    /// Creates a publisher identity and its declared domains. Domain declaration does not approve that publisher for publication on a discovery host.
    ///
    /// `POST /api/v1/publishers`
    pub async fn create_publisher(
        &self,
        idempotency_key: impl AsRef<str>,
        request: CreatePublisher,
    ) -> Result<Response<Publisher>, ApiOpError<CreatePublisherApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/publishers");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<CreatePublisherApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CreatePublisherApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Create a Resource
    ///
    /// Creates a stable local Resource in an authorized namespace. Representations, versions, listings and publication are separate lifecycle steps.
    ///
    /// `POST /api/v1/resources`
    pub async fn create_resource(
        &self,
        idempotency_key: impl AsRef<str>,
        request: CreateResource,
    ) -> Result<Response<Resource>, ApiOpError<CreateResourceApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/resources");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<CreateResourceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CreateResourceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Create a Resource version
    ///
    /// Reserves a version label under the Resource and creates its initial immutable revision from the supplied representations. Returns the version, revision and validation evidence.
    ///
    /// `POST /api/v1/resources/{resourceId}/versions`
    pub async fn create_resource_version(
        &self,
        resource_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        request: CreateVersion,
    ) -> Result<Response<VersionEvidenceEnvelope>, ApiOpError<CreateResourceVersionApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/resources/{}/versions",
                __pct_encode_path_segment(resource_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<CreateResourceVersionApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CreateResourceVersionApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Create role
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Assignment grants, revocations and group-member writes stay within the same identity realm; membership inventory and lifecycle are tenant-wide. Built-in roles are immutable. Custom roles cannot grant access.manage. Writes and private audit evidence commit atomically. Public notifications do not expose the permission graph. Permission updates replace the exact set and require explicit read companions. Revoke incompatible active assignments before a scope change or archival.
    ///
    /// `POST /api/v1/access/roles`
    pub async fn create_role_definition(
        &self,
        idempotency_key: impl AsRef<str>,
        request: RoleDefinitionInput,
    ) -> Result<Response<RoleDefinition>, ApiOpError<CreateRoleDefinitionApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/access/roles");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<CreateRoleDefinitionApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CreateRoleDefinitionApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Create an immutable version revision
    ///
    /// Adds a new immutable representation revision while the version remains editable. Requires the current version ETag; earlier revisions remain retained.
    ///
    /// `POST /api/v1/resource-versions/{versionId}/revisions`
    pub async fn create_version_revision(
        &self,
        version_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: CreateVersionRevisionRequest,
    ) -> Result<Response<VersionEvidenceEnvelope>, ApiOpError<CreateVersionRevisionApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/resource-versions/{}/revisions",
                __pct_encode_path_segment(version_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<CreateVersionRevisionApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CreateVersionRevisionApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Create a webhook endpoint
    ///
    /// Creates a signed HTTPS notification endpoint and returns its signing secret once. Store the secret at the receiver; later reads do not disclose it.
    ///
    /// `POST /api/v1/webhooks`
    pub async fn create_webhook(
        &self,
        idempotency_key: impl AsRef<str>,
        request: CreateWebhook,
    ) -> Result<Response<WebhookSecretResult>, ApiOpError<CreateWebhookApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/webhooks");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<CreateWebhookApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(CreateWebhookApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Remove my saved item
    ///
    /// Requires an active human membership. Requires library.own and exact tenant/realm/membership ownership; administrators cannot select another owner. Owner reads, rename and removal remain available after target access loss. OAuth scope when enforced: personal.write. Responses are no-store. Mutations require Idempotency-Key; updates and removals also require If-Match. Required audit contains identity/revision only; no public notification is created. At most 200 items per owner, labels up to 120 characters, bodies up to 16 KiB.
    ///
    /// `DELETE /api/v1/personal/items/{itemId}`
    pub async fn delete_personal_item(
        &self,
        item_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
    ) -> Result<Response<PersonalReceipt>, ApiOpError<DeletePersonalItemApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/personal/items/{}",
                __pct_encode_path_segment(item_id.as_ref())
            )
        );
        let mut req = self.http_client.delete(request_url);
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<DeletePersonalItemApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(DeletePersonalItemApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Soft-delete an unpublished local draft with no retained review, release, or adoption dependency
    ///
    /// Soft-delete an eligible local draft using its current ETag and an audit reason. Requires current resource.delete_draft and either the exact originating membership/realm/subject or protected access.manage authority. Immutable creation audit attribution establishes ownership; missing or ambiguous attribution never grants creator access. Public createdBy text is attribution only. Retained review, release or adoption dependencies prevent deletion.
    ///
    /// `POST /api/v1/resources/{resourceId}:delete`
    pub async fn delete_resource(
        &self,
        resource_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<()>, ApiOpError<DeleteResourceApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/resources/{}:delete",
                __pct_encode_path_segment(resource_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 204u16 {
            let _ = body_text;
            let _ = raw_body;
            let _ = headers;
            Ok(Response::new((), status_code, headers, raw_body))
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "204",
                )),
            }))
        } else {
            let typed: Option<DeleteResourceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(DeleteResourceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Deprecate a listing release
    ///
    /// Marks an eligible release deprecated with a reason and the current release ETag. Retained release evidence is preserved.
    ///
    /// `POST /api/v1/listing-releases/{releaseId}:deprecate`
    pub async fn deprecate_listing_release(
        &self,
        release_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: ReleaseTransition,
    ) -> Result<Response<ListingRelease>, ApiOpError<DeprecateListingReleaseApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/listing-releases/{}:deprecate",
                __pct_encode_path_segment(release_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<DeprecateListingReleaseApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(DeprecateListingReleaseApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Disable a webhook endpoint
    ///
    /// Disables the endpoint using its current ETag without a request body. Retained delivery evidence is not deleted.
    ///
    /// `DELETE /api/v1/webhooks/{webhookId}`
    pub async fn disable_webhook(
        &self,
        webhook_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
    ) -> Result<Response<()>, ApiOpError<DisableWebhookApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/webhooks/{}",
                __pct_encode_path_segment(webhook_id.as_ref())
            )
        );
        let mut req = self.http_client.delete(request_url);
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 204u16 {
            let _ = body_text;
            let _ = raw_body;
            let _ = headers;
            Ok(Response::new((), status_code, headers, raw_body))
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "204",
                )),
            }))
        } else {
            let typed: Option<DisableWebhookApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(DisableWebhookApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Edit your feedback and reopen it
    ///
    /// Requires the human submitter with feedback.own and feedback.write scope when enforced. Replaces comment/category and clears the response without changing context. Terminal records cannot change.
    ///
    /// `PATCH /api/v1/feedback/{feedbackId}`
    pub async fn edit_feedback(
        &self,
        feedback_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: FeedbackEdit,
    ) -> Result<Response<FeedbackReceipt>, ApiOpError<EditFeedbackApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/feedback/{}",
                __pct_encode_path_segment(feedback_id.as_ref())
            )
        );
        let mut req = self.http_client.patch(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<EditFeedbackApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(EditFeedbackApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Explore host-selected ARD Registry facets
    ///
    /// Authentication is required only when the exact configured discovery host uses authenticated mode.
    ///
    /// `POST /ard/v1/explore`
    pub async fn explore_ard_registry(
        &self,
        request: ARDExplore,
    ) -> Result<Response<ARDExploreResponse>, ApiOpError<ExploreArdRegistryApiError>> {
        let request_url = format!("{}{}", self.base_url, "/ard/v1/explore");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ExploreArdRegistryApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<ARDError>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ExploreArdRegistryApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Explore local Catalog facets
    ///
    /// Computes the requested bounded facets inside PostgreSQL over the same authorized local-release filters used by Catalog browse. Indexed and live federation results are not included in facet counts.
    ///
    /// `POST /api/v1/catalog/explore`
    pub async fn explore_catalog(
        &self,
        request: CatalogExplore,
    ) -> Result<Response<CatalogExploreResponse>, ApiOpError<ExploreCatalogApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/catalog/explore");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ExploreCatalogApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ExploreCatalogApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Export one bounded deterministic audit range
    ///
    /// Returns one synchronous ascending audit slice and a self-consistency manifest from a repeatable PostgreSQL snapshot. The range is half open, aggregate overflow requires a narrower range, and no export object or background job is created. File verification proves structural consistency only; restored-database comparison additionally requires a separately trusted manifest bound to the same quiesced recovery point.
    ///
    /// `POST /api/v1/audit-exports`
    pub async fn export_audit_events(
        &self,
        idempotency_key: impl AsRef<str>,
        request: AuditExportRequest,
    ) -> Result<Response<AuditExport>, ApiOpError<ExportAuditEventsApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/audit-exports");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ExportAuditEventsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ExportAuditEventsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Download a reusable draft JSON file
    ///
    /// Exports all representations from this exact VersionRevision, including readable content with validation findings. Requires current resource.read and resource.read_content in the canonical Namespace, plus catalog.read when OAuth scopes are enforced. Exact reads do not require resource.read_history. The optional ListingRevision must reference this exact revision and the same Resource, Namespace and tenant. VersionRevision and document private-read audits must succeed before disclosure; Listing metadata retains its permission-only read contract. Native text comes from retained documents, never a new source-URL fetch. Skill packages require separate package content. Plugin references carry the retained archive checksum. This read changes no domain records or outbox state. The encoded file limit is 64 MiB. Client and deployment response limits can be lower.
    ///
    /// `GET /api/v1/version-revisions/{revisionId}/export`
    pub async fn export_version_revision_draft(
        &self,
        revision_id: impl AsRef<str>,
        listing_revision_id: Option<impl AsRef<str>>,
    ) -> Result<Response<CatalogDraftFile>, ApiOpError<ExportVersionRevisionDraftApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/version-revisions/{}/export",
                __pct_encode_path_segment(revision_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = listing_revision_id {
                query_params.push(("listingRevisionId".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ExportVersionRevisionDraftApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ExportVersionRevisionDraftApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get the active AI Catalog document
    ///
    /// The exact request host selects one configured tenant and disabled, anonymous, or bearer-authenticated discovery mode. Unknown, disabled, and inactive hosts return 404.
    ///
    /// `GET /.well-known/ai-catalog.json`
    pub async fn get_ai_catalog(
        &self,
        if_none_match: Option<impl AsRef<str>>,
        if_modified_since: Option<impl AsRef<str>>,
    ) -> Result<Response<AICatalog>, ApiOpError<GetAiCatalogApiError>> {
        let request_url = format!("{}{}", self.base_url, "/.well-known/ai-catalog.json");
        let mut req = self.http_client.get(request_url);
        if let Some(v) = if_none_match {
            req = req.header("If-None-Match", v.as_ref());
        }
        if let Some(v) = if_modified_since {
            req = req.header("If-Modified-Since", v.as_ref());
        }
        req = req.header(reqwest::header::ACCEPT, "application/ai-catalog+json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        if status_code == 304 {
            return Ok(Response::empty(status_code, headers, response.body));
        }
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetAiCatalogApiError>;
            let parse_error: Option<String>;
            match status_code {
                401u16 => match serde_json::from_slice::<ARDError>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetAiCatalogApiError::Status401(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                403u16 => match serde_json::from_slice::<ARDError>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetAiCatalogApiError::Status403(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                404u16 => match serde_json::from_slice::<ARDError>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetAiCatalogApiError::Status404(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                503u16 => match serde_json::from_slice::<ARDError>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetAiCatalogApiError::Status503(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                _ => {
                    typed = None;
                    parse_error = None;
                }
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get the active ARD manifest
    ///
    /// The exact request host selects one configured tenant and disabled, anonymous, or bearer-authenticated discovery mode. Unknown, disabled, and inactive hosts return 404.
    ///
    /// `GET /.well-known/ard.json`
    pub async fn get_ard_manifest(
        &self,
        if_none_match: Option<impl AsRef<str>>,
        if_modified_since: Option<impl AsRef<str>>,
    ) -> Result<Response<ARDManifest>, ApiOpError<GetArdManifestApiError>> {
        let request_url = format!("{}{}", self.base_url, "/.well-known/ard.json");
        let mut req = self.http_client.get(request_url);
        if let Some(v) = if_none_match {
            req = req.header("If-None-Match", v.as_ref());
        }
        if let Some(v) = if_modified_since {
            req = req.header("If-Modified-Since", v.as_ref());
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        if status_code == 304 {
            return Ok(Response::empty(status_code, headers, response.body));
        }
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetArdManifestApiError>;
            let parse_error: Option<String>;
            match status_code {
                401u16 => match serde_json::from_slice::<ARDError>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetArdManifestApiError::Status401(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                403u16 => match serde_json::from_slice::<ARDError>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetArdManifestApiError::Status403(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                404u16 => match serde_json::from_slice::<ARDError>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetArdManifestApiError::Status404(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                503u16 => match serde_json::from_slice::<ARDError>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetArdManifestApiError::Status503(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                _ => {
                    typed = None;
                    parse_error = None;
                }
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get group
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled.
    ///
    /// `GET /api/v1/access/groups/{groupId}`
    pub async fn get_access_group(
        &self,
        group_id: impl AsRef<str>,
    ) -> Result<Response<AccessGroup>, ApiOpError<GetAccessGroupApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/access/groups/{}",
                __pct_encode_path_segment(group_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetAccessGroupApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetAccessGroupApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Summarize Catalog access administration
    ///
    /// Requires current access.manage and enforced tenant.admin. Counts retained memberships by actor type and lifecycle state. Active realms are not required for counted memberships. Groups count active definitions in the caller's realm. Roles count active tenant definitions. Assignments count retained active IDs, including suspended Groups or members. These are not effective access or identity-provider counts. Each measure has an independent 10000-row ceiling. Incomplete distributions are omitted. No subjects or permission expansion are returned.
    ///
    /// `GET /api/v1/overview/administration`
    pub async fn get_administration_overview(
        &self,
    ) -> Result<Response<AdministrationOverview>, ApiOpError<GetAdministrationOverviewApiError>>
    {
        let request_url = format!("{}{}", self.base_url, "/api/v1/overview/administration");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetAdministrationOverviewApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetAdministrationOverviewApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Download an authorized bounded Catalog document
    ///
    /// New content representations are exact tenant-scoped documents stored transactionally with immutable version revisions. For a URL-backed representation, this authenticated route requires current resource.read_content authority at a live same-tenant reference namespace (or an explicit tenant-wide content grant); it does not grant public artifact-delivery authority.
    ///
    /// `GET /api/v1/artifacts/{artifactId}/content`
    pub async fn get_artifact_content(
        &self,
        artifact_id: impl AsRef<str>,
    ) -> Result<Response<bytes::Bytes>, ApiOpError<GetArtifactContentApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/artifacts/{}/content",
                __pct_encode_path_segment(artifact_id.as_ref())
            )
        );
        let req = self.http_client.get(request_url);
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        if false || status_code == 200u16 {
            Ok(Response::new(
                bytes::Bytes::from(body_bytes.clone()),
                status_code,
                headers,
                body_bytes,
            ))
        } else {
            let raw_body = body_bytes;
            let body_text = String::from_utf8_lossy(&raw_body).into_owned();
            if status.is_success() {
                return Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!(
                        "unexpected successful status {}; generated return type selects `{}`",
                        status_code, "200",
                    )),
                }));
            }
            let typed: Option<GetArtifactContentApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetArtifactContentApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Download an authorized bounded Catalog document
    ///
    /// New content representations are exact tenant-scoped documents stored transactionally with immutable version revisions. For a URL-backed representation, this authenticated route requires current resource.read_content authority at a live same-tenant reference namespace (or an explicit tenant-wide content grant); it does not grant public artifact-delivery authority.
    ///
    /// `GET /api/v1/artifacts/{artifactId}/content`
    ///Select `*/*` as buffered; accepts response statuses 200. Other successful statuses and mismatched Content-Type values are returned as inspectable API errors.
    pub async fn get_artifact_content_binary(
        &self,
        artifact_id: impl AsRef<str>,
    ) -> Result<Response<bytes::Bytes>, ApiOpError<GetArtifactContentApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/artifacts/{}/content",
                __pct_encode_path_segment(artifact_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "*/*");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        if false || status_code == 200u16 {
            let actual = response
                .headers()
                .get(reqwest::header::CONTENT_TYPE)
                .and_then(|value| value.to_str().ok())
                .unwrap_or("")
                .to_string();
            let expected = "*/*";
            let parse_media = |value: &str| {
                let mut parts = Vec::new();
                let mut start = 0;
                let mut quoted = false;
                let mut escaped = false;
                for (index, character) in value.char_indices() {
                    if escaped {
                        escaped = false;
                        continue;
                    }
                    if character == '\\' && quoted {
                        escaped = true;
                        continue;
                    }
                    if character == '"' {
                        quoted = !quoted;
                    }
                    if character == ';' && !quoted {
                        parts.push(&value[start..index]);
                        start = index + 1;
                    }
                }
                if quoted || escaped {
                    return None;
                }
                parts.push(&value[start..]);
                let essence = parts
                    .first()
                    .copied()
                    .unwrap_or("")
                    .trim()
                    .to_ascii_lowercase();
                let mut parameters = std::collections::BTreeMap::new();
                for part in parts.into_iter().skip(1) {
                    let (name, value) = part.split_once('=')?;
                    let name = name.trim().to_ascii_lowercase();
                    if name.is_empty() {
                        return None;
                    }
                    let value = value.trim();
                    let value = if let Some(quoted) = value.strip_prefix('"') {
                        let inner = quoted.strip_suffix('"')?;
                        let mut decoded = String::new();
                        let mut escaped = false;
                        for character in inner.chars() {
                            if escaped {
                                decoded.push(character);
                                escaped = false;
                            } else if character == '\\' {
                                escaped = true;
                            } else if character == '"' {
                                return None;
                            } else {
                                decoded.push(character);
                            }
                        }
                        if escaped {
                            return None;
                        }
                        decoded
                    } else {
                        if value.contains('"') {
                            return None;
                        }
                        value.to_string()
                    };
                    if parameters.insert(name, value).is_some() {
                        return None;
                    }
                }
                Some((essence, parameters))
            };
            let matches = match (parse_media(&actual), parse_media(expected)) {
                (
                    Some((actual_essence, actual_parameters)),
                    Some((expected_essence, expected_parameters)),
                ) => {
                    let essence_matches = if expected_essence == "*/*" {
                        actual_essence.contains('/')
                    } else if let Some(prefix) = expected_essence.strip_suffix("/*") {
                        actual_essence.split('/').next() == Some(prefix)
                    } else {
                        actual_essence == expected_essence
                    };
                    essence_matches
                        && expected_parameters.iter().all(|(name, value)| {
                            actual_parameters.get(name).is_some_and(|actual| {
                                if name == "charset" {
                                    actual.eq_ignore_ascii_case(value)
                                } else {
                                    actual == value
                                }
                            })
                        })
                }
                _ => false,
            };
            if !matches {
                let headers = response.headers().clone();
                let raw_body = response.body.clone();
                let body_text = String::from_utf8_lossy(&raw_body).into_owned();
                let typed: Option<GetArtifactContentApiError>;
                let parse_error: Option<String>;
                match status_code {
                    _ => match serde_json::from_slice::<Problem>(&raw_body) {
                        Ok(v) => {
                            typed = Some(GetArtifactContentApiError::Default(v));
                            parse_error = None;
                        }
                        Err(e) => {
                            typed = None;
                            parse_error = Some(e.to_string());
                        }
                    },
                }
                let _ = parse_error;
                return Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers,
                    body: body_text,
                    raw_body,
                    typed,
                    parse_error: Some(format!(
                        "unexpected response Content-Type `{}`; expected `{}`",
                        actual, expected
                    )),
                }));
            }
        }
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        if false || status_code == 200u16 {
            Ok(Response::new(
                bytes::Bytes::from(body_bytes.clone()),
                status_code,
                headers,
                body_bytes,
            ))
        } else {
            let raw_body = body_bytes;
            let body_text = String::from_utf8_lossy(&raw_body).into_owned();
            if status.is_success() {
                return Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!(
                        "unexpected successful status {}; generated return type selects `{}`",
                        status_code, "200",
                    )),
                }));
            }
            let typed: Option<GetArtifactContentApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetArtifactContentApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Download an authorized bounded Catalog document
    ///
    /// New content representations are exact tenant-scoped documents stored transactionally with immutable version revisions. For a URL-backed representation, this authenticated route requires current resource.read_content authority at a live same-tenant reference namespace (or an explicit tenant-wide content grant); it does not grant public artifact-delivery authority.
    ///
    /// `GET /api/v1/artifacts/{artifactId}/content`
    /// Get a local consumer artwork override
    ///
    /// Requires source.read and sources.read when OAuth scopes are enforced. Returns the retained override head for an exact source authority and entry. Does not require or accept a live receipt in the URL. Missing heads return NOT_FOUND.
    ///
    /// `GET /api/v1/federation/sources/{sourceId}/artwork`
    pub async fn get_artwork_override(
        &self,
        source_id: impl AsRef<str>,
        presentation_generation: i64,
        identifier: impl AsRef<str>,
        version_present: bool,
        version: Option<impl AsRef<str>>,
        kind: ResourceKind,
    ) -> Result<Response<ArtworkOverride>, ApiOpError<GetArtworkOverrideApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/sources/{}/artwork",
                __pct_encode_path_segment(source_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            query_params.push((
                "presentationGeneration".to_string(),
                presentation_generation.to_string(),
            ));
            query_params.push(("identifier".to_string(), identifier.as_ref().to_string()));
            query_params.push(("versionPresent".to_string(), version_present.to_string()));
            if let Some(v) = version {
                query_params.push(("version".to_string(), v.as_ref().to_string()));
            }
            query_params.push(("kind".to_string(), kind.to_string()));
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetArtworkOverrideApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetArtworkOverrideApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get current Catalog capability state
    ///
    /// Returns implemented limits, standards profiles, optional capability states and validation capabilities. A disabled capability is not made available by client requests.
    ///
    /// `GET /api/v1/capabilities`
    pub async fn get_capabilities(
        &self,
    ) -> Result<Response<Capabilities>, ApiOpError<GetCapabilitiesApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/capabilities");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetCapabilitiesApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetCapabilitiesApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get a Catalog listing
    ///
    /// Returns a visible listing and its current revision pointer, with an ETag for subsequent commands.
    ///
    /// `GET /api/v1/catalog-listings/{listingId}`
    pub async fn get_catalog_listing(
        &self,
        listing_id: impl AsRef<str>,
    ) -> Result<Response<CatalogListing>, ApiOpError<GetCatalogListingApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/catalog-listings/{}",
                __pct_encode_path_segment(listing_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetCatalogListingApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetCatalogListingApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Discover the configured MCP OAuth resource and issuer
    ///
    /// Available only when MCP is enabled and authentication uses OIDC. URLs come from configuration, never Host or forwarding headers. Development mode and disabled MCP return 404.
    ///
    /// `GET /.well-known/oauth-protected-resource/mcp`
    pub async fn get_catalog_mcpo_auth_metadata(
        &self,
    ) -> Result<Response<MCPOAuthMetadata>, ApiOpError<GetCatalogMcpoAuthMetadataApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url, "/.well-known/oauth-protected-resource/mcp"
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetCatalogMcpoAuthMetadataApiError>;
            let parse_error: Option<String>;
            match status_code {
                400u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetCatalogMcpoAuthMetadataApiError::Status400(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                403u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetCatalogMcpoAuthMetadataApiError::Status403(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                404u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetCatalogMcpoAuthMetadataApiError::Status404(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                _ => {
                    typed = None;
                    parse_error = None;
                }
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get a governed Catalog record
    ///
    /// Returns one governed catalog record visible to the caller, including its resource and release metadata.
    ///
    /// `GET /api/v1/catalog/resources/{catalogRecordId}`
    pub async fn get_catalog_record(
        &self,
        catalog_record_id: impl AsRef<str>,
    ) -> Result<Response<CatalogRecord>, ApiOpError<GetCatalogRecordApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/catalog/resources/{}",
                __pct_encode_path_segment(catalog_record_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetCatalogRecordApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetCatalogRecordApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Download the Control API contract
    ///
    /// Returns the exact OpenAPI YAML embedded in this build, with its ETag and content digest. Send the exact returned ETag in If-None-Match to receive 304 while the contract is unchanged.
    ///
    /// `GET /openapi/control-v1.yaml`
    pub async fn get_control_open_api(
        &self,
        if_none_match: Option<impl AsRef<str>>,
    ) -> Result<Response<()>, ApiOpError<serde_json::Value>> {
        let request_url = format!("{}{}", self.base_url, "/openapi/control-v1.yaml");
        let mut req = self.http_client.get(request_url);
        if let Some(v) = if_none_match {
            req = req.header("If-None-Match", v.as_ref());
        }
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        if status_code == 304 {
            return Ok(Response::empty(status_code, headers, response.body));
        }
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            let _ = body_text;
            let _ = raw_body;
            let _ = headers;
            Ok(Response::new((), status_code, headers, raw_body))
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<serde_json::Value>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<serde_json::Value>(&raw_body) {
                    Ok(v) => {
                        typed = Some(v);
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get current resolved Catalog access
    ///
    /// Requires an active authenticated member, but no product role or OAuth scope. Current tenant permissions, bounded any-scope navigation hints and a paged exact-namespace permission projection use one policy snapshot. The cursor binds current authorization. No raw token claims or complete grant graph are returned.
    ///
    /// `GET /api/v1/me`
    pub async fn get_current_access(
        &self,
        namespace_id: Option<impl AsRef<str>>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<CurrentAccess>, ApiOpError<GetCurrentAccessApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/me");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = namespace_id {
                query_params.push(("namespaceId".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetCurrentAccessApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetCurrentAccessApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get a federation source
    ///
    /// Returns the source's reviewed configuration and current ETag without exposing its credential values.
    ///
    /// `GET /api/v1/federation/sources/{sourceId}`
    pub async fn get_federation_source(
        &self,
        source_id: impl AsRef<str>,
    ) -> Result<Response<FederationSource>, ApiOpError<GetFederationSourceApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/sources/{}",
                __pct_encode_path_segment(source_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetFederationSourceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetFederationSourceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get a source inspection
    ///
    /// Returns retained inspection evidence. Governed source history keeps referenced inspections available. Worker housekeeping may reclaim an expired unreferenced inspection; subsequent reads return not found.
    ///
    /// `GET /api/v1/federation/source-inspections/{inspectionId}`
    pub async fn get_federation_source_inspection(
        &self,
        inspection_id: impl AsRef<str>,
    ) -> Result<
        Response<FederationSourceInspection>,
        ApiOpError<GetFederationSourceInspectionApiError>,
    > {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/source-inspections/{}",
                __pct_encode_path_segment(inspection_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetFederationSourceInspectionApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetFederationSourceInspectionApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get a federation source snapshot
    ///
    /// Returns an authorized retained source snapshot and its retrieval evidence. Partial snapshots do not acquire complete-snapshot withdrawal authority.
    ///
    /// `GET /api/v1/federation/snapshots/{snapshotId}`
    pub async fn get_federation_source_snapshot(
        &self,
        snapshot_id: impl AsRef<str>,
    ) -> Result<Response<FederationSnapshot>, ApiOpError<GetFederationSourceSnapshotApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/snapshots/{}",
                __pct_encode_path_segment(snapshot_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetFederationSourceSnapshotApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetFederationSourceSnapshotApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Read current private feedback
    ///
    /// Only an authorized submitter with feedback.own or a same-tenant moderator with feedback.moderate can read feedback. Requires catalog.read scope when enforced. Existing feedback reads do not require resource.read. Terminal records contain no comment or response text. Responses are non-cacheable.
    ///
    /// `GET /api/v1/feedback/{feedbackId}`
    pub async fn get_feedback(
        &self,
        feedback_id: impl AsRef<str>,
    ) -> Result<Response<Feedback>, ApiOpError<GetFeedbackApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/feedback/{}",
                __pct_encode_path_segment(feedback_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetFeedbackApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetFeedbackApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Summarize open feedback
    ///
    /// Requires feedback.moderate and enforced tenant.admin. Counts open feedback by category, excluding soft-deleted Resources. The independent selection ceiling is 10000. An incomplete category distribution is omitted. The oldest original submission time remains independently exact, including reopened cases. No submission text or private Resource metadata is returned.
    ///
    /// `GET /api/v1/overview/feedback`
    pub async fn get_feedback_overview(
        &self,
    ) -> Result<Response<FeedbackOverview>, ApiOpError<GetFeedbackOverviewApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/overview/feedback");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetFeedbackOverviewApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetFeedbackOverviewApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get a current indexed Catalog projection
    ///
    /// Requires current tenant-level federation.discover and catalog.read. Returns the current projection under indexed-only Search eligibility, with bounded source attribution and conflict indicators. No live request, raw observation or administrator configuration is returned. A removed, paused, withdrawn or excluded external identity returns not found. A visible local counterpart does not hide this explicit indexed bookmark. This is a current bookmark, not immutable evidence.
    ///
    /// `GET /api/v1/catalog/indexed-record`
    pub async fn get_indexed_catalog_record(
        &self,
        identifier: impl AsRef<str>,
        version_present: bool,
        version: Option<impl AsRef<str>>,
    ) -> Result<Response<CatalogSearchResult>, ApiOpError<GetIndexedCatalogRecordApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/catalog/indexed-record");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            query_params.push(("identifier".to_string(), identifier.as_ref().to_string()));
            query_params.push(("versionPresent".to_string(), version_present.to_string()));
            if let Some(v) = version {
                query_params.push(("version".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetIndexedCatalogRecordApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetIndexedCatalogRecordApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Count distinct indexed external records
    ///
    /// Private current-access summary. Tenant federation.discover and enforced catalog.read. Reuse indexed-only Search eligibility without local shadowing. Bound eligible source contributions before deduplication by identifier, version presence and version. Limited contribution selection omits the distinct count. No live queries, embedding calls or artifact fetches occur. Missing or outdated derived projections return 503 instead of zero. Each response captures one server observation time and applies a fixed 10000-row ceiling per independent measure. Counts are subject to current tenant and Namespace permissions before aggregation. Separate reads are not one atomic dashboard snapshot.
    ///
    /// `GET /api/v1/overview/indexed`
    pub async fn get_indexed_overview(
        &self,
    ) -> Result<Response<IndexedOverview>, ApiOpError<GetIndexedOverviewApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/overview/indexed");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetIndexedOverviewApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetIndexedOverviewApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get a durable Job
    ///
    /// Returns retained operational Job state. A terminal Job can be reclaimed after the configured retention interval once it has no child Job, and then returns not found.
    ///
    /// `GET /api/v1/jobs/{jobId}`
    pub async fn get_job(
        &self,
        job_id: impl AsRef<str>,
    ) -> Result<Response<Job>, ApiOpError<GetJobApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/jobs/{}",
                __pct_encode_path_segment(job_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetJobApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetJobApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Summarize jobs
    ///
    /// Requires job.read and enforced catalog.read. Current family permissions and deployment target checks precede selection. Counts cover retained Jobs. Each count has an independent 10000-row ceiling. Incomplete kind/state distributions are omitted. Due work uses not_before for queued/waiting Jobs and lease expiry for running Jobs. Oldest due time remains independently exact. Due work does not establish worker availability.
    ///
    /// `GET /api/v1/overview/jobs`
    pub async fn get_job_overview(
        &self,
    ) -> Result<Response<JobOverview>, ApiOpError<GetJobOverviewApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/overview/jobs");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetJobOverviewApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetJobOverviewApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get a listing release
    ///
    /// Returns a visible release binding a listing revision to a version revision, including its lifecycle state and ETag.
    ///
    /// `GET /api/v1/listing-releases/{releaseId}`
    pub async fn get_listing_release(
        &self,
        release_id: impl AsRef<str>,
    ) -> Result<Response<ListingRelease>, ApiOpError<GetListingReleaseApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/listing-releases/{}",
                __pct_encode_path_segment(release_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetListingReleaseApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetListingReleaseApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get an immutable listing revision
    ///
    /// Returns an authorized immutable listing revision and its presentation, visibility and publisher metadata.
    ///
    /// `GET /api/v1/listing-revisions/{revisionId}`
    pub async fn get_listing_revision(
        &self,
        revision_id: impl AsRef<str>,
    ) -> Result<Response<ListingRevision>, ApiOpError<GetListingRevisionApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/listing-revisions/{}",
                __pct_encode_path_segment(revision_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetListingRevisionApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetListingRevisionApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Check process liveness
    ///
    /// Returns process liveness without contacting PostgreSQL or optional providers. Use readiness, not this endpoint, to decide whether the API can accept work.
    ///
    /// `GET /livez`
    pub async fn get_liveness(
        &self,
    ) -> Result<Response<GetLivenessResponse>, ApiOpError<serde_json::Value>> {
        let request_url = format!("{}{}", self.base_url, "/livez");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<serde_json::Value>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<serde_json::Value>(&raw_body) {
                    Ok(v) => {
                        typed = Some(v);
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get one Catalog-owned membership
    ///
    /// Returns one membership in the authenticated tenant, including its retained lifecycle state and revision.
    ///
    /// `GET /api/v1/memberships/{membershipId}`
    pub async fn get_membership(
        &self,
        membership_id: impl AsRef<str>,
    ) -> Result<Response<Membership>, ApiOpError<GetMembershipApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/memberships/{}",
                __pct_encode_path_segment(membership_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetMembershipApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetMembershipApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Inspect a member's direct and group assignment contributions
    ///
    /// Requires current access.manage and tenant.admin when OAuth scopes are enforced. Returns retained direct paths and current-realm group paths with exact role permissions and lifecycle eligibility. Other-realm group coverage is unavailable. Pages are partial inventories, not complete permission unions, distinct-user counts or recipient OAuth decisions. Each group path reports its active relation or most recent revoked relation. The cursor binds the inspected membership, current actor, policy revision and page size. Restart after policy changes.
    ///
    /// `GET /api/v1/memberships/{membershipId}/access`
    pub async fn get_membership_access(
        &self,
        membership_id: impl AsRef<str>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<MemberAccessPage>, ApiOpError<GetMembershipAccessApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/memberships/{}/access",
                __pct_encode_path_segment(membership_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetMembershipAccessApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetMembershipAccessApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get Prometheus metrics
    ///
    /// Returns deterministic size-bounded process-local metrics with fixed low-cardinality labels. PostgreSQL-backed API processes include safe client-pool and statement-class signals but never worker-global tenant aggregates.
    ///
    /// `GET /metrics`
    pub async fn get_metrics(&self) -> Result<Response<String>, ApiOpError<serde_json::Value>> {
        let request_url = format!("{}{}", self.base_url, "/metrics");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "text/plain");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            let _ = raw_body;
            Ok(Response::new(body_text, status_code, headers, raw_body))
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<serde_json::Value>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<serde_json::Value>(&raw_body) {
                    Ok(v) => {
                        typed = Some(v);
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get a namespace
    ///
    /// Returns a visible namespace and its current ETag for subsequent revision-bound commands.
    ///
    /// `GET /api/v1/namespaces/{namespaceId}`
    pub async fn get_namespace(
        &self,
        namespace_id: impl AsRef<str>,
    ) -> Result<Response<Namespace>, ApiOpError<GetNamespaceApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/namespaces/{}",
                __pct_encode_path_segment(namespace_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetNamespaceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetNamespaceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Count readable Namespaces
    ///
    /// Private current-access summary. namespace.read for each Namespace. Count readable Namespace IDs before the selection ceiling. Each response captures one server observation time and applies a fixed 10000-row ceiling per independent measure. Counts are subject to current tenant and Namespace permissions before aggregation. Separate reads are not one atomic dashboard snapshot.
    ///
    /// `GET /api/v1/overview/namespaces`
    pub async fn get_namespace_overview(
        &self,
    ) -> Result<Response<DirectoryOverview>, ApiOpError<GetNamespaceOverviewApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/overview/namespaces");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetNamespaceOverviewApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetNamespaceOverviewApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get one exact current observation group
    ///
    /// Requires tenant-level source.read and, when scope enforcement is enabled, the sources.read OAuth scope. Equivalent normalized observations share a member. Exact contributions, occurrence counts and same-source conflicts remain visible.
    ///
    /// `GET /api/v1/observation-groups`
    pub async fn get_observation_group(
        &self,
        identifier: impl AsRef<str>,
        version_present: bool,
        version: Option<impl AsRef<str>>,
    ) -> Result<Response<ObservationGroup>, ApiOpError<GetObservationGroupApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/observation-groups");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            query_params.push(("identifier".to_string(), identifier.as_ref().to_string()));
            query_params.push(("versionPresent".to_string(), version_present.to_string()));
            if let Some(v) = version {
                query_params.push(("version".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetObservationGroupApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetObservationGroupApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Read my saved item
    ///
    /// Requires an active human membership. Requires library.own and exact tenant/realm/membership ownership; administrators cannot select another owner. Owner reads, rename and removal remain available after target access loss. OAuth scope when enforced: catalog.read. Responses are no-store. Cursors bind principal, filters and current policy. Resolve never makes a live request or substitutes a different release/source/generation.
    ///
    /// `GET /api/v1/personal/items/{itemId}`
    pub async fn get_personal_item(
        &self,
        item_id: impl AsRef<str>,
    ) -> Result<Response<PersonalItem>, ApiOpError<GetPersonalItemApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/personal/items/{}",
                __pct_encode_path_segment(item_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetPersonalItemApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetPersonalItemApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Download a bounded document allowlisted by the host's active snapshot
    ///
    /// Only an exact PostgreSQL content identifier allowlisted by the active immutable publication snapshot is served.
    ///
    /// `GET /public/v1/artifacts/{artifactId}`
    pub async fn get_public_artifact(
        &self,
        artifact_id: impl AsRef<str>,
        if_none_match: Option<impl AsRef<str>>,
    ) -> Result<Response<bytes::Bytes>, ApiOpError<GetPublicArtifactApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/public/v1/artifacts/{}",
                __pct_encode_path_segment(artifact_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        if let Some(v) = if_none_match {
            req = req.header("If-None-Match", v.as_ref());
        }
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        if status_code == 304 {
            return Ok(Response::empty(status_code, headers, response.body));
        }
        let body_bytes = response.body.clone();
        if false || status_code == 200u16 {
            Ok(Response::new(
                bytes::Bytes::from(body_bytes.clone()),
                status_code,
                headers,
                body_bytes,
            ))
        } else {
            let raw_body = body_bytes;
            let body_text = String::from_utf8_lossy(&raw_body).into_owned();
            if status.is_success() {
                return Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!(
                        "unexpected successful status {}; generated return type selects `{}`",
                        status_code, "200",
                    )),
                }));
            }
            let typed: Option<GetPublicArtifactApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<ARDError>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetPublicArtifactApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Download a bounded document allowlisted by the host's active snapshot
    ///
    /// Only an exact PostgreSQL content identifier allowlisted by the active immutable publication snapshot is served.
    ///
    /// `GET /public/v1/artifacts/{artifactId}`
    ///Select `*/*` as buffered; accepts response statuses 200. Other successful statuses and mismatched Content-Type values are returned as inspectable API errors.
    pub async fn get_public_artifact_binary(
        &self,
        artifact_id: impl AsRef<str>,
        if_none_match: Option<impl AsRef<str>>,
    ) -> Result<Response<bytes::Bytes>, ApiOpError<GetPublicArtifactApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/public/v1/artifacts/{}",
                __pct_encode_path_segment(artifact_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        if let Some(v) = if_none_match {
            req = req.header("If-None-Match", v.as_ref());
        }
        req = req.header(reqwest::header::ACCEPT, "*/*");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        if false || status_code == 200u16 {
            let actual = response
                .headers()
                .get(reqwest::header::CONTENT_TYPE)
                .and_then(|value| value.to_str().ok())
                .unwrap_or("")
                .to_string();
            let expected = "*/*";
            let parse_media = |value: &str| {
                let mut parts = Vec::new();
                let mut start = 0;
                let mut quoted = false;
                let mut escaped = false;
                for (index, character) in value.char_indices() {
                    if escaped {
                        escaped = false;
                        continue;
                    }
                    if character == '\\' && quoted {
                        escaped = true;
                        continue;
                    }
                    if character == '"' {
                        quoted = !quoted;
                    }
                    if character == ';' && !quoted {
                        parts.push(&value[start..index]);
                        start = index + 1;
                    }
                }
                if quoted || escaped {
                    return None;
                }
                parts.push(&value[start..]);
                let essence = parts
                    .first()
                    .copied()
                    .unwrap_or("")
                    .trim()
                    .to_ascii_lowercase();
                let mut parameters = std::collections::BTreeMap::new();
                for part in parts.into_iter().skip(1) {
                    let (name, value) = part.split_once('=')?;
                    let name = name.trim().to_ascii_lowercase();
                    if name.is_empty() {
                        return None;
                    }
                    let value = value.trim();
                    let value = if let Some(quoted) = value.strip_prefix('"') {
                        let inner = quoted.strip_suffix('"')?;
                        let mut decoded = String::new();
                        let mut escaped = false;
                        for character in inner.chars() {
                            if escaped {
                                decoded.push(character);
                                escaped = false;
                            } else if character == '\\' {
                                escaped = true;
                            } else if character == '"' {
                                return None;
                            } else {
                                decoded.push(character);
                            }
                        }
                        if escaped {
                            return None;
                        }
                        decoded
                    } else {
                        if value.contains('"') {
                            return None;
                        }
                        value.to_string()
                    };
                    if parameters.insert(name, value).is_some() {
                        return None;
                    }
                }
                Some((essence, parameters))
            };
            let matches = match (parse_media(&actual), parse_media(expected)) {
                (
                    Some((actual_essence, actual_parameters)),
                    Some((expected_essence, expected_parameters)),
                ) => {
                    let essence_matches = if expected_essence == "*/*" {
                        actual_essence.contains('/')
                    } else if let Some(prefix) = expected_essence.strip_suffix("/*") {
                        actual_essence.split('/').next() == Some(prefix)
                    } else {
                        actual_essence == expected_essence
                    };
                    essence_matches
                        && expected_parameters.iter().all(|(name, value)| {
                            actual_parameters.get(name).is_some_and(|actual| {
                                if name == "charset" {
                                    actual.eq_ignore_ascii_case(value)
                                } else {
                                    actual == value
                                }
                            })
                        })
                }
                _ => false,
            };
            if !matches {
                let headers = response.headers().clone();
                if status_code == 304 {
                    return Ok(Response::empty(status_code, headers, response.body));
                }
                let raw_body = response.body.clone();
                let body_text = String::from_utf8_lossy(&raw_body).into_owned();
                let typed: Option<GetPublicArtifactApiError>;
                let parse_error: Option<String>;
                match status_code {
                    _ => match serde_json::from_slice::<ARDError>(&raw_body) {
                        Ok(v) => {
                            typed = Some(GetPublicArtifactApiError::Default(v));
                            parse_error = None;
                        }
                        Err(e) => {
                            typed = None;
                            parse_error = Some(e.to_string());
                        }
                    },
                }
                let _ = parse_error;
                return Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers,
                    body: body_text,
                    raw_body,
                    typed,
                    parse_error: Some(format!(
                        "unexpected response Content-Type `{}`; expected `{}`",
                        actual, expected
                    )),
                }));
            }
        }
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        if false || status_code == 200u16 {
            Ok(Response::new(
                bytes::Bytes::from(body_bytes.clone()),
                status_code,
                headers,
                body_bytes,
            ))
        } else {
            let raw_body = body_bytes;
            let body_text = String::from_utf8_lossy(&raw_body).into_owned();
            if status.is_success() {
                return Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!(
                        "unexpected successful status {}; generated return type selects `{}`",
                        status_code, "200",
                    )),
                }));
            }
            let typed: Option<GetPublicArtifactApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<ARDError>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetPublicArtifactApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Download a bounded document allowlisted by the host's active snapshot
    ///
    /// Only an exact PostgreSQL content identifier allowlisted by the active immutable publication snapshot is served.
    ///
    /// `GET /public/v1/artifacts/{artifactId}`
    /// Summarize configured publication hosts
    ///
    /// Requires publication.read and enforced catalog.read. Only configured hosts for the current tenant appear. Disabled hosts have no snapshot lookup. Active metadata for enabled hosts is read in one bounded query. Configured mode and stored audience remain separate. Recorded metadata and mismatch labels do not prove full document validity or discovery availability.
    ///
    /// `GET /api/v1/overview/publication`
    pub async fn get_publication_overview(
        &self,
    ) -> Result<Response<PublicationOverview>, ApiOpError<GetPublicationOverviewApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/overview/publication");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetPublicationOverviewApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetPublicationOverviewApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get a publication snapshot
    ///
    /// Returns the retained snapshot, generated manifests and exclusions for the specified configured host.
    ///
    /// `GET /api/v1/publication/snapshots/{snapshotId}`
    pub async fn get_publication_snapshot(
        &self,
        snapshot_id: impl AsRef<str>,
        host: impl AsRef<str>,
    ) -> Result<Response<PublicationSnapshot>, ApiOpError<GetPublicationSnapshotApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/publication/snapshots/{}",
                __pct_encode_path_segment(snapshot_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            query_params.push(("host".to_string(), host.as_ref().to_string()));
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetPublicationSnapshotApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetPublicationSnapshotApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get a publisher
    ///
    /// Returns a publisher's current metadata, declared domains and publication-domain approvals, with its revision ETag.
    ///
    /// `GET /api/v1/publishers/{publisherId}`
    pub async fn get_publisher(
        &self,
        publisher_id: impl AsRef<str>,
    ) -> Result<Response<Publisher>, ApiOpError<GetPublisherApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/publishers/{}",
                __pct_encode_path_segment(publisher_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetPublisherApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetPublisherApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Read current TXT instructions
    ///
    /// Requires current tenant-level publisher.manage. Requires publishers.manage when OAuth scopes are enforced. Returns the TXT name, value and expiry only for a usable pending challenge. Does not query DNS. Returns a verification ETag even before a challenge exists. The publishers.read OAuth scope is not required. Disabled mode returns 404.
    ///
    /// `GET /api/v1/publishers/{publisherId}/domains/{domain}/verification/challenge`
    pub async fn get_publisher_domain_challenge(
        &self,
        publisher_id: impl AsRef<str>,
        domain: impl AsRef<str>,
    ) -> Result<Response<PublisherDomainChallenge>, ApiOpError<GetPublisherDomainChallengeApiError>>
    {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/publishers/{}/domains/{}/verification/challenge",
                __pct_encode_path_segment(publisher_id.as_ref()),
                __pct_encode_path_segment(domain.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetPublisherDomainChallengeApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetPublisherDomainChallengeApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Read publisher DNS verification status
    ///
    /// Requires current tenant-level publisher.read. Requires publishers.read when OAuth scopes are enforced. Reads evidence for one exact declared DNS domain without a DNS lookup. Does not expose the TXT value. Returns a verification ETag even before a challenge exists. Status remains readable when verification is disabled. Proof freshness and the last check outcome are separate from publication approval, general publisher authority and legal rights.
    ///
    /// `GET /api/v1/publishers/{publisherId}/domains/{domain}/verification`
    pub async fn get_publisher_domain_verification(
        &self,
        publisher_id: impl AsRef<str>,
        domain: impl AsRef<str>,
    ) -> Result<
        Response<PublisherDomainVerification>,
        ApiOpError<GetPublisherDomainVerificationApiError>,
    > {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/publishers/{}/domains/{}/verification",
                __pct_encode_path_segment(publisher_id.as_ref()),
                __pct_encode_path_segment(domain.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetPublisherDomainVerificationApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetPublisherDomainVerificationApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Count Publishers
    ///
    /// Private current-access summary. Tenant publisher.read and enforced publishers.read. This count does not require catalog.read. Each response captures one server observation time and applies a fixed 10000-row ceiling per independent measure. Counts are subject to current tenant and Namespace permissions before aggregation. Separate reads are not one atomic dashboard snapshot.
    ///
    /// `GET /api/v1/overview/publishers`
    pub async fn get_publisher_overview(
        &self,
    ) -> Result<Response<DirectoryOverview>, ApiOpError<GetPublisherOverviewApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/overview/publishers");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetPublisherOverviewApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetPublisherOverviewApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Read your opinion or a moderated opinion
    ///
    /// Human opinions use exact version and source-generation identity. They do not change relevance, trust or publication approval. Own rating submission requires an active human and current target visibility. Retained own-list reads and withdrawal remain available after target visibility loss. Administrator actions cannot choose a score.  OAuth scope when enforced: catalog.read.
    ///
    /// `GET /api/v1/ratings/{ratingId}`
    pub async fn get_rating(
        &self,
        rating_id: impl AsRef<str>,
    ) -> Result<Response<Rating>, ApiOpError<GetRatingApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/ratings/{}",
                __pct_encode_path_segment(rating_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetRatingApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetRatingApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Summarize retained ratings
    ///
    /// Requires ratings.moderate and enforced tenant.admin. Counts retained active, withdrawn and removed ratings, including unavailable targets. The selection ceiling is 10000. An incomplete state distribution is omitted. Active ratings are not pending moderation. No rater identities, target metadata or aggregate score is returned.
    ///
    /// `GET /api/v1/overview/ratings`
    pub async fn get_rating_overview(
        &self,
    ) -> Result<Response<RatingOverview>, ApiOpError<GetRatingOverviewApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/overview/ratings");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetRatingOverviewApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetRatingOverviewApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Read the publication audience sharing choice
    ///
    /// Human opinions use exact version and source-generation identity. They do not change relevance, trust or publication approval. Own rating submission requires an active human and current target visibility. Retained own-list reads and withdrawal remain available after target visibility loss. Administrator actions cannot choose a score.  OAuth scope when enforced: tenant.admin.
    ///
    /// `GET /api/v1/ratings/sharing`
    pub async fn get_rating_sharing(
        &self,
        host: impl AsRef<str>,
        audience: GetRatingSharingAudience,
    ) -> Result<Response<RatingSharing>, ApiOpError<GetRatingSharingApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/ratings/sharing");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            query_params.push(("host".to_string(), host.as_ref().to_string()));
            query_params.push(("audience".to_string(), audience.to_string()));
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetRatingSharingApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetRatingSharingApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Download authorized raw observation evidence
    ///
    /// Returns the exact retained observation bytes to an authorized caller. The evidence remains external-source data, not a locally approved Resource.
    ///
    /// `GET /api/v1/observations/{observationId}/raw`
    pub async fn get_raw_observation(
        &self,
        observation_id: impl AsRef<str>,
    ) -> Result<Response<GetRawObservationResponse>, ApiOpError<GetRawObservationApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/observations/{}/raw",
                __pct_encode_path_segment(observation_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetRawObservationApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetRawObservationApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Check dependency readiness
    ///
    /// Returns readiness after startup and required database checks. The API becomes unready during drain or when its required PostgreSQL connection is unavailable or read-only.
    ///
    /// `GET /readyz`
    pub async fn get_readiness(
        &self,
    ) -> Result<Response<GetReadinessResponse>, ApiOpError<GetReadinessApiError>> {
        let request_url = format!("{}{}", self.base_url, "/readyz");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetReadinessApiError>;
            let parse_error: Option<String>;
            match status_code {
                503u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetReadinessApiError::Status503(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                _ => {
                    typed = None;
                    parse_error = None;
                }
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Count current Listing releases
    ///
    /// Private current-access summary. resource.read_history in each Listing Namespace. Follow current Listing pointers only. Published and deprecated counts use independent ceilings. Resource-read permission is not required. Each response captures one server observation time and applies a fixed 10000-row ceiling per independent measure. Counts are subject to current tenant and Namespace permissions before aggregation. Separate reads are not one atomic dashboard snapshot.
    ///
    /// `GET /api/v1/overview/releases`
    pub async fn get_release_overview(
        &self,
    ) -> Result<Response<ReleaseOverview>, ApiOpError<GetReleaseOverviewApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/overview/releases");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetReleaseOverviewApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetReleaseOverviewApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get a Resource
    ///
    /// Returns a visible local Resource and its current revision ETag. Immutable versions and listing history are read through their own endpoints.
    ///
    /// `GET /api/v1/resources/{resourceId}`
    pub async fn get_resource(
        &self,
        resource_id: impl AsRef<str>,
    ) -> Result<Response<Resource>, ApiOpError<GetResourceApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/resources/{}",
                __pct_encode_path_segment(resource_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetResourceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetResourceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Count readable local Resources
    ///
    /// Private current-access summary. resource.read in each Resource Namespace. Local Resource IDs count once. Soft-deleted Resources are excluded. Kind counts are present only for a complete population. Each response captures one server observation time and applies a fixed 10000-row ceiling per independent measure. Counts are subject to current tenant and Namespace permissions before aggregation. Separate reads are not one atomic dashboard snapshot.
    ///
    /// `GET /api/v1/overview/resources`
    pub async fn get_resource_overview(
        &self,
    ) -> Result<Response<ResourceOverview>, ApiOpError<GetResourceOverviewApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/overview/resources");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetResourceOverviewApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetResourceOverviewApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get a Resource version
    ///
    /// Returns version metadata and pointers to its current and sealed revisions, with the version ETag.
    ///
    /// `GET /api/v1/resource-versions/{versionId}`
    pub async fn get_resource_version(
        &self,
        version_id: impl AsRef<str>,
    ) -> Result<Response<ResourceVersion>, ApiOpError<GetResourceVersionApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/resource-versions/{}",
                __pct_encode_path_segment(version_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetResourceVersionApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetResourceVersionApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Summarize readable reviews and 30-day activity
    ///
    /// Current resource.read_history visibility applies to all populations. The interval starts at UTC midnight 29 days before observedAt and excludes observedAt. Each population has an independent 10000-row ceiling. Incomplete age distributions and series are omitted. The median includes all decision outcomes selected by decidedAt. No decisions omit the median. Invalid durations omit the median with invalid_timestamps. Pending and approved-unreleased totals are independent of the interval.
    ///
    /// `GET /api/v1/overview/reviews`
    pub async fn get_review_overview(
        &self,
    ) -> Result<Response<ReviewOverview>, ApiOpError<GetReviewOverviewApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/overview/reviews");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetReviewOverviewApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetReviewOverviewApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get a review submission
    ///
    /// Returns a visible review submission with its decision state and referenced immutable revisions.
    ///
    /// `GET /api/v1/review-submissions/{submissionId}`
    pub async fn get_review_submission(
        &self,
        submission_id: impl AsRef<str>,
    ) -> Result<Response<ReviewSubmission>, ApiOpError<GetReviewSubmissionApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/review-submissions/{}",
                __pct_encode_path_segment(submission_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetReviewSubmissionApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetReviewSubmissionApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Read exact submission-bound validation evidence
    ///
    /// Requires current resource.read_content at the submission namespace and retains the version-revision private-read audit record before disclosure. Returns the evidence ID, digest and policy version bound by this submission, never a later validation run. A failed audit write prevents disclosure.
    ///
    /// `GET /api/v1/review-submissions/{submissionId}/evidence`
    pub async fn get_review_submission_evidence(
        &self,
        submission_id: impl AsRef<str>,
    ) -> Result<Response<EvidenceSet>, ApiOpError<GetReviewSubmissionEvidenceApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/review-submissions/{}/evidence",
                __pct_encode_path_segment(submission_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetReviewSubmissionEvidenceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetReviewSubmissionEvidenceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get role
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled.
    ///
    /// `GET /api/v1/access/roles/{roleId}`
    pub async fn get_role_definition(
        &self,
        role_id: impl AsRef<str>,
    ) -> Result<Response<RoleDefinition>, ApiOpError<GetRoleDefinitionApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/access/roles/{}",
                __pct_encode_path_segment(role_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetRoleDefinitionApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetRoleDefinitionApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get request statistics for the responding API process
    ///
    /// Requires current active membership and tenant-wide access.manage permission. When OAuth scope enforcement is enabled, GET and implicit HEAD require tenant.admin. The route is disabled by default. Enable http.serviceActivityEnabled to permit aggregate traffic disclosure across tenants to current administrators. Returns only bounded process-memory counters, with no tenant, user, resource or query identifiers. A later request can reach another replica. Counters reset on process restart. Accepts no query parameters or request body. This read creates no audit or outbox event. Catalog work excludes probes, metrics and activity polling. Search and MCP outcomes overlap HTTP counts.
    ///
    /// `GET /api/v1/service-activity`
    pub async fn get_service_activity(
        &self,
    ) -> Result<Response<ServiceActivity>, ApiOpError<GetServiceActivityApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/service-activity");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetServiceActivityApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetServiceActivityApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get the responding process's build and startup information
    ///
    /// Requires authentication; no additional role or OAuth scope is required. Returns build and process timing together. The handler reads only process memory; authentication still applies. The instanceId identifies one execution, including when tracing is disabled. A later read may reach another replica. This is not a node inventory, health report or deployment history. Uptime uses monotonic elapsed time; UTC wall-clock timestamps can change after a clock correction.
    ///
    /// `GET /api/v1/service-information`
    pub async fn get_service_information(
        &self,
    ) -> Result<Response<ServiceInformation>, ApiOpError<GetServiceInformationApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/service-information");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetServiceInformationApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetServiceInformationApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Summarize sources
    ///
    /// Requires source.read and enforced sources.read. Removed Sources do not count. Current-revision completed attempts determine sync outcomes. Select the latest attempt before failures. Last-complete evidence can belong to an earlier revision. Distributions are omitted above 10000 Sources. The problem list independently selects up to five Sources. No peer requests occur.
    ///
    /// `GET /api/v1/overview/sources`
    pub async fn get_source_overview(
        &self,
    ) -> Result<Response<SourceOverview>, ApiOpError<GetSourceOverviewApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/overview/sources");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetSourceOverviewApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetSourceOverviewApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Read the current tenant's operational settings
    ///
    /// Requires tenant settings.read and tenant.admin OAuth scope when enforced. Returns saved overrides, this API process's deployment defaults and the resolved policy. An absent record has revision zero, empty overrides and a usable strong ETag. Deployment defaults must be consistent across API and worker processes.
    ///
    /// `GET /api/v1/tenant-settings`
    pub async fn get_tenant_settings(
        &self,
    ) -> Result<Response<TenantSettings>, ApiOpError<GetTenantSettingsApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/tenant-settings");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetTenantSettingsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetTenantSettingsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Read a private completed assessment and exact evidence
    ///
    /// Requires job.read and source.read_evidence in the exact tenant and source. Non-succeeded Jobs return INVALID_TRANSITION. Historical results remain readable after disablement, source removal or policy rotation. Current interpretation also checks source revision, configuration, authority policy and signed validity. Evidence uses base64 to preserve original bytes. Generic Job responses never expose this evidence. A failed private-read audit write prevents disclosure. Job retention controls availability, not validity.
    ///
    /// `GET /api/v1/federation/sources/{sourceId}/trust-assessments/{jobId}`
    pub async fn get_trust_assessment(
        &self,
        source_id: impl AsRef<str>,
        job_id: impl AsRef<str>,
    ) -> Result<Response<TrustAssessmentResponse>, ApiOpError<GetTrustAssessmentApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/sources/{}/trust-assessments/{}",
                __pct_encode_path_segment(source_id.as_ref()),
                __pct_encode_path_segment(job_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetTrustAssessmentApiError>;
            let parse_error: Option<String>;
            match status_code {
                401u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetTrustAssessmentApiError::Status401(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                403u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetTrustAssessmentApiError::Status403(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                404u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetTrustAssessmentApiError::Status404(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                409u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetTrustAssessmentApiError::Status409(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                503u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetTrustAssessmentApiError::Status503(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                _ => {
                    typed = None;
                    parse_error = None;
                }
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get service and profile versions
    ///
    /// Returns build metadata and the pinned standards profiles supported by this process. This public response contains no deployment credentials.
    ///
    /// `GET /version`
    pub async fn get_version(
        &self,
    ) -> Result<Response<ServiceVersion>, ApiOpError<serde_json::Value>> {
        let request_url = format!("{}{}", self.base_url, "/version");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<serde_json::Value>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<serde_json::Value>(&raw_body) {
                    Ok(v) => {
                        typed = Some(v);
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get an immutable version revision
    ///
    /// Returns an authorized immutable version revision with its representation references and retained validation evidence.
    ///
    /// `GET /api/v1/version-revisions/{revisionId}`
    pub async fn get_version_revision(
        &self,
        revision_id: impl AsRef<str>,
    ) -> Result<Response<RevisionEvidenceEnvelope>, ApiOpError<GetVersionRevisionApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/version-revisions/{}",
                __pct_encode_path_segment(revision_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetVersionRevisionApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetVersionRevisionApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Get a webhook endpoint
    ///
    /// Returns the webhook endpoint's configuration and current ETag without its signing secret.
    ///
    /// `GET /api/v1/webhooks/{webhookId}`
    pub async fn get_webhook(
        &self,
        webhook_id: impl AsRef<str>,
    ) -> Result<Response<Webhook>, ApiOpError<GetWebhookApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/webhooks/{}",
                __pct_encode_path_segment(webhook_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetWebhookApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetWebhookApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Summarize retained webhook deliveries
    ///
    /// Requires webhook.read_evidence with its webhook.read companion and enforced webhooks.manage. Webhooks must be enabled. Counts cover retained production deliveries, including disabled endpoints and acknowledged parents. Synthetic tests are excluded. Distributions are omitted above 10000 rows. Failed endpoints independently select up to five active endpoints. Select the latest attempted row before filtering waiting or dead_letter outcomes. Order attempts by updated_at DESC and id DESC, at or after the current endpoint update. Retention can remove later outcomes. Failure can precede HTTP. These results do not establish receiver health, dispatch eligibility or lifetime order.
    ///
    /// `GET /api/v1/overview/webhook-deliveries`
    pub async fn get_webhook_delivery_overview(
        &self,
    ) -> Result<Response<WebhookDeliveryOverview>, ApiOpError<GetWebhookDeliveryOverviewApiError>>
    {
        let request_url = format!("{}{}", self.base_url, "/api/v1/overview/webhook-deliveries");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetWebhookDeliveryOverviewApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetWebhookDeliveryOverviewApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Summarize webhook endpoints
    ///
    /// Requires webhook.read and enforced webhooks.manage. Webhooks must be enabled. Counts include active and disabled endpoints. The independent selection ceiling is 10000. Incomplete distributions are omitted. No receiver requests occur.
    ///
    /// `GET /api/v1/overview/webhook-endpoints`
    pub async fn get_webhook_endpoint_overview(
        &self,
    ) -> Result<Response<WebhookEndpointOverview>, ApiOpError<GetWebhookEndpointOverviewApiError>>
    {
        let request_url = format!("{}{}", self.base_url, "/api/v1/overview/webhook-endpoints");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetWebhookEndpointOverviewApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetWebhookEndpointOverviewApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Read tenant webhook reconciliation state
    ///
    /// Requires current tenant-wide webhook.read_evidence permission and the webhooks.manage OAuth scope when scope enforcement is enabled. Returns coalesced notification loss counters. Compare both counters with the receiver checkpoint and refresh current authorized Catalog state after a change, restore, or disabled interval. Capacity loss counts public events; delivery loss counts terminal recipient failures, including retry exhaustion but excluding synthetic tests. Counters are not reset and do not promise historical replay. Pending events include claimed and deferred work and are capped at pendingEventLimit; equality means at least that many. Disabled webhooks omit this route.
    ///
    /// `GET /api/v1/webhooks/reconciliation`
    pub async fn get_webhook_reconciliation(
        &self,
    ) -> Result<Response<WebhookReconciliation>, ApiOpError<GetWebhookReconciliationApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/webhooks/reconciliation");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<GetWebhookReconciliationApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GetWebhookReconciliationApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Grant a scoped role to a member or group
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Assignment grants, revocations and group-member writes stay within the same identity realm; membership inventory and lifecycle are tenant-wide. Built-in roles are immutable. Custom roles cannot grant access.manage. Writes and private audit evidence commit atomically. Public notifications do not expose the permission graph.
    ///
    /// `POST /api/v1/access/assignments`
    pub async fn grant_access_assignment(
        &self,
        idempotency_key: impl AsRef<str>,
        request: AccessAssignmentInput,
    ) -> Result<Response<AccessAssignment>, ApiOpError<GrantAccessAssignmentApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/access/assignments");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<GrantAccessAssignmentApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GrantAccessAssignmentApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Grant a Catalog-owned tenant or namespace role in the caller's identity realm
    ///
    /// Grants a role in the authenticated tenant and identity realm, optionally scoped to one namespace. Authorization, separation of duties and the supplied audit reason are checked before the grant is committed.
    ///
    /// `POST /api/v1/role-assignments`
    pub async fn grant_role_assignment(
        &self,
        idempotency_key: impl AsRef<str>,
        request: GrantRole,
    ) -> Result<Response<RoleAssignment>, ApiOpError<GrantRoleAssignmentApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/role-assignments");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<GrantRoleAssignmentApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(GrantRoleAssignmentApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Check MCP OAuth metadata availability
    ///
    /// `HEAD /.well-known/oauth-protected-resource/mcp`
    pub async fn head_catalog_mcpo_auth_metadata(
        &self,
    ) -> Result<Response<()>, ApiOpError<HeadCatalogMcpoAuthMetadataApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url, "/.well-known/oauth-protected-resource/mcp"
        );
        let req = self.http_client.head(request_url);
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            let _ = body_text;
            let _ = raw_body;
            let _ = headers;
            Ok(Response::new((), status_code, headers, raw_body))
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<HeadCatalogMcpoAuthMetadataApiError>;
            let parse_error: Option<String>;
            match status_code {
                403u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(HeadCatalogMcpoAuthMetadataApiError::Status403(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                404u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(HeadCatalogMcpoAuthMetadataApiError::Status404(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                _ => {
                    typed = None;
                    parse_error = None;
                }
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Inspect a federation source
    ///
    /// Inspects an administrator-supplied source endpoint under the bounded outbound fetch policy and returns retained inspection evidence. Inspection does not connect or approve the source.
    ///
    /// `POST /api/v1/federation/source-inspections`
    pub async fn inspect_federation_source(
        &self,
        idempotency_key: impl AsRef<str>,
        request: InspectSource,
    ) -> Result<Response<FederationSourceInspection>, ApiOpError<InspectFederationSourceApiError>>
    {
        let request_url = format!(
            "{}{}",
            self.base_url, "/api/v1/federation/source-inspections"
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<InspectFederationSourceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(InspectFederationSourceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Issue or replace a DNS TXT challenge
    ///
    /// Requires current tenant-level publisher.manage. Requires publishers.manage when OAuth scopes are enforced. Send an Idempotency-Key and the current verification ETag from either verification read in If-Match. Do not use the Publisher metadata ETag. Replaces the pending TXT value without extending or erasing earlier proof. Disabled mode returns 404 for a new command. Historical commands replay under current management authority after replacement, clearing or domain removal.
    ///
    /// `POST /api/v1/publishers/{publisherId}/domains/{domain}/verification:issue`
    pub async fn issue_publisher_domain_challenge(
        &self,
        publisher_id: impl AsRef<str>,
        domain: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<PublisherDomainChallenge>, ApiOpError<IssuePublisherDomainChallengeApiError>>
    {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/publishers/{}/domains/{}/verification:issue",
                __pct_encode_path_segment(publisher_id.as_ref()),
                __pct_encode_path_segment(domain.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<IssuePublisherDomainChallengeApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(IssuePublisherDomainChallengeApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List host-selected ARD Registry entries
    ///
    /// Authentication is required only when the exact configured discovery host uses authenticated mode.
    ///
    /// `GET /ard/v1/agents`
    pub async fn list_ard_registry_entries(
        &self,
        filter: Option<impl AsRef<str>>,
        order_by: Option<impl AsRef<str>>,
        page_size: Option<i64>,
        page_token: Option<impl AsRef<str>>,
    ) -> Result<Response<ARDListResponse>, ApiOpError<ListArdRegistryEntriesApiError>> {
        let request_url = format!("{}{}", self.base_url, "/ard/v1/agents");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = filter {
                query_params.push(("filter".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = order_by {
                query_params.push(("orderBy".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = page_size {
                query_params.push(("pageSize".to_string(), v.to_string()));
            }
            if let Some(v) = page_token {
                query_params.push(("pageToken".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListArdRegistryEntriesApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<ARDError>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListArdRegistryEntriesApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List retained scoped authorization assignments
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Role and assignment views are tenant-bounded. Group views remain in the administrator's identity realm. The signed cursor binds the actor, authorization revision, filters and page limit. Restart pagination after authority changes.
    ///
    /// `GET /api/v1/access/assignments`
    pub async fn list_access_assignments(
        &self,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
        role_definition_id: Option<impl AsRef<str>>,
        membership_id: Option<impl AsRef<str>>,
        group_id: Option<impl AsRef<str>>,
    ) -> Result<Response<AccessAssignmentPage>, ApiOpError<ListAccessAssignmentsApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/access/assignments");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = role_definition_id {
                query_params.push(("roleDefinitionId".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = membership_id {
                query_params.push(("membershipId".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = group_id {
                query_params.push(("groupId".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListAccessAssignmentsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListAccessAssignmentsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List retained direct group members
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Role and assignment views are tenant-bounded. Group views remain in the administrator's identity realm. The signed cursor binds the actor, authorization revision, filters and page limit. Restart pagination after authority changes.
    ///
    /// `GET /api/v1/access/groups/{groupId}/members`
    pub async fn list_access_group_members(
        &self,
        group_id: impl AsRef<str>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<AccessGroupMemberPage>, ApiOpError<ListAccessGroupMembersApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/access/groups/{}/members",
                __pct_encode_path_segment(group_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListAccessGroupMembersApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListAccessGroupMembersApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List groups
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Role and assignment views are tenant-bounded. Group views remain in the administrator's identity realm. The signed cursor binds the actor, authorization revision, filters and page limit. Exact name and state filters intersect with AND before selection. Display spelling is case-sensitive; duplicate names remain separate rows. Restart pagination after authority changes.
    ///
    /// `GET /api/v1/access/groups`
    pub async fn list_access_groups(
        &self,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
        name: Option<impl AsRef<str>>,
        state: Option<ListAccessGroupsState>,
    ) -> Result<Response<AccessGroupPage>, ApiOpError<ListAccessGroupsApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/access/groups");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = name {
                query_params.push(("name".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = state {
                query_params.push(("state".to_string(), v.to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListAccessGroupsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListAccessGroupsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List local artwork override history
    ///
    /// Requires source.read and sources.read when OAuth scopes are enforced. Bounded same-tenant retained consumer override revisions, ordered by stable revision identity. No live proof or image bytes are returned.
    ///
    /// `GET /api/v1/federation/sources/{sourceId}/artwork/history`
    pub async fn list_artwork_override_history(
        &self,
        source_id: impl AsRef<str>,
        presentation_generation: i64,
        identifier: impl AsRef<str>,
        version_present: bool,
        version: Option<impl AsRef<str>>,
        kind: ResourceKind,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<ArtworkOverridePage>, ApiOpError<ListArtworkOverrideHistoryApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/sources/{}/artwork/history",
                __pct_encode_path_segment(source_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            query_params.push((
                "presentationGeneration".to_string(),
                presentation_generation.to_string(),
            ));
            query_params.push(("identifier".to_string(), identifier.as_ref().to_string()));
            query_params.push(("versionPresent".to_string(), version_present.to_string()));
            if let Some(v) = version {
                query_params.push(("version".to_string(), v.as_ref().to_string()));
            }
            query_params.push(("kind".to_string(), kind.to_string()));
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListArtworkOverrideHistoryApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListArtworkOverrideHistoryApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Investigate authorized audit events
    ///
    /// Returns one newest-first page after applying exact tenant-scoped filters at the PostgreSQL boundary. The paired target filters must be supplied together; occurredFrom is inclusive and occurredBefore is exclusive. Preparing a successful page appends an audit-only access record before response bytes are returned.
    ///
    /// `GET /api/v1/audit-events`
    pub async fn list_audit_events(
        &self,
        actor: Option<impl AsRef<str>>,
        action: Option<impl AsRef<str>>,
        request_id: Option<impl AsRef<str>>,
        target_type: Option<impl AsRef<str>>,
        target_id: Option<impl AsRef<str>>,
        occurred_from: Option<impl AsRef<str>>,
        occurred_before: Option<impl AsRef<str>>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<AuditEventPage>, ApiOpError<ListAuditEventsApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/audit-events");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = actor {
                query_params.push(("actor".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = action {
                query_params.push(("action".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = request_id {
                query_params.push(("requestId".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = target_type {
                query_params.push(("targetType".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = target_id {
                query_params.push(("targetId".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = occurred_from {
                query_params.push(("occurredFrom".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = occurred_before {
                query_params.push(("occurredBefore".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListAuditEventsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListAuditEventsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List current effective assignment paths
    ///
    /// Requires a current active membership. Reports only the caller's current Catalog assignment paths. OAuth ceilings and operation eligibility remain separate. Group captions describe only eligible paths in the caller's identity realm and grant no group-directory access. Names are current presentation, not immutable attribution or additional authority. The signed cursor binds the actor, authorization revision and page limit. Restart pagination after authority changes.
    ///
    /// `GET /api/v1/access/self`
    pub async fn list_current_access(
        &self,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<EffectiveAccessPage>, ApiOpError<ListCurrentAccessApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/access/self");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListCurrentAccessApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListCurrentAccessApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List source snapshots
    ///
    /// Returns cursor-paginated synchronization snapshot summaries for the source. Fetch an individual snapshot to inspect its retained retrieval evidence.
    ///
    /// `GET /api/v1/federation/sources/{sourceId}/snapshots`
    pub async fn list_federation_source_snapshots(
        &self,
        source_id: impl AsRef<str>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<FederationSnapshotPage>, ApiOpError<ListFederationSourceSnapshotsApiError>>
    {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/sources/{}/snapshots",
                __pct_encode_path_segment(source_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListFederationSourceSnapshotsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListFederationSourceSnapshotsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List federation sources
    ///
    /// Returns nonremoved configured sources in the authenticated tenant, ordered by exact display name then ID. Optional exact displayName and status filters apply before pagination. Duplicate names retain distinct IDs. Continuation binds filters, ordering, page size, tenant and current authority. Removed sources are unavailable here and through exact Source reads; exact retained evidence follows its separate admission. No peer is contacted and credential values are never returned.
    ///
    /// `GET /api/v1/federation/sources`
    pub async fn list_federation_sources(
        &self,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
        display_name: Option<impl AsRef<str>>,
        status: Option<ListFederationSourcesStatus>,
    ) -> Result<Response<FederationSourcePage>, ApiOpError<ListFederationSourcesApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/federation/sources");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = display_name {
                query_params.push(("displayName".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = status {
                query_params.push(("status".to_string(), v.to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListFederationSourcesApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListFederationSourcesApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List the private feedback inbox
    ///
    /// Requires feedback.moderate and tenant.admin scope when scopes are enforced. Filters apply before pagination; cursors bind authorization and filters. Responses are non-cacheable.
    ///
    /// `GET /api/v1/feedback/inbox`
    pub async fn list_feedback_inbox(
        &self,
        resource_id: Option<impl AsRef<str>>,
        category: Option<FeedbackCategory>,
        state: Option<FeedbackState>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<FeedbackPage>, ApiOpError<ListFeedbackInboxApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/feedback/inbox");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = resource_id {
                query_params.push(("resourceId".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = category {
                query_params.push(("category".to_string(), v.to_string()));
            }
            if let Some(v) = state {
                query_params.push(("state".to_string(), v.to_string()));
            }
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListFeedbackInboxApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListFeedbackInboxApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List durable Jobs
    ///
    /// Returns a cursor-paginated page of durable Jobs with public status and result references, never private execution payloads. Requires current job.read. Selection applies the actual family read permission and existing configured target before pagination: source.read, source.read_evidence, publication.read or operations.reindex. Unreadable families and unknown, removed or unconfigured targets are omitted. Exact Job reads require the actual family and target authority. Optional kind, state and parentJobId filters intersect before page limits. Kind matches literally, including unknown names. Parent filtering matches each child's retained reference without reading or requiring access to the parent.
    ///
    /// `GET /api/v1/jobs`
    pub async fn list_jobs(
        &self,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
        kind: Option<impl AsRef<str>>,
        state: Option<ListJobsState>,
        parent_job_id: Option<impl AsRef<str>>,
    ) -> Result<Response<JobPage>, ApiOpError<ListJobsApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/jobs");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = kind {
                query_params.push(("kind".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = state {
                query_params.push(("state".to_string(), v.to_string()));
            }
            if let Some(v) = parent_job_id {
                query_params.push(("parentJobId".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListJobsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListJobsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List releases
    ///
    /// Requires current resource.read_history at the canonical parent namespace. Results use ascending immutable UUID order and bounded parent-scoped pages. Revision histories contain summaries; exact content is read separately.
    ///
    /// `GET /api/v1/catalog-listings/{listingId}/releases`
    pub async fn list_listing_releases(
        &self,
        listing_id: impl AsRef<str>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<ListListingReleasesResponse>, ApiOpError<ListListingReleasesApiError>>
    {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/catalog-listings/{}/releases",
                __pct_encode_path_segment(listing_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListListingReleasesApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListListingReleasesApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List listing revisions
    ///
    /// Requires current resource.read_history at the canonical parent namespace. Results use ascending immutable UUID order and bounded parent-scoped pages. Revision histories contain summaries; exact content is read separately.
    ///
    /// `GET /api/v1/catalog-listings/{listingId}/revisions`
    pub async fn list_listing_revisions(
        &self,
        listing_id: impl AsRef<str>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<ListListingRevisionsResponse>, ApiOpError<ListListingRevisionsApiError>>
    {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/catalog-listings/{}/revisions",
                __pct_encode_path_segment(listing_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListListingRevisionsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListListingRevisionsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List Catalog-owned memberships for the authenticated tenant
    ///
    /// Returns a cursor-paginated tenant membership page. Optional exact subject, state, actorType and same-tenant realmId filters intersect with AND before selection. Subjects remain case-sensitive. Cursors bind filters, current authority and page size.
    ///
    /// `GET /api/v1/memberships`
    pub async fn list_memberships(
        &self,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
        subject: Option<impl AsRef<str>>,
        state: Option<ListMembershipsState>,
        actor_type: Option<ActorType>,
        realm_id: Option<impl AsRef<str>>,
    ) -> Result<Response<MembershipPage>, ApiOpError<ListMembershipsApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/memberships");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = subject {
                query_params.push(("subject".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = state {
                query_params.push(("state".to_string(), v.to_string()));
            }
            if let Some(v) = actor_type {
                query_params.push(("actorType".to_string(), v.to_string()));
            }
            if let Some(v) = realm_id {
                query_params.push(("realmId".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListMembershipsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListMembershipsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List visible namespaces
    ///
    /// Returns a cursor-paginated page selected by current namespace.read permission before ordering and limits. Optional exact slug and displayName filters are intersected before limits. Slugs are trimmed and lowercased; display names preserve exact case and spacing. Duplicate names remain distinct by ID. A policy or filter change invalidates continuation; an active caller without namespace grants receives an empty page.
    ///
    /// `GET /api/v1/namespaces`
    pub async fn list_namespaces(
        &self,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
        slug: Option<impl AsRef<str>>,
        display_name: Option<impl AsRef<str>>,
    ) -> Result<Response<NamespacePage>, ApiOpError<ListNamespacesApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/namespaces");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = slug {
                query_params.push(("slug".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = display_name {
                query_params.push(("displayName".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListNamespacesApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListNamespacesApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List exact snapshot occurrences for an observation
    ///
    /// Entries require complete source-revision and retrieval provenance. Unsupported upstream observations may omit normalized evidence.
    ///
    /// `GET /api/v1/observations/{observationId}/occurrences`
    pub async fn list_observation_occurrences(
        &self,
        observation_id: impl AsRef<str>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<ObservationOccurrencePage>, ApiOpError<ListObservationOccurrencesApiError>>
    {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/observations/{}/occurrences",
                __pct_encode_path_segment(observation_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListObservationOccurrencesApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListObservationOccurrencesApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List source observations
    ///
    /// Returns a cursor-paginated page of authorized source observations filtered by source and freshness.
    ///
    /// `GET /api/v1/observations`
    pub async fn list_observations(
        &self,
        source_id: Option<impl AsRef<str>>,
        freshness: Option<ListObservationsFreshness>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<ObservationPage>, ApiOpError<ListObservationsApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/observations");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = source_id {
                query_params.push(("sourceId".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = freshness {
                query_params.push(("freshness".to_string(), v.to_string()));
            }
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListObservationsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListObservationsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List your own feedback
    ///
    /// Requires active human membership with feedback.own and catalog.read scope when scopes are enforced. Filters apply before pagination; cursors bind authorization and filters. Responses are non-cacheable.
    ///
    /// `GET /api/v1/feedback`
    pub async fn list_own_feedback(
        &self,
        resource_id: Option<impl AsRef<str>>,
        category: Option<FeedbackCategory>,
        state: Option<FeedbackState>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<FeedbackPage>, ApiOpError<ListOwnFeedbackApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/feedback");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = resource_id {
                query_params.push(("resourceId".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = category {
                query_params.push(("category".to_string(), v.to_string()));
            }
            if let Some(v) = state {
                query_params.push(("state".to_string(), v.to_string()));
            }
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListOwnFeedbackApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListOwnFeedbackApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List your own opinions
    ///
    /// Human opinions use exact version and source-generation identity. They do not change relevance, trust or publication approval. Own rating submission requires an active human and current target visibility. Retained own-list reads and withdrawal remain available after target visibility loss. Administrator actions cannot choose a score.  OAuth scope when enforced: catalog.read.
    ///
    /// `GET /api/v1/ratings`
    pub async fn list_own_ratings(
        &self,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
        state: Option<ListOwnRatingsState>,
    ) -> Result<Response<RatingPage>, ApiOpError<ListOwnRatingsApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/ratings");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = state {
                query_params.push(("state".to_string(), v.to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListOwnRatingsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListOwnRatingsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List application-owned permission descriptors
    ///
    /// Requires a current active membership. Returns the complete finite application permission vocabulary, including keys the caller does not hold. Descriptors explain actions and required role-definition companions; they do not grant permission or report an allow decision. Use capabilities for current checks and self-access for the caller's current grants. OAuth ceilings and operation eligibility remain separate.
    ///
    /// `GET /api/v1/access/permissions`
    pub async fn list_permission_descriptors(
        &self,
    ) -> Result<
        Response<ListPermissionDescriptorsResponse>,
        ApiOpError<ListPermissionDescriptorsApiError>,
    > {
        let request_url = format!("{}{}", self.base_url, "/api/v1/access/permissions");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListPermissionDescriptorsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListPermissionDescriptorsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List my saved resources and searches
    ///
    /// Requires an active human membership. Requires library.own and exact tenant/realm/membership ownership; administrators cannot select another owner. Owner reads, rename and removal remain available after target access loss. OAuth scope when enforced: catalog.read. Responses are no-store. Cursors bind principal, filters and current policy. Resolve never makes a live request or substitutes a different release/source/generation.
    ///
    /// `GET /api/v1/personal/items`
    pub async fn list_personal_items(
        &self,
        r#type: Option<ListPersonalItemsType>,
        label: Option<impl AsRef<str>>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<PersonalPage>, ApiOpError<ListPersonalItemsApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/personal/items");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = r#type {
                query_params.push(("type".to_string(), v.to_string()));
            }
            if let Some(v) = label {
                query_params.push(("label".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListPersonalItemsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListPersonalItemsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List my current authoring work
    ///
    /// Requires an active human membership. Current own-work attribution and per-field metadata/content/history permissions apply. OAuth scope when enforced: catalog.read. Responses are no-store. Cursors bind principal, filters and current policy. Resolve never makes a live request or substitutes a different release/source/generation.
    ///
    /// `GET /api/v1/me/work`
    pub async fn list_personal_work(
        &self,
        queue: Option<ListPersonalWorkQueue>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<PersonalWorkPage>, ApiOpError<ListPersonalWorkApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/me/work");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = queue {
                query_params.push(("queue".to_string(), v.to_string()));
            }
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListPersonalWorkApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListPersonalWorkApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List configured publication hosts
    ///
    /// Requires tenant-wide publication.read permission and catalog.read when OAuth scopes are enforced. Returns only the current tenant's exact configured hostnames and discovery modes, sorted by hostname. The deployment permits at most 64 hosts, so this complete inventory is not paginated. Disabled hosts remain visible but cannot be used for publication. Configuration choices are not availability checks or discovery URLs; publication commands recheck current host and tenant authority.
    ///
    /// `GET /api/v1/publication/hosts`
    pub async fn list_publication_hosts(
        &self,
    ) -> Result<Response<PublicationHostInventory>, ApiOpError<ListPublicationHostsApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/publication/hosts");
        let mut req = self.http_client.get(request_url);
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListPublicationHostsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListPublicationHostsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List publication snapshots
    ///
    /// Returns cursor-paginated snapshot history for one exact configured discovery host in the authenticated tenant. The optional exact state filter applies before pagination; use state=active and limit=1 for an observed active snapshot. An empty page means no matching snapshot at this read. A cursor is bound to the selected state, host, tenant, current authorization and page size.
    ///
    /// `GET /api/v1/publication/snapshots`
    pub async fn list_publication_snapshots(
        &self,
        host: impl AsRef<str>,
        state: Option<ListPublicationSnapshotsState>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<PublicationSnapshotPage>, ApiOpError<ListPublicationSnapshotsApiError>>
    {
        let request_url = format!("{}{}", self.base_url, "/api/v1/publication/snapshots");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            query_params.push(("host".to_string(), host.as_ref().to_string()));
            if let Some(v) = state {
                query_params.push(("state".to_string(), v.to_string()));
            }
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListPublicationSnapshotsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListPublicationSnapshotsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List publishers
    ///
    /// Returns a cursor-paginated page of publisher identities visible in the authenticated tenant. Optional exact displayName and declaredDomain filters are intersected before limits and bound to continuation. Names preserve exact case and spacing and are not unique. Domains are trimmed, lowercased and stripped of one trailing dot; only declared domains match, independently of publication approval.
    ///
    /// `GET /api/v1/publishers`
    pub async fn list_publishers(
        &self,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
        display_name: Option<impl AsRef<str>>,
        declared_domain: Option<impl AsRef<str>>,
    ) -> Result<Response<PublisherPage>, ApiOpError<ListPublishersApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/publishers");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = display_name {
                query_params.push(("displayName".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = declared_domain {
                query_params.push(("declaredDomain".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListPublishersApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListPublishersApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List opinions for moderation
    ///
    /// Human opinions use exact version and source-generation identity. They do not change relevance, trust or publication approval. Own rating submission requires an active human and current target visibility. Retained own-list reads and withdrawal remain available after target visibility loss. Administrator actions cannot choose a score.  OAuth scope when enforced: tenant.admin.
    ///
    /// `GET /api/v1/ratings/inbox`
    pub async fn list_rating_inbox(
        &self,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
        state: Option<ListRatingInboxState>,
    ) -> Result<Response<RatingPage>, ApiOpError<ListRatingInboxApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/ratings/inbox");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = state {
                query_params.push(("state".to_string(), v.to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListRatingInboxApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListRatingInboxApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List listings
    ///
    /// Requires current resource.read_history at the canonical parent namespace. Results use ascending immutable UUID order and bounded parent-scoped pages. Revision histories contain summaries; exact content is read separately.
    ///
    /// `GET /api/v1/resources/{resourceId}/listings`
    pub async fn list_resource_listings(
        &self,
        resource_id: impl AsRef<str>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<ListResourceListingsResponse>, ApiOpError<ListResourceListingsApiError>>
    {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/resources/{}/listings",
                __pct_encode_path_segment(resource_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListResourceListingsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListResourceListingsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List versions
    ///
    /// Requires current resource.read_history at the canonical parent namespace. Results use ascending immutable UUID order and bounded parent-scoped pages. Revision histories contain summaries; exact content is read separately.
    ///
    /// `GET /api/v1/resources/{resourceId}/versions`
    pub async fn list_resource_versions(
        &self,
        resource_id: impl AsRef<str>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<ListResourceVersionsResponse>, ApiOpError<ListResourceVersionsApiError>>
    {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/resources/{}/versions",
                __pct_encode_path_segment(resource_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListResourceVersionsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListResourceVersionsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List local Resources
    ///
    /// Returns a cursor-paginated page selected by current resource.read permission at each Resource namespace before ordering and limits. A policy change invalidates continuation. This authoring inventory is distinct from the governed discovery catalog.
    ///
    /// `GET /api/v1/resources`
    pub async fn list_resources(
        &self,
        canonical_identifier: Option<impl AsRef<str>>,
        kind: Option<ResourceKind>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<ResourcePage>, ApiOpError<ListResourcesApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/resources");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = canonical_identifier {
                query_params.push(("canonicalIdentifier".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = kind {
                query_params.push(("kind".to_string(), v.to_string()));
            }
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListResourcesApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListResourcesApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List review submissions
    ///
    /// Requires catalog.read when scopes are enforced. Current resource.read_history predicates select the authorized namespace population before pagination. Filter by listing and state; results use ascending immutable UUID order. A listing filter additionally requires history authority at that canonical namespace. Optional canonicalIdentifier and submittedDisplayName are independently projected under current resource.read and resource.read_content permissions. Names come from the exact submitted listing revision; missing context does not remove an otherwise authorized submission. Set unreleased=true only with state=approved to exclude every submission referenced by a release, including retired or revoked releases. False or omission adds no release filter. The filter binds continuation.
    ///
    /// `GET /api/v1/review-submissions`
    pub async fn list_review_submissions(
        &self,
        unreleased: Option<bool>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
        listing_id: Option<impl AsRef<str>>,
        state: Option<ListReviewSubmissionsState>,
    ) -> Result<Response<ListReviewSubmissionsResponse>, ApiOpError<ListReviewSubmissionsApiError>>
    {
        let request_url = format!("{}{}", self.base_url, "/api/v1/review-submissions");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = unreleased {
                query_params.push(("unreleased".to_string(), v.to_string()));
            }
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = listing_id {
                query_params.push(("listingId".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = state {
                query_params.push(("state".to_string(), v.to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListReviewSubmissionsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListReviewSubmissionsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List tenant role assignments
    ///
    /// Returns a cursor-paginated projection of direct assignments to the eight exact CatalogRole built-in templates, regardless of which assignment API created them. Includes active and revoked tenant and namespace grants across the current tenant's realms. Custom roles, group targets and other templates are excluded before paging. Use membershipId to restrict the page to one membership.
    ///
    /// `GET /api/v1/role-assignments`
    pub async fn list_role_assignments(
        &self,
        membership_id: Option<impl AsRef<str>>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<ListRoleAssignmentsResponse>, ApiOpError<ListRoleAssignmentsApiError>>
    {
        let request_url = format!("{}{}", self.base_url, "/api/v1/role-assignments");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = membership_id {
                query_params.push(("membershipId".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListRoleAssignmentsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListRoleAssignmentsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List roles
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Role and assignment views are tenant-bounded. Group views remain in the administrator's identity realm. The signed cursor binds the actor, authorization revision, filters and page limit. Exact name and state filters intersect with AND before selection. Display spelling is case-sensitive; duplicate names remain separate rows. Restart pagination after authority changes.
    ///
    /// `GET /api/v1/access/roles`
    pub async fn list_role_definitions(
        &self,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
        name: Option<impl AsRef<str>>,
        state: Option<ListRoleDefinitionsState>,
        key: Option<impl AsRef<str>>,
    ) -> Result<Response<RoleDefinitionPage>, ApiOpError<ListRoleDefinitionsApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/access/roles");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = name {
                query_params.push(("name".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = state {
                query_params.push(("state".to_string(), v.to_string()));
            }
            if let Some(v) = key {
                query_params.push(("key".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListRoleDefinitionsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListRoleDefinitionsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List retained assignment impact for a role
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Role and assignment views are tenant-bounded. Group views remain in the administrator's identity realm. The signed cursor binds the actor, authorization revision, filters and page limit. Restart pagination after authority changes.
    ///
    /// `GET /api/v1/access/roles/{roleId}/assignments`
    pub async fn list_role_impact(
        &self,
        role_id: impl AsRef<str>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<AccessAssignmentPage>, ApiOpError<ListRoleImpactApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/access/roles/{}/assignments",
                __pct_encode_path_segment(role_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListRoleImpactApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListRoleImpactApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List version revisions
    ///
    /// Requires current resource.read_history at the canonical parent namespace. Results use ascending immutable UUID order and bounded parent-scoped pages. Revision histories contain summaries; exact content is read separately.
    ///
    /// `GET /api/v1/resource-versions/{versionId}/revisions`
    pub async fn list_version_revisions(
        &self,
        version_id: impl AsRef<str>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
    ) -> Result<Response<ListVersionRevisionsResponse>, ApiOpError<ListVersionRevisionsApiError>>
    {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/resource-versions/{}/revisions",
                __pct_encode_path_segment(version_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListVersionRevisionsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListVersionRevisionsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List webhook deliveries
    ///
    /// Returns cursor-paginated delivery evidence for the endpoint. Exact deliveryId, eventId and state filters intersect before the page limit. Continuations bind these filters, endpoint, tenant and current authorization. Each row is the latest retained endpoint/event outcome, not a history of all external sends. Mutable outcomes can move between pages; refresh from the first page to reconcile, and account for retention gaps. The returned page and required private-read audit commit atomically. Delivery status is separate from the originating Catalog transaction and does not disclose signing secrets or receiver bodies.
    ///
    /// `GET /api/v1/webhooks/{webhookId}/deliveries`
    pub async fn list_webhook_deliveries(
        &self,
        webhook_id: impl AsRef<str>,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
        delivery_id: Option<impl AsRef<str>>,
        event_id: Option<impl AsRef<str>>,
        state: Option<ListWebhookDeliveriesState>,
    ) -> Result<Response<WebhookDeliveryPage>, ApiOpError<ListWebhookDeliveriesApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/webhooks/{}/deliveries",
                __pct_encode_path_segment(webhook_id.as_ref())
            )
        );
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = delivery_id {
                query_params.push(("deliveryId".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = event_id {
                query_params.push(("eventId".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = state {
                query_params.push(("state".to_string(), v.to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListWebhookDeliveriesApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListWebhookDeliveriesApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// List webhook endpoints
    ///
    /// Returns a cursor-paginated page of webhook endpoints for the authenticated tenant. Exact case-sensitive name and status filters intersect before the page limit. Names are not unique; use the existing exact-ID read for one endpoint. Continuations bind filters, tenant and current authorization. Signing secrets are never included.
    ///
    /// `GET /api/v1/webhooks`
    pub async fn list_webhooks(
        &self,
        limit: Option<i64>,
        cursor: Option<impl AsRef<str>>,
        name: Option<impl AsRef<str>>,
        status: Option<ListWebhooksStatus>,
    ) -> Result<Response<WebhookPage>, ApiOpError<ListWebhooksApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/webhooks");
        let mut req = self.http_client.get(request_url);
        {
            let mut query_params: Vec<(String, String)> = Vec::new();
            if let Some(v) = limit {
                query_params.push(("limit".to_string(), v.to_string()));
            }
            if let Some(v) = cursor {
                query_params.push(("cursor".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = name {
                query_params.push(("name".to_string(), v.as_ref().to_string()));
            }
            if let Some(v) = status {
                query_params.push(("status".to_string(), v.to_string()));
            }
            if !query_params.is_empty() {
                req = req.query(&query_params);
            }
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ListWebhooksApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ListWebhooksApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Diagnose approved live Registries
    ///
    /// Administrator-facing diagnostic search over already configured live sources. It cannot introduce a source and is not the ordinary unified Catalog search contract.
    ///
    /// `POST /api/v1/federation/sources:live-search`
    pub async fn live_federated_search(
        &self,
        idempotency_key: impl AsRef<str>,
        request: LiveSearch,
    ) -> Result<Response<LiveSearchResponse>, ApiOpError<LiveFederatedSearchApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url, "/api/v1/federation/sources:live-search"
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<LiveFederatedSearchApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(LiveFederatedSearchApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Permanently offboard a membership and atomically revoke its active grants
    ///
    /// Ends the membership permanently and revokes its active role and namespace grants atomically. Requires the current revision and a reason, and preserves the final active tenant administrator.
    ///
    /// `POST /api/v1/memberships/{membershipId}:offboard`
    pub async fn offboard_membership(
        &self,
        membership_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<MembershipLifecycleResult>, ApiOpError<OffboardMembershipApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/memberships/{}:offboard",
                __pct_encode_path_segment(membership_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<OffboardMembershipApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(OffboardMembershipApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Pause a federation source
    ///
    /// Pauses scheduled synchronization and live participation with the current source ETag and a reason, preserving retained observations.
    ///
    /// `POST /api/v1/federation/sources/{sourceId}:pause`
    pub async fn pause_federation_source(
        &self,
        source_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<FederationSource>, ApiOpError<PauseFederationSourceApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/sources/{}:pause",
                __pct_encode_path_segment(source_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<PauseFederationSourceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(PauseFederationSourceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Permit an opinion
    ///
    /// Human opinions use exact version and source-generation identity. They do not change relevance, trust or publication approval. Own rating submission requires an active human and current target visibility. Retained own-list reads and withdrawal remain available after target visibility loss. Administrator actions cannot choose a score.  OAuth scope when enforced: tenant.admin.
    ///
    /// `POST /api/v1/ratings/{ratingId}:permit`
    pub async fn permit_rating(
        &self,
        rating_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: RatingModeration,
    ) -> Result<Response<RatingReceipt>, ApiOpError<PermitRatingApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/ratings/{}:permit",
                __pct_encode_path_segment(rating_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<PermitRatingApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(PermitRatingApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Check an exact MCP browser origin
    ///
    /// Requires an approved Origin, POST request method and an admitted header set. Disabled routes return 404. Does not enable cookie authentication or global API CORS.
    ///
    /// `OPTIONS /mcp`
    pub async fn preflight_catalog_mcp(
        &self,
    ) -> Result<Response<()>, ApiOpError<PreflightCatalogMcpApiError>> {
        let request_url = format!("{}{}", self.base_url, "/mcp");
        let req = self
            .http_client
            .request(reqwest::Method::OPTIONS, request_url);
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 204u16 {
            let _ = body_text;
            let _ = raw_body;
            let _ = headers;
            Ok(Response::new((), status_code, headers, raw_body))
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "204",
                )),
            }))
        } else {
            let typed: Option<PreflightCatalogMcpApiError>;
            let parse_error: Option<String>;
            match status_code {
                400u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(PreflightCatalogMcpApiError::Status400(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                403u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(PreflightCatalogMcpApiError::Status403(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                404u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(PreflightCatalogMcpApiError::Status404(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                _ => {
                    typed = None;
                    parse_error = None;
                }
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Check an exact metadata browser origin
    ///
    /// `OPTIONS /.well-known/oauth-protected-resource/mcp`
    pub async fn preflight_catalog_mcpo_auth_metadata(
        &self,
    ) -> Result<Response<()>, ApiOpError<PreflightCatalogMcpoAuthMetadataApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url, "/.well-known/oauth-protected-resource/mcp"
        );
        let req = self
            .http_client
            .request(reqwest::Method::OPTIONS, request_url);
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 204u16 {
            let _ = body_text;
            let _ = raw_body;
            let _ = headers;
            Ok(Response::new((), status_code, headers, raw_body))
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "204",
                )),
            }))
        } else {
            let typed: Option<PreflightCatalogMcpoAuthMetadataApiError>;
            let parse_error: Option<String>;
            match status_code {
                400u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(PreflightCatalogMcpoAuthMetadataApiError::Status400(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                403u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(PreflightCatalogMcpoAuthMetadataApiError::Status403(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                404u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(PreflightCatalogMcpoAuthMetadataApiError::Status404(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                _ => {
                    typed = None;
                    parse_error = None;
                }
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Preview a federation source change
    ///
    /// Computes the impact of a proposed update or removal against the current source and observations. The returned expiring digest binds a later mutation to that exact preview.
    ///
    /// `POST /api/v1/federation/sources/{sourceId}/change-previews`
    pub async fn preview_federation_source_change(
        &self,
        source_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        request: PreviewSourceChange,
    ) -> Result<Response<FederationChangePreview>, ApiOpError<PreviewFederationSourceChangeApiError>>
    {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/sources/{}/change-previews",
                __pct_encode_path_segment(source_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<PreviewFederationSourceChangeApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(PreviewFederationSourceChangeApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Publish an approved Catalog listing
    ///
    /// Rechecks current approved validation evidence before the publication transaction. URL-backed representations additionally require a fresh anonymous fetch through the deployment-owned public route whose status, media type, and exact bytes still match the reviewed PostgreSQL binding.
    ///
    /// `POST /api/v1/catalog-listings/{listingId}:publish`
    pub async fn publish_catalog_listing(
        &self,
        listing_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: PublishListing,
    ) -> Result<Response<ListingPublished>, ApiOpError<PublishCatalogListingApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/catalog-listings/{}:publish",
                __pct_encode_path_segment(listing_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<PublishCatalogListingApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(PublishCatalogListingApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Reactivate group
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Assignment grants, revocations and group-member writes stay within the same identity realm; membership inventory and lifecycle are tenant-wide. Built-in roles are immutable. Custom roles cannot grant access.manage. Writes and private audit evidence commit atomically. Public notifications do not expose the permission graph.
    ///
    /// `POST /api/v1/access/groups/{groupId}:reactivate`
    pub async fn reactivate_access_group(
        &self,
        group_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<AccessGroup>, ApiOpError<ReactivateAccessGroupApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/access/groups/{}:reactivate",
                __pct_encode_path_segment(group_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ReactivateAccessGroupApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ReactivateAccessGroupApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Reactivate a suspended membership in an active identity realm
    ///
    /// Restores a suspended membership only while its identity realm is active. Requires the current membership revision and a reason; permanently offboarded memberships cannot be reactivated.
    ///
    /// `POST /api/v1/memberships/{membershipId}:reactivate`
    pub async fn reactivate_membership(
        &self,
        membership_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<MembershipLifecycleResult>, ApiOpError<ReactivateMembershipApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/memberships/{}:reactivate",
                __pct_encode_path_segment(membership_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ReactivateMembershipApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ReactivateMembershipApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Read an admitted artwork slot
    ///
    /// Authenticated read-only POST for a bounded target and slot. No Idempotency-Key is required. Never contacts a peer to repeat Search or persist image bytes. Current resource/federation authority and source fences apply before credential-free image delivery.
    ///
    /// `POST /api/v1/artwork/read`
    pub async fn read_artwork(
        &self,
        request: ArtworkRead,
    ) -> Result<Response<bytes::Bytes>, ApiOpError<ReadArtworkApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/artwork/read");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header(reqwest::header::ACCEPT, "image/jpeg");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        if false || status_code == 200u16 {
            Ok(Response::new(
                bytes::Bytes::from(body_bytes.clone()),
                status_code,
                headers,
                body_bytes,
            ))
        } else {
            let raw_body = body_bytes;
            let body_text = String::from_utf8_lossy(&raw_body).into_owned();
            if status.is_success() {
                return Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!(
                        "unexpected successful status {}; generated return type selects `{}`",
                        status_code, "200",
                    )),
                }));
            }
            let typed: Option<ReadArtworkApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ReadArtworkApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Read an admitted artwork slot
    ///
    /// Authenticated read-only POST for a bounded target and slot. No Idempotency-Key is required. Never contacts a peer to repeat Search or persist image bytes. Current resource/federation authority and source fences apply before credential-free image delivery.
    ///
    /// `POST /api/v1/artwork/read`
    ///Select `image/jpeg` as binary_stream; accepts response statuses 200. Other successful statuses and mismatched Content-Type values are returned as inspectable API errors.
    /// Read an admitted artwork slot
    ///
    /// Authenticated read-only POST for a bounded target and slot. No Idempotency-Key is required. Never contacts a peer to repeat Search or persist image bytes. Current resource/federation authority and source fences apply before credential-free image delivery.
    ///
    /// `POST /api/v1/artwork/read`
    ///Select `image/png` as buffered; accepts response statuses 200. Other successful statuses and mismatched Content-Type values are returned as inspectable API errors.
    pub async fn read_artwork_binary_image_png(
        &self,
        request: ArtworkRead,
    ) -> Result<Response<bytes::Bytes>, ApiOpError<ReadArtworkApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/artwork/read");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header(reqwest::header::ACCEPT, "image/png");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        if false || status_code == 200u16 {
            let actual = response
                .headers()
                .get(reqwest::header::CONTENT_TYPE)
                .and_then(|value| value.to_str().ok())
                .unwrap_or("")
                .to_string();
            let expected = "image/png";
            let parse_media = |value: &str| {
                let mut parts = Vec::new();
                let mut start = 0;
                let mut quoted = false;
                let mut escaped = false;
                for (index, character) in value.char_indices() {
                    if escaped {
                        escaped = false;
                        continue;
                    }
                    if character == '\\' && quoted {
                        escaped = true;
                        continue;
                    }
                    if character == '"' {
                        quoted = !quoted;
                    }
                    if character == ';' && !quoted {
                        parts.push(&value[start..index]);
                        start = index + 1;
                    }
                }
                if quoted || escaped {
                    return None;
                }
                parts.push(&value[start..]);
                let essence = parts
                    .first()
                    .copied()
                    .unwrap_or("")
                    .trim()
                    .to_ascii_lowercase();
                let mut parameters = std::collections::BTreeMap::new();
                for part in parts.into_iter().skip(1) {
                    let (name, value) = part.split_once('=')?;
                    let name = name.trim().to_ascii_lowercase();
                    if name.is_empty() {
                        return None;
                    }
                    let value = value.trim();
                    let value = if let Some(quoted) = value.strip_prefix('"') {
                        let inner = quoted.strip_suffix('"')?;
                        let mut decoded = String::new();
                        let mut escaped = false;
                        for character in inner.chars() {
                            if escaped {
                                decoded.push(character);
                                escaped = false;
                            } else if character == '\\' {
                                escaped = true;
                            } else if character == '"' {
                                return None;
                            } else {
                                decoded.push(character);
                            }
                        }
                        if escaped {
                            return None;
                        }
                        decoded
                    } else {
                        if value.contains('"') {
                            return None;
                        }
                        value.to_string()
                    };
                    if parameters.insert(name, value).is_some() {
                        return None;
                    }
                }
                Some((essence, parameters))
            };
            let matches = match (parse_media(&actual), parse_media(expected)) {
                (
                    Some((actual_essence, actual_parameters)),
                    Some((expected_essence, expected_parameters)),
                ) => {
                    let essence_matches = if expected_essence == "*/*" {
                        actual_essence.contains('/')
                    } else if let Some(prefix) = expected_essence.strip_suffix("/*") {
                        actual_essence.split('/').next() == Some(prefix)
                    } else {
                        actual_essence == expected_essence
                    };
                    essence_matches
                        && expected_parameters.iter().all(|(name, value)| {
                            actual_parameters.get(name).is_some_and(|actual| {
                                if name == "charset" {
                                    actual.eq_ignore_ascii_case(value)
                                } else {
                                    actual == value
                                }
                            })
                        })
                }
                _ => false,
            };
            if !matches {
                let headers = response.headers().clone();
                let raw_body = response.body.clone();
                let body_text = String::from_utf8_lossy(&raw_body).into_owned();
                let typed: Option<ReadArtworkApiError>;
                let parse_error: Option<String>;
                match status_code {
                    _ => match serde_json::from_slice::<Problem>(&raw_body) {
                        Ok(v) => {
                            typed = Some(ReadArtworkApiError::Default(v));
                            parse_error = None;
                        }
                        Err(e) => {
                            typed = None;
                            parse_error = Some(e.to_string());
                        }
                    },
                }
                let _ = parse_error;
                return Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers,
                    body: body_text,
                    raw_body,
                    typed,
                    parse_error: Some(format!(
                        "unexpected response Content-Type `{}`; expected `{}`",
                        actual, expected
                    )),
                }));
            }
        }
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        if false || status_code == 200u16 {
            Ok(Response::new(
                bytes::Bytes::from(body_bytes.clone()),
                status_code,
                headers,
                body_bytes,
            ))
        } else {
            let raw_body = body_bytes;
            let body_text = String::from_utf8_lossy(&raw_body).into_owned();
            if status.is_success() {
                return Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!(
                        "unexpected successful status {}; generated return type selects `{}`",
                        status_code, "200",
                    )),
                }));
            }
            let typed: Option<ReadArtworkApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ReadArtworkApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Read an admitted artwork slot
    ///
    /// Authenticated read-only POST for a bounded target and slot. No Idempotency-Key is required. Never contacts a peer to repeat Search or persist image bytes. Current resource/federation authority and source fences apply before credential-free image delivery.
    ///
    /// `POST /api/v1/artwork/read`
    ///Select `image/png` as binary_stream; accepts response statuses 200. Other successful statuses and mismatched Content-Type values are returned as inspectable API errors.
    /// Read the current rating summary and your opinion
    ///
    /// Human opinions use exact version and source-generation identity. They do not change relevance, trust or publication approval. Own rating submission requires an active human and current target visibility. Retained own-list reads and withdrawal remain available after target visibility loss. Administrator actions cannot choose a score. This read-only POST does not require Idempotency-Key. OAuth scope when enforced: catalog.read.
    ///
    /// `POST /api/v1/ratings/read`
    pub async fn read_rating(
        &self,
        request: RatingRequest,
    ) -> Result<Response<RatingView>, ApiOpError<ReadRatingApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/ratings/read");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ReadRatingApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ReadRatingApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Refuse deletion because MCP has no retained sessions
    ///
    /// `DELETE /mcp`
    pub async fn refuse_catalog_mcp_session_deletion(
        &self,
    ) -> Result<Response<()>, ApiOpError<RefuseCatalogMcpSessionDeletionApiError>> {
        let request_url = format!("{}{}", self.base_url, "/mcp");
        let req = self.http_client.delete(request_url);
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "any declared 2xx response",
                )),
            }))
        } else {
            let typed: Option<RefuseCatalogMcpSessionDeletionApiError>;
            let parse_error: Option<String>;
            match status_code {
                400u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RefuseCatalogMcpSessionDeletionApiError::Status400(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                403u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RefuseCatalogMcpSessionDeletionApiError::Status403(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                404u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RefuseCatalogMcpSessionDeletionApiError::Status404(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                405u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RefuseCatalogMcpSessionDeletionApiError::Status405(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                _ => {
                    typed = None;
                    parse_error = None;
                }
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Refuse a standalone MCP stream
    ///
    /// `GET /mcp`
    pub async fn refuse_catalog_mcp_stream(
        &self,
    ) -> Result<Response<()>, ApiOpError<RefuseCatalogMcpStreamApiError>> {
        let request_url = format!("{}{}", self.base_url, "/mcp");
        let req = self.http_client.get(request_url);
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "any declared 2xx response",
                )),
            }))
        } else {
            let typed: Option<RefuseCatalogMcpStreamApiError>;
            let parse_error: Option<String>;
            match status_code {
                403u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RefuseCatalogMcpStreamApiError::Status403(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                404u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RefuseCatalogMcpStreamApiError::Status404(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                405u16 => match serde_json::from_slice::<MCPReply>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RefuseCatalogMcpStreamApiError::Status405(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                _ => {
                    typed = None;
                    parse_error = None;
                }
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Reindex a Catalog search scope
    ///
    /// Requires tenant-level operations.reindex and, when scope enforcement is enabled, the tenant.admin OAuth scope. Rebuilds the authenticated tenant's control search index, or the specified active publication snapshot's public index, using the deployment-configured model. Uses existing Job deduplication, audit evidence and idempotency. Disabled or excluded tenants receive a dependency-unavailable error. No provider or model override is accepted.
    ///
    /// `POST /api/v1/catalog:reindex`
    pub async fn reindex_catalog(
        &self,
        idempotency_key: impl AsRef<str>,
        request: ReindexCatalogRequest,
    ) -> Result<Response<Job>, ApiOpError<ReindexCatalogApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/catalog:reindex");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 202u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "202",
                )),
            }))
        } else {
            let typed: Option<ReindexCatalogApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ReindexCatalogApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Reject a review submission
    ///
    /// Records a rejection and its reason against the current submission revision, preserving the submission and review evidence. Independent review resolves immutable submission audit attribution; the originating membership or the same tenant/realm/subject cannot decide. An unrelated equal subject in another realm is eligible with current permissions. Missing or ambiguous attribution refuses the decision.
    ///
    /// `POST /api/v1/review-submissions/{submissionId}:reject`
    pub async fn reject_review_submission(
        &self,
        submission_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<ReviewDecision>, ApiOpError<RejectReviewSubmissionApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/review-submissions/{}:reject",
                __pct_encode_path_segment(submission_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<RejectReviewSubmissionApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RejectReviewSubmissionApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Remove a federation source
    ///
    /// Remove a source using the current ETag, an audit reason and the digest of a matching removal preview. Observations remain historical evidence; this does not delete adopted local resources.
    ///
    /// `POST /api/v1/federation/sources/{sourceId}:remove`
    pub async fn remove_federation_source(
        &self,
        source_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: RemoveSource,
    ) -> Result<Response<()>, ApiOpError<RemoveFederationSourceApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/sources/{}:remove",
                __pct_encode_path_segment(source_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 204u16 {
            let _ = body_text;
            let _ = raw_body;
            let _ = headers;
            Ok(Response::new((), status_code, headers, raw_body))
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "204",
                )),
            }))
        } else {
            let typed: Option<RemoveFederationSourceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RemoveFederationSourceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Remove feedback text
    ///
    /// Requires feedback.moderate and tenant.admin scope when enforced. Available after submitter offboarding; clears both text fields and makes the record terminal. The receipt contains no private text. Read current state separately with GET.
    ///
    /// `POST /api/v1/feedback/{feedbackId}:remove`
    pub async fn remove_feedback(
        &self,
        feedback_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: FeedbackEmptyCommand,
    ) -> Result<Response<FeedbackReceipt>, ApiOpError<RemoveFeedbackApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/feedback/{}:remove",
                __pct_encode_path_segment(feedback_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<RemoveFeedbackApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RemoveFeedbackApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Remove an opinion
    ///
    /// Human opinions use exact version and source-generation identity. They do not change relevance, trust or publication approval. Own rating submission requires an active human and current target visibility. Retained own-list reads and withdrawal remain available after target visibility loss. Administrator actions cannot choose a score.  OAuth scope when enforced: tenant.admin.
    ///
    /// `POST /api/v1/ratings/{ratingId}:remove`
    pub async fn remove_rating(
        &self,
        rating_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: RatingModeration,
    ) -> Result<Response<RatingReceipt>, ApiOpError<RemoveRatingApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/ratings/{}:remove",
                __pct_encode_path_segment(rating_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<RemoveRatingApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RemoveRatingApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Reopen feedback
    ///
    /// Requires feedback.moderate and tenant.admin scope when enforced. Reopens and clears the previous response without rewriting the comment. The receipt contains no private text. Read current state separately with GET.
    ///
    /// `POST /api/v1/feedback/{feedbackId}:reopen`
    pub async fn reopen_feedback(
        &self,
        feedback_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: FeedbackEmptyCommand,
    ) -> Result<Response<FeedbackReceipt>, ApiOpError<ReopenFeedbackApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/feedback/{}:reopen",
                __pct_encode_path_segment(feedback_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ReopenFeedbackApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ReopenFeedbackApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Request review changes
    ///
    /// Records the reviewer's reason for requesting changes against the current submission revision. Submitted immutable evidence is not rewritten. Independent review resolves immutable submission audit attribution; the originating membership or the same tenant/realm/subject cannot decide. An unrelated equal subject in another realm is eligible with current permissions. Missing or ambiguous attribution refuses the decision.
    ///
    /// `POST /api/v1/review-submissions/{submissionId}:request-changes`
    pub async fn request_review_changes(
        &self,
        submission_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<ReviewDecision>, ApiOpError<RequestReviewChangesApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/review-submissions/{}:request-changes",
                __pct_encode_path_segment(submission_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<RequestReviewChangesApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RequestReviewChangesApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Assess one exact approved external entry
    ///
    /// Requires current tenant source.manage, source.read_evidence and federation.discover permissions. The optional deployment capability and selected source policy must be enabled. Select indexed evidence by observationId, or live evidence by text. Live selection contacts only this source, before enqueue and outside an interactive transaction. Exact-byte digest, version presence, native type and delivery must match. A replay does not contact peers. This command does not publish, adopt or approve software.
    ///
    /// `POST /api/v1/federation/sources/{sourceId}/trust-assessments`
    pub async fn request_trust_assessment(
        &self,
        source_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        request: TrustAssessmentRequest,
    ) -> Result<Response<Job>, ApiOpError<RequestTrustAssessmentApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/sources/{}/trust-assessments",
                __pct_encode_path_segment(source_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 202u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "202",
                )),
            }))
        } else {
            let typed: Option<RequestTrustAssessmentApiError>;
            let parse_error: Option<String>;
            match status_code {
                400u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RequestTrustAssessmentApiError::Status400(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                401u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RequestTrustAssessmentApiError::Status401(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                403u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RequestTrustAssessmentApiError::Status403(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                404u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RequestTrustAssessmentApiError::Status404(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                409u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RequestTrustAssessmentApiError::Status409(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                412u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RequestTrustAssessmentApiError::Status412(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                503u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RequestTrustAssessmentApiError::Status503(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                _ => {
                    typed = None;
                    parse_error = None;
                }
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Resolve an exact saved resource without live discovery
    ///
    /// Requires an active human membership. Requires library.own and exact tenant/realm/membership ownership; administrators cannot select another owner. Owner reads, rename and removal remain available after target access loss. OAuth scope when enforced: catalog.read. Responses are no-store. Cursors bind principal, filters and current policy. Resolve never makes a live request or substitutes a different release/source/generation.
    ///
    /// `POST /api/v1/personal/items/{itemId}/resolve`
    pub async fn resolve_personal_item(
        &self,
        item_id: impl AsRef<str>,
        request: ResolvePersonalItemRequest,
    ) -> Result<Response<PersonalResolution>, ApiOpError<ResolvePersonalItemApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/personal/items/{}/resolve",
                __pct_encode_path_segment(item_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ResolvePersonalItemApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ResolvePersonalItemApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Resume a federation source
    ///
    /// Resumes an eligible paused source under its reviewed configuration with the current source ETag and a reason.
    ///
    /// `POST /api/v1/federation/sources/{sourceId}:resume`
    pub async fn resume_federation_source(
        &self,
        source_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<FederationSource>, ApiOpError<ResumeFederationSourceApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/sources/{}:resume",
                __pct_encode_path_segment(source_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<ResumeFederationSourceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ResumeFederationSourceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Retire a listing release
    ///
    /// Retires an eligible release with a reason and the current release ETag. This lifecycle change does not delete retained release history.
    ///
    /// `POST /api/v1/listing-releases/{releaseId}:retire`
    pub async fn retire_listing_release(
        &self,
        release_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: ReleaseTransition,
    ) -> Result<Response<ListingRelease>, ApiOpError<RetireListingReleaseApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/listing-releases/{}:retire",
                __pct_encode_path_segment(release_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<RetireListingReleaseApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RetireListingReleaseApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Retry an eligible Job
    ///
    /// Creates an attributable retry for an eligible Job with an audit reason. Follow the returned Job location; acceptance is not evidence of completion.
    ///
    /// `POST /api/v1/jobs/{jobId}:retry`
    pub async fn retry_job(
        &self,
        job_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<Job>, ApiOpError<RetryJobApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/jobs/{}:retry",
                __pct_encode_path_segment(job_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 202u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "202",
                )),
            }))
        } else {
            let typed: Option<RetryJobApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RetryJobApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Revoke a retained scoped assignment
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Assignment grants, revocations and group-member writes stay within the same identity realm; membership inventory and lifecycle are tenant-wide. Built-in roles are immutable. Custom roles cannot grant access.manage. Writes and private audit evidence commit atomically. Public notifications do not expose the permission graph.
    ///
    /// `POST /api/v1/access/assignments/{assignmentId}:revoke`
    pub async fn revoke_access_assignment(
        &self,
        assignment_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<AccessAssignment>, ApiOpError<RevokeAccessAssignmentApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/access/assignments/{}:revoke",
                __pct_encode_path_segment(assignment_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<RevokeAccessAssignmentApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RevokeAccessAssignmentApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Revoke a retained group member relation
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Assignment grants, revocations and group-member writes stay within the same identity realm; membership inventory and lifecycle are tenant-wide. Built-in roles are immutable. Custom roles cannot grant access.manage. Writes and private audit evidence commit atomically. Public notifications do not expose the permission graph.
    ///
    /// `POST /api/v1/access/groups/{groupId}/members/{memberId}:revoke`
    pub async fn revoke_access_group_member(
        &self,
        group_id: impl AsRef<str>,
        member_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<AccessGroupMember>, ApiOpError<RevokeAccessGroupMemberApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/access/groups/{}/members/{}:revoke",
                __pct_encode_path_segment(group_id.as_ref()),
                __pct_encode_path_segment(member_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<RevokeAccessGroupMemberApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RevokeAccessGroupMemberApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Revoke a listing release
    ///
    /// Revokes an eligible release with a reason and the current release ETag while preserving its retained evidence.
    ///
    /// `POST /api/v1/listing-releases/{releaseId}:revoke`
    pub async fn revoke_listing_release(
        &self,
        release_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: ReleaseTransition,
    ) -> Result<Response<ListingRelease>, ApiOpError<RevokeListingReleaseApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/listing-releases/{}:revoke",
                __pct_encode_path_segment(release_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<RevokeListingReleaseApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RevokeListingReleaseApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Revoke a publisher domain from publication
    ///
    /// Requires publisher.approve_domain and publishers.manage when OAuth scopes are enforced. Removes the reason-bearing publication approval, identifies affected releases, and atomically withdraws active discovery snapshots for configured hosts that contain the domain. Discovery reads remain snapshot-only and fail closed after withdrawal.
    ///
    /// `POST /api/v1/publishers/{publisherId}/publication-domains/{domain}:revoke`
    pub async fn revoke_publisher_publication_domain(
        &self,
        publisher_id: impl AsRef<str>,
        domain: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<
        Response<PublicationDomainRevocation>,
        ApiOpError<RevokePublisherPublicationDomainApiError>,
    > {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/publishers/{}/publication-domains/{}:revoke",
                __pct_encode_path_segment(publisher_id.as_ref()),
                __pct_encode_path_segment(domain.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<RevokePublisherPublicationDomainApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RevokePublisherPublicationDomainApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Revoke a Catalog-owned role assignment
    ///
    /// Revoke an eligible direct assignment to an exact CatalogRole built-in template in the caller's identity realm, using its current ETag and an audit reason. Unsupported custom, group and other-template targets and assignments in another realm return NOT_FOUND without mutation; use the general access-assignment API for other supported targets in the caller's realm. Final-administrator protection counts active direct tenant administrators across the tenant, not separately per realm.
    ///
    /// `POST /api/v1/role-assignments/{assignmentId}:revoke`
    pub async fn revoke_role_assignment(
        &self,
        assignment_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<RoleAssignment>, ApiOpError<RevokeRoleAssignmentApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/role-assignments/{}:revoke",
                __pct_encode_path_segment(assignment_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<RevokeRoleAssignmentApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RevokeRoleAssignmentApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Roll back to a retained publication snapshot
    ///
    /// Restores an eligible retained snapshot for its configured host after checking the retained snapshot ETag and current publication eligibility.
    ///
    /// `POST /api/v1/publication/snapshots/{snapshotId}:rollback`
    pub async fn rollback_publication_snapshot(
        &self,
        snapshot_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: PublicationTransition,
    ) -> Result<Response<PublicationSnapshot>, ApiOpError<RollbackPublicationSnapshotApiError>>
    {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/publication/snapshots/{}:rollback",
                __pct_encode_path_segment(snapshot_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<RollbackPublicationSnapshotApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RollbackPublicationSnapshotApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Rotate a webhook signing secret
    ///
    /// Rotates signing material using the current endpoint ETag and returns the new secret once. Distribute it to the receiver before retiring any permitted overlap.
    ///
    /// `POST /api/v1/webhooks/{webhookId}:rotate-secret`
    pub async fn rotate_webhook_secret(
        &self,
        webhook_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Option<RotateWebhookSecretRequest>,
    ) -> Result<Response<WebhookSecretResult>, ApiOpError<RotateWebhookSecretApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/webhooks/{}:rotate-secret",
                __pct_encode_path_segment(webhook_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        if let Some(request) = request {
            req = req
                .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
                .header("content-type", "application/json");
        } else {
            req = req.header(reqwest::header::CONTENT_LENGTH, "0");
        }
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<RotateWebhookSecretApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(RotateWebhookSecretApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Search the host-selected ARD Registry
    ///
    /// Authentication is required only when the exact configured discovery host uses authenticated mode. Omitted federation means auto. Core auto Search merges the active local snapshot with up to 50 live candidates from administrator-approved sources. Signed continuations repeat bounded peer traversal while live candidates remain, and bind the unchanged window, snapshot, query, caller and host audience. The initial 15-minute expiry does not extend. A changed window or snapshot requires a restart; a temporary replay failure permits retry of the same token. Unsafe first-page traversal preserves healthy results with durable-only continuation. Hybrid Search returns the durable ranking before a distinct live tail; none and referrals modes continue local results only. A partial live stage is reported through Server-Timing without adding non-ARD members to the response.
    ///
    /// `POST /ard/v1/search`
    pub async fn search_ard_registry(
        &self,
        request: ARDSearch,
    ) -> Result<Response<ARDSearchResponse>, ApiOpError<SearchArdRegistryApiError>> {
        let request_url = format!("{}{}", self.base_url, "/ard/v1/search");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<SearchArdRegistryApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<ARDError>(&raw_body) {
                    Ok(v) => {
                        typed = Some(SearchArdRegistryApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Search the unified authenticated Catalog
    ///
    /// Returns one bounded deterministic merge of authorized local published releases, current indexed observations from administrator-managed sources, and transient live results from eligible administrator-approved sources. Local resource.read predicates precede selection, totals and paging. Without federation.discover, default discovery selects only authorized local records; explicitly selecting indexed or live origins is forbidden before outbound disclosure. Visible current local releases shadow matching external identities only when the local origin participates. For a nonempty core query with durable origins, signed continuations traverse an unchanged live window of at most 50 candidates alongside durable results. Each continuation that needs live results repeats bounded approved peer traversal. A changed window or durable generation requires a restart; a temporary replay failure permits retry of the same cursor. The initial 15-minute expiry does not extend. Hybrid results retain exact durable rank order, followed by a distinct bounded live tail. Live-only Search pages the same bounded window without durable or embedding work. The service selects its core SearchProfile and source topology; clients cannot select sources, providers, models, indexes, weights, or tiers. core-lexical-v1 matches all query tokens across fields using Unicode letters/numbers and simple lowercase, without stemming or accent folding. Core results sort by descending score, then Unicode-whitespace-normalized lowercase display name and stable result ID. Hybrid composite order retains precise durable ranks before live results; each display score remains an integer from 0 to 100. Missing or outdated indexed search projections return DEPENDENCY_UNAVAILABLE until the bounded operator repair completes.
    ///
    /// `POST /api/v1/catalog/search`
    pub async fn search_catalog(
        &self,
        request: CatalogSearch,
    ) -> Result<Response<CatalogSearchResponse>, ApiOpError<SearchCatalogApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/catalog/search");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<SearchCatalogApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(SearchCatalogApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Search indexed observations
    ///
    /// Searches authorized indexed observations using structured filters and pagination. Results retain source provenance and do not imply local adoption or approval.
    ///
    /// `POST /api/v1/observations:search`
    pub async fn search_observations(
        &self,
        request: ObservationSearch,
    ) -> Result<Response<ObservationSearchPage>, ApiOpError<SearchObservationsApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/observations:search");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<SearchObservationsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(SearchObservationsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Set a local consumer artwork override
    ///
    /// Requires source.manage and sources.manage when OAuth scopes are enforced. Validates replacement image facts before the mutation transaction. Initial creation omits If-Match; changes require the current head ETag. Live target admission requires a signed receipt in the body. Both inherit modes explicitly reset presentation; publisher evidence is unchanged.
    ///
    /// `POST /api/v1/federation/sources/{sourceId}/artwork`
    pub async fn set_artwork_override(
        &self,
        source_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: Option<impl AsRef<str>>,
        request: SetArtworkOverride,
    ) -> Result<Response<ArtworkOverride>, ApiOpError<SetArtworkOverrideApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/sources/{}/artwork",
                __pct_encode_path_segment(source_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        if let Some(v) = if_match {
            req = req.header("If-Match", v.as_ref());
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<SetArtworkOverrideApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(SetArtworkOverrideApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Choose whether the audience may receive consented aggregates
    ///
    /// Human opinions use exact version and source-generation identity. They do not change relevance, trust or publication approval. Own rating submission requires an active human and current target visibility. Retained own-list reads and withdrawal remain available after target visibility loss. Administrator actions cannot choose a score.  OAuth scope when enforced: tenant.admin.
    ///
    /// `POST /api/v1/ratings/sharing`
    pub async fn set_rating_sharing(
        &self,
        idempotency_key: impl AsRef<str>,
        if_match: Option<impl AsRef<str>>,
        request: RatingSharingInput,
    ) -> Result<Response<RatingSharing>, ApiOpError<SetRatingSharingApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/ratings/sharing");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        if let Some(v) = if_match {
            req = req.header("If-Match", v.as_ref());
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<SetRatingSharingApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(SetRatingSharingApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Replace the current tenant's operational overrides
    ///
    /// Requires tenant settings.manage and settings.read, with tenant.admin OAuth scope when enforced. Omitted override fields inherit deployment defaults. An empty overrides object resets all fields. Explicit false pauses new automatic source admissions; retained Jobs and manual sync continue. Saves and resets are audited atomically. Exact replay returns the original receipt; read current settings separately.
    ///
    /// `POST /api/v1/tenant-settings`
    pub async fn set_tenant_settings(
        &self,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: SetTenantSettingsRequest,
    ) -> Result<Response<TenantSettings>, ApiOpError<SetTenantSettingsApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/tenant-settings");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<SetTenantSettingsApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(SetTenantSettingsApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Submit a listing for review
    ///
    /// Submits the listing's current revision and its referenced version revision for review. No revision IDs are supplied in the body; submission does not activate a public discovery snapshot.
    ///
    /// `POST /api/v1/catalog-listings/{listingId}/submissions`
    pub async fn submit_listing_for_review(
        &self,
        listing_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        request: Option<SubmitListingForReviewRequest>,
    ) -> Result<Response<ReviewSubmission>, ApiOpError<SubmitListingForReviewApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/catalog-listings/{}/submissions",
                __pct_encode_path_segment(listing_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        if let Some(request) = request {
            req = req
                .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
                .header("content-type", "application/json");
        } else {
            req = req.header(reqwest::header::CONTENT_LENGTH, "0");
        }
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<SubmitListingForReviewApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(SubmitListingForReviewApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Submit or replace your score
    ///
    /// Human opinions use exact version and source-generation identity. They do not change relevance, trust or publication approval. Own rating submission requires an active human and current target visibility. Retained own-list reads and withdrawal remain available after target visibility loss. Administrator actions cannot choose a score.  OAuth scope when enforced: ratings.write.
    ///
    /// `POST /api/v1/ratings`
    pub async fn submit_rating(
        &self,
        idempotency_key: impl AsRef<str>,
        if_match: Option<impl AsRef<str>>,
        request: RatingSubmit,
    ) -> Result<Response<RatingReceipt>, ApiOpError<SubmitRatingApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/ratings");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        if let Some(v) = if_match {
            req = req.header("If-Match", v.as_ref());
        }
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200, 201",
                )),
            }))
        } else {
            let typed: Option<SubmitRatingApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(SubmitRatingApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Supersede a listing release
    ///
    /// Marks an eligible release superseded by the specified replacement release. Requires a reason, the current release ETag and an eligible replacement.
    ///
    /// `POST /api/v1/listing-releases/{releaseId}:supersede`
    pub async fn supersede_listing_release(
        &self,
        release_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: ReleaseTransition,
    ) -> Result<Response<ListingRelease>, ApiOpError<SupersedeListingReleaseApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/listing-releases/{}:supersede",
                __pct_encode_path_segment(release_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<SupersedeListingReleaseApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(SupersedeListingReleaseApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Suspend group
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Assignment grants, revocations and group-member writes stay within the same identity realm; membership inventory and lifecycle are tenant-wide. Built-in roles are immutable. Custom roles cannot grant access.manage. Writes and private audit evidence commit atomically. Public notifications do not expose the permission graph.
    ///
    /// `POST /api/v1/access/groups/{groupId}:suspend`
    pub async fn suspend_access_group(
        &self,
        group_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<AccessGroup>, ApiOpError<SuspendAccessGroupApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/access/groups/{}:suspend",
                __pct_encode_path_segment(group_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<SuspendAccessGroupApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(SuspendAccessGroupApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Suspend an active membership without deleting its retained grants
    ///
    /// Makes the membership inactive while preserving its grants and history. Requires the current membership revision and a reason; the final active tenant administrator cannot be suspended.
    ///
    /// `POST /api/v1/memberships/{membershipId}:suspend`
    pub async fn suspend_membership(
        &self,
        membership_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Reason,
    ) -> Result<Response<MembershipLifecycleResult>, ApiOpError<SuspendMembershipApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/memberships/{}:suspend",
                __pct_encode_path_segment(membership_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<SuspendMembershipApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(SuspendMembershipApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Synchronize a federation source
    ///
    /// Queues synchronization for an eligible source using its current ETag. Follow the returned Job; acceptance does not mean synchronization has completed.
    ///
    /// `POST /api/v1/federation/sources/{sourceId}:sync`
    pub async fn sync_federation_source(
        &self,
        source_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Option<SyncFederationSourceRequest>,
    ) -> Result<Response<SourceSyncAccepted>, ApiOpError<SyncFederationSourceApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/sources/{}:sync",
                __pct_encode_path_segment(source_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        if let Some(request) = request {
            req = req
                .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
                .header("content-type", "application/json");
        } else {
            req = req.header(reqwest::header::CONTENT_LENGTH, "0");
        }
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 202u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "202",
                )),
            }))
        } else {
            let typed: Option<SyncFederationSourceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(SyncFederationSourceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Test a federation source
    ///
    /// Revalidates the exact endpoint with the current deployment egress policy and credential revision. Success advances the immutable source revision only when the source type, profile, and Registry endpoint remain unchanged. Failure does not change source configuration or approved evidence.
    ///
    /// `POST /api/v1/federation/sources/{sourceId}:test`
    pub async fn test_federation_source(
        &self,
        source_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Option<TestFederationSourceRequest>,
    ) -> Result<Response<FederationTestResult>, ApiOpError<TestFederationSourceApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/sources/{}:test",
                __pct_encode_path_segment(source_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        if let Some(request) = request {
            req = req
                .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
                .header("content-type", "application/json");
        } else {
            req = req.header(reqwest::header::CONTENT_LENGTH, "0");
        }
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<TestFederationSourceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(TestFederationSourceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Send a test webhook event
    ///
    /// At most one synchronous test operation is admitted per API process. A competing authorized current-revision request returns 503 with code WEBHOOK_TEST_BUSY without sending or retaining delivery evidence. An admitted test makes one attempt, ignores subscriptions and returns 200 with delivery evidence even when the receiver returns 503. It does not prove worker delivery or receiver processing. A failure after sending may leave acceptance uncertain even without a committed delivery row. Other 503 responses do not prove that no request was sent. This is a concurrency bound, not a rate limit. An explicit later attempt uses a new Idempotency-Key because command failures may be replayed.
    ///
    /// `POST /api/v1/webhooks/{webhookId}:test`
    pub async fn test_webhook(
        &self,
        webhook_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: Option<TestWebhookRequest>,
    ) -> Result<Response<WebhookDelivery>, ApiOpError<TestWebhookApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/webhooks/{}:test",
                __pct_encode_path_segment(webhook_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        if let Some(request) = request {
            req = req
                .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
                .header("content-type", "application/json");
        } else {
            req = req.header(reqwest::header::CONTENT_LENGTH, "0");
        }
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<TestWebhookApiError>;
            let parse_error: Option<String>;
            match status_code {
                503u16 => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(TestWebhookApiError::Status503(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(TestWebhookApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Unpublish one configured discovery host
    ///
    /// Removes the active discovery snapshot for the exact configured host while retaining its history. Requires an audit reason.
    ///
    /// `POST /api/v1/publication:unpublish`
    pub async fn unpublish_catalog(
        &self,
        idempotency_key: impl AsRef<str>,
        request: PublicationTransition,
    ) -> Result<Response<PublicationSnapshot>, ApiOpError<UnpublishCatalogApiError>> {
        let request_url = format!("{}{}", self.base_url, "/api/v1/publication:unpublish");
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<UnpublishCatalogApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(UnpublishCatalogApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Replace group metadata
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Assignment grants, revocations and group-member writes stay within the same identity realm; membership inventory and lifecycle are tenant-wide. Built-in roles are immutable. Custom roles cannot grant access.manage. Writes and private audit evidence commit atomically. Public notifications do not expose the permission graph.
    ///
    /// `PATCH /api/v1/access/groups/{groupId}`
    pub async fn update_access_group(
        &self,
        group_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: AccessGroupInput,
    ) -> Result<Response<AccessGroup>, ApiOpError<UpdateAccessGroupApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/access/groups/{}",
                __pct_encode_path_segment(group_id.as_ref())
            )
        );
        let mut req = self.http_client.patch(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<UpdateAccessGroupApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(UpdateAccessGroupApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Update a federation source
    ///
    /// Applies reviewed source configuration with the current ETag and any required change-preview or replacement-inspection evidence. Existing observations retain their provenance.
    ///
    /// `PATCH /api/v1/federation/sources/{sourceId}`
    pub async fn update_federation_source(
        &self,
        source_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: UpdateSource,
    ) -> Result<Response<FederationSource>, ApiOpError<UpdateFederationSourceApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/federation/sources/{}",
                __pct_encode_path_segment(source_id.as_ref())
            )
        );
        let mut req = self.http_client.patch(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<UpdateFederationSourceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(UpdateFederationSourceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Update namespace metadata without changing its stable slug
    ///
    /// Updates namespace display metadata with the current If-Match value and an audit reason. The namespace slug remains unchanged.
    ///
    /// `PATCH /api/v1/namespaces/{namespaceId}`
    pub async fn update_namespace(
        &self,
        namespace_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: PatchNamespace,
    ) -> Result<Response<Namespace>, ApiOpError<UpdateNamespaceApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/namespaces/{}",
                __pct_encode_path_segment(namespace_id.as_ref())
            )
        );
        let mut req = self.http_client.patch(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<UpdateNamespaceApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(UpdateNamespaceApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Update my saved item
    ///
    /// Requires an active human membership. Requires library.own and exact tenant/realm/membership ownership; administrators cannot select another owner. Owner reads, rename and removal remain available after target access loss. OAuth scope when enforced: personal.write. Responses are no-store. Mutations require Idempotency-Key; updates and removals also require If-Match. Required audit contains identity/revision only; no public notification is created. At most 200 items per owner, labels up to 120 characters, bodies up to 16 KiB.
    ///
    /// `PATCH /api/v1/personal/items/{itemId}`
    pub async fn update_personal_item(
        &self,
        item_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: PersonalInput,
    ) -> Result<Response<PersonalReceipt>, ApiOpError<UpdatePersonalItemApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/personal/items/{}",
                __pct_encode_path_segment(item_id.as_ref())
            )
        );
        let mut req = self.http_client.patch(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<UpdatePersonalItemApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(UpdatePersonalItemApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Update publisher metadata or declared domains
    ///
    /// Declared domains that remain approved or back an active publication must be revoked and retired before removal.
    ///
    /// `PATCH /api/v1/publishers/{publisherId}`
    pub async fn update_publisher(
        &self,
        publisher_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: PatchPublisher,
    ) -> Result<Response<Publisher>, ApiOpError<UpdatePublisherApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/publishers/{}",
                __pct_encode_path_segment(publisher_id.as_ref())
            )
        );
        let mut req = self.http_client.patch(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<UpdatePublisherApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(UpdatePublisherApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Replace role metadata and permissions
    ///
    /// Requires current access.manage permission held by a direct tenant administrator. Also requires the tenant.admin OAuth scope when scope enforcement is enabled. Assignment grants, revocations and group-member writes stay within the same identity realm; membership inventory and lifecycle are tenant-wide. Built-in roles are immutable. Custom roles cannot grant access.manage. Writes and private audit evidence commit atomically. Public notifications do not expose the permission graph. Permission updates replace the exact set and require explicit read companions. Revoke incompatible active assignments before a scope change or archival.
    ///
    /// `PATCH /api/v1/access/roles/{roleId}`
    pub async fn update_role_definition(
        &self,
        role_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: RoleDefinitionInput,
    ) -> Result<Response<RoleDefinition>, ApiOpError<UpdateRoleDefinitionApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/access/roles/{}",
                __pct_encode_path_segment(role_id.as_ref())
            )
        );
        let mut req = self.http_client.patch(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<UpdateRoleDefinitionApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(UpdateRoleDefinitionApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Update a webhook endpoint
    ///
    /// Updates endpoint settings with the current ETag. Destination and subscription changes remain subject to webhook admission policy.
    ///
    /// `PATCH /api/v1/webhooks/{webhookId}`
    pub async fn update_webhook(
        &self,
        webhook_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: PatchWebhook,
    ) -> Result<Response<Webhook>, ApiOpError<UpdateWebhookApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/webhooks/{}",
                __pct_encode_path_segment(webhook_id.as_ref())
            )
        );
        let mut req = self.http_client.patch(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<UpdateWebhookApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(UpdateWebhookApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Revalidate an immutable version revision
    ///
    /// Records a new immutable validation run over the exact retained VersionRevision under the current adapter and policy without changing its content, approval, or prior evidence. Deterministic structural evidence has no wall-clock expiry. An HTTPS representation is retrieved again through the deployment-owned anonymous public route; identical bytes renew its bounded retrieval evidence, while an unavailable, mismatched, or changed response records a blocked run and never rewrites the VersionRevision. An explicit blocked URL revalidation also withdraws active publication snapshots containing a release for that revision.
    ///
    /// `POST /api/v1/version-revisions/{revisionId}:validate`
    pub async fn validate_version_revision(
        &self,
        revision_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        request: Option<ValidateVersionRevisionRequest>,
    ) -> Result<Response<RevisionEvidenceEnvelope>, ApiOpError<ValidateVersionRevisionApiError>>
    {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/version-revisions/{}:validate",
                __pct_encode_path_segment(revision_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        if let Some(request) = request {
            req = req
                .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
                .header("content-type", "application/json");
        } else {
            req = req.header(reqwest::header::CONTENT_LENGTH, "0");
        }
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 201u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "201",
                )),
            }))
        } else {
            let typed: Option<ValidateVersionRevisionApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(ValidateVersionRevisionApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Withdraw your feedback
    ///
    /// Requires the human submitter with feedback.own and feedback.write scope when enforced. Clears comment and response and makes the record terminal. The receipt contains no private text. Read current state separately with GET.
    ///
    /// `POST /api/v1/feedback/{feedbackId}:withdraw`
    pub async fn withdraw_feedback(
        &self,
        feedback_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: FeedbackEmptyCommand,
    ) -> Result<Response<FeedbackReceipt>, ApiOpError<WithdrawFeedbackApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/feedback/{}:withdraw",
                __pct_encode_path_segment(feedback_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<WithdrawFeedbackApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(WithdrawFeedbackApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
    /// Withdraw an opinion
    ///
    /// Human opinions use exact version and source-generation identity. They do not change relevance, trust or publication approval. Own rating submission requires an active human and current target visibility. Retained own-list reads and withdrawal remain available after target visibility loss. Administrator actions cannot choose a score.  OAuth scope when enforced: ratings.write.
    ///
    /// `POST /api/v1/ratings/{ratingId}:withdraw`
    pub async fn withdraw_rating(
        &self,
        rating_id: impl AsRef<str>,
        idempotency_key: impl AsRef<str>,
        if_match: impl AsRef<str>,
        request: FeedbackEmptyCommand,
    ) -> Result<Response<RatingReceipt>, ApiOpError<WithdrawRatingApiError>> {
        let request_url = format!(
            "{}{}",
            self.base_url,
            format!(
                "/api/v1/ratings/{}:withdraw",
                __pct_encode_path_segment(rating_id.as_ref())
            )
        );
        let mut req = self.http_client.post(request_url);
        req = req
            .body(serde_json::to_vec(&request).map_err(HttpError::serialization_error)?)
            .header("content-type", "application/json");
        req = req.header("Idempotency-Key", idempotency_key.as_ref());
        req = req.header("If-Match", if_match.as_ref());
        req = req.header(reqwest::header::ACCEPT, "application/json");
        let response = self.send(req).await?;
        let status = response.status();
        let status_code = status.as_u16();
        let headers = response.headers().clone();
        let body_bytes = response.body.clone();
        let raw_body = body_bytes;
        let body_text = String::from_utf8_lossy(&raw_body).into_owned();
        if false || status_code == 200u16 {
            match serde_json::from_slice(&raw_body) {
                Ok(body) => Ok(Response::new(body, status_code, headers, raw_body)),
                Err(e) => Err(ApiOpError::api(ApiError {
                    status: status_code,
                    headers: headers,
                    body: body_text,
                    raw_body,
                    typed: None,
                    parse_error: Some(format!("failed to deserialize 2xx response body: {}", e)),
                })),
            }
        } else if status.is_success() {
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed: None,
                parse_error: Some(format!(
                    "unexpected successful status {}; generated return type selects `{}`",
                    status_code, "200",
                )),
            }))
        } else {
            let typed: Option<WithdrawRatingApiError>;
            let parse_error: Option<String>;
            match status_code {
                _ => match serde_json::from_slice::<Problem>(&raw_body) {
                    Ok(v) => {
                        typed = Some(WithdrawRatingApiError::Default(v));
                        parse_error = None;
                    }
                    Err(e) => {
                        typed = None;
                        parse_error = Some(e.to_string());
                    }
                },
            }
            Err(ApiOpError::api(ApiError {
                status: status_code,
                headers,
                body: body_text,
                raw_body,
                typed,
                parse_error,
            }))
        }
    }
}

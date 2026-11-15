-- Own-feedback current activity uses an ordered index, not a response-history store.
CREATE INDEX item_feedback_member_state_updated_page
    ON item_feedback(tenant_id,membership_id,state,updated_at DESC,id DESC);

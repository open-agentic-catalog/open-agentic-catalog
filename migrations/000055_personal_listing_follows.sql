-- A follow can outlive access to its target. The private baseline is never a foreign key.
ALTER TABLE personal_items DROP CONSTRAINT personal_items_input_check,
 ADD CONSTRAINT personal_items_input_check CHECK ((
   jsonb_typeof(input) = 'object' AND octet_length(input::text) <= 16384
   AND jsonb_typeof(input->'label') = 'string' AND char_length(input->>'label') BETWEEN 1 AND 120
   AND CASE WHEN input->>'type' = 'follow' THEN
     input - ARRAY['type','label','listingId','acknowledged'] = '{}'::jsonb
     AND jsonb_typeof(input->'listingId') = 'string' AND (input->>'listingId')::uuid::text = input->>'listingId'
     AND (NOT input ? 'acknowledged' OR (
       jsonb_typeof(input->'acknowledged') = 'object'
       AND (input->'acknowledged') ?& ARRAY['releaseId','revision']
       AND (input->'acknowledged') - ARRAY['releaseId','revision'] = '{}'::jsonb
       AND (input#>>'{acknowledged,releaseId}')::uuid::text = input#>>'{acknowledged,releaseId}'
       AND jsonb_typeof(input#>'{acknowledged,revision}') = 'number'
       AND (input#>>'{acknowledged,revision}')::bigint > 0))
   ELSE input->>'type' IN ('search','resource') AND NOT input ?| ARRAY['listingId','acknowledged'] END
 ) IS TRUE);
CREATE UNIQUE INDEX personal_items_follow_listing ON personal_items
 (tenant_id,membership_id,realm_id,(input->>'listingId')) WHERE input->>'type'='follow';

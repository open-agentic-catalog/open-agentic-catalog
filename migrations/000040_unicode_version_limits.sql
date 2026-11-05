-- Match the Control API's Unicode character limit without changing identity
-- equality, version presence, immutable evidence or document byte budgets.
ALTER TABLE source_artwork_revisions
  DROP CONSTRAINT source_artwork_revisions_check,
  ADD CONSTRAINT source_artwork_revisions_check
    CHECK (char_length(version) <= 128 AND (version_present OR version = ''));

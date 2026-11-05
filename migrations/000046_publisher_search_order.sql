-- Publisher names participate in column ordering. A rename must invalidate
-- existing search cursors before a reader can skip or repeat results.
CREATE TRIGGER publishers_search_generation
AFTER INSERT OR UPDATE OR DELETE ON publishers
FOR EACH ROW EXECUTE FUNCTION advance_catalog_search_generation();

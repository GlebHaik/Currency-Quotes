-- Date the provider's rate applies to (ECB publishes once per business day).
-- Nullable: rows written before this migration have no date.
ALTER TABLE quote_updates ADD COLUMN source_date DATE;
ALTER TABLE quotes ADD COLUMN source_date DATE;

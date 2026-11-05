-- Tenant overrides affect only new default-budget Job admissions.
ALTER TABLE tenant_settings ADD COLUMN new_job_max_attempts integer CHECK (new_job_max_attempts BETWEEN 1 AND 20);

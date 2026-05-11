-- Add report types used by the richer executive reporting workflow.
ALTER TYPE report_type ADD VALUE IF NOT EXISTS 'quarterly';
ALTER TYPE report_type ADD VALUE IF NOT EXISTS 'ai_executive';

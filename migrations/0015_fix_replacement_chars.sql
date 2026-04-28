-- Fix U+FFFD replacement characters introduced by importing Windows-1252 CSV files
-- before the CP1252 decoder was in place. Go's json.Marshal converts invalid UTF-8
-- bytes (e.g. CP1252 right-single-quote 0x92) to U+FFFD (chr(65533)).
-- Replace with a plain apostrophe, which covers the vast majority of cases
-- (contractions, possessives, smart quotes) in these umpire comment fields.

UPDATE submissions
SET
    comments  = replace(comments,          chr(65533), ''''),
    form_data = replace(form_data::text,   chr(65533), '''')::jsonb
WHERE comments   LIKE '%' || chr(65533) || '%'
   OR form_data::text LIKE '%' || chr(65533) || '%';

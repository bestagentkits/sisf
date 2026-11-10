-- What the current run of an issue is working on, PostgreSQL dialect. The
-- SQLite script next to this one makes the same change and must change
-- together with it.
--
-- The record keeps the candidate the run submitted and the release it rolled
-- out, so a later step cannot be about another one. The journal keeps both as
-- they stood on the record after each step, which is what a replayed write
-- returns. Rows written before this change name neither.
ALTER TABLE records ADD COLUMN candidate_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE records ADD COLUMN release_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE transitions ADD COLUMN record_candidate_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE transitions ADD COLUMN record_release_digest TEXT NOT NULL DEFAULT '';

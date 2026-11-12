-- When a stored delivery is tried again, and how it left the inbox, SQLite
-- dialect. The PostgreSQL script next to this one makes the same change and
-- must change together with it.
--
-- A delivery whose processing failed is held back until next_attempt_at,
-- and the pass asks only for deliveries that are due, so deliveries that
-- keep failing cannot fill every batch and hold back the ones behind them.
-- attempts and last_error count and explain the failures. outcome says how
-- a processed delivery left the inbox: handled, refused, gone (its issue or
-- repository no longer exists) or parked (it failed for too long and is
-- not tried again; last_error keeps why). Rows written before this change
-- are due at once and have no recorded outcome.
ALTER TABLE inbox ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE inbox ADD COLUMN next_attempt_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE inbox ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
ALTER TABLE inbox ADD COLUMN outcome TEXT NOT NULL DEFAULT '';

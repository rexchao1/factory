-- Migration 40 records who answered a needs-input question.
--
-- Approval already says who approved (sessions.approved_by). An answer given
-- on the human's behalf, by an overseer for instance, should say so too, or
-- the audit trail reads as if the human typed every answer themselves.
--
-- work_answers.actor is the label on each stored answer. It is free-form and
-- bounded like approved_by; the one reserved label, 'agent', is refused by
-- the application rather than by a CHECK, because the column carries no
-- allowed-value list. sessions.answered_by projects the current answer's
-- actor beside sessions.answer, and the needs-input transition clears both.
--
-- Both are plain ADD COLUMN, so nothing is rebuilt and no foreign key marker
-- is needed. SQLite requires an added NOT NULL column to have a non-NULL
-- default. 'operator' is the right value for every answer that already
-- exists, since only the operator could answer before this migration, and
-- the UPDATE below labels every Session that currently holds an answer the
-- same way. A Session with no answer keeps the empty string, which is what
-- the needs-input transition writes.
ALTER TABLE work_answers ADD COLUMN actor TEXT NOT NULL DEFAULT 'operator'
    CHECK (length(CAST(actor AS BLOB)) BETWEEN 1 AND 255);

ALTER TABLE sessions ADD COLUMN answered_by TEXT NOT NULL DEFAULT ''
    CHECK (length(CAST(answered_by AS BLOB)) <= 255);

UPDATE sessions SET answered_by = 'operator' WHERE answer != '';

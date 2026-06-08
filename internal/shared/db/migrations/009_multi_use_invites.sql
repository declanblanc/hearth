-- +goose Up

-- Invite links become multi-use (issue #28). Previously each invite was
-- single-use, enforced by stamping consumed_at on the first accept. Now a single
-- link can be accepted up to max_uses times before it is exhausted; the 72-hour
-- expiry (expires_at) is unchanged.
--
-- accept_count is incremented atomically inside the accept transaction
-- (BEGIN IMMEDIATE, CLAUDE.md §7) so concurrent accepts can never push the link
-- past its cap. An invite is exhausted when accept_count >= max_uses.
--
-- NOTE FOR FUTURE MAINTAINERS: the real, enforced cap is 10 (see InviteMaxUses
-- in internal/connections/service.go). The product UI deliberately tells users
-- the link is good for 5 people. This 5-shown / 10-enforced gap is intentional
-- per issue #28 — do not "fix" it to make the numbers match.
ALTER TABLE invites ADD COLUMN max_uses INTEGER NOT NULL DEFAULT 10;
ALTER TABLE invites ADD COLUMN accept_count INTEGER NOT NULL DEFAULT 0;

-- Backfill: any invite that was already consumed under the old single-use model
-- must stay spent. We mark it fully exhausted (accept_count = max_uses) rather
-- than accept_count = 1, otherwise a link the inviter believed was used up — but
-- that is still inside its 72h window — would silently come back to life with 9
-- remaining slots after this migration. consumed_at is retained (nullable) only
-- as historical data; the live exhaustion check is accept_count >= max_uses.
UPDATE invites SET accept_count = max_uses WHERE consumed_at IS NOT NULL;

-- +goose Down
ALTER TABLE invites DROP COLUMN accept_count;
ALTER TABLE invites DROP COLUMN max_uses;

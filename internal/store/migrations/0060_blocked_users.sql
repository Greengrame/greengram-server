-- contacts.block. A blocked user can't write to or add the blocker to
-- groups, and stops seeing their last seen, photo and the rest of what
-- privacy rules guard.
CREATE TABLE blocked_users (
    blocker_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    blocked_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    date int NOT NULL,
    PRIMARY KEY (blocker_id, blocked_id)
);
CREATE INDEX blocked_users_blocked ON blocked_users(blocked_id);

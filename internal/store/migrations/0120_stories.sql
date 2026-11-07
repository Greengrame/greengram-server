-- Stories. Ids count up per owner and are never reused, like message ids:
-- the apps keep read marks and deleted ids by number.
CREATE TABLE story_owners (
    owner_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    last_id int NOT NULL DEFAULT 0
);

-- audience is the main rule (everybody, contacts, close_friends,
-- selected); allow_* and deny_* are the exceptions, user and chat ids.
-- An expired story leaves the feed but stays in the owner's archive and,
-- if pinned, on the profile.
CREATE TABLE stories (
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    id int NOT NULL,
    random_id bigint NOT NULL DEFAULT 0,
    date int NOT NULL,
    expire_date int NOT NULL,
    pinned boolean NOT NULL DEFAULT false,
    pinned_top int NOT NULL DEFAULT 0, -- order on the profile, 0 = not
    noforwards boolean NOT NULL DEFAULT false,
    edit_date int NOT NULL DEFAULT 0,
    caption text NOT NULL DEFAULT '',
    entities bytea,
    media bytea NOT NULL,
    media_kind text,
    media_id bigint,
    audience text NOT NULL CHECK (audience IN ('everybody', 'contacts', 'close_friends', 'selected')),
    allow_users bigint[] NOT NULL DEFAULT '{}',
    deny_users bigint[] NOT NULL DEFAULT '{}',
    allow_chats bigint[] NOT NULL DEFAULT '{}',
    deny_chats bigint[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (owner_id, id)
);
CREATE INDEX stories_expire ON stories (owner_id, expire_date);
CREATE UNIQUE INDEX stories_random ON stories (owner_id, random_id) WHERE random_id <> 0;

-- One row per viewer and story; reaction is the TL Reaction, or null.
CREATE TABLE story_views (
    owner_id bigint NOT NULL,
    story_id int NOT NULL,
    viewer_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    date int NOT NULL,
    reaction bytea,
    reaction_date int NOT NULL DEFAULT 0,
    PRIMARY KEY (owner_id, story_id, viewer_id),
    FOREIGN KEY (owner_id, story_id) REFERENCES stories (owner_id, id) ON DELETE CASCADE
);

-- stories.readStories: the highest story id viewer has seen of owner.
CREATE TABLE story_reads (
    viewer_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    max_id int NOT NULL,
    date int NOT NULL,
    PRIMARY KEY (viewer_id, owner_id)
);

-- contacts.editCloseFriends.
CREATE TABLE close_friends (
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    friend_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (owner_id, friend_id)
);

-- stories.togglePeerStoriesHidden: owners whose stories viewer moved out
-- of the main feed (into "hidden stories").
CREATE TABLE stories_hidden (
    viewer_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (viewer_id, owner_id)
);

-- contacts.block with my_stories_from: "hide my stories from". These
-- people see none of owner's stories, whatever each story's rules say.
CREATE TABLE stories_blocked (
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    blocked_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    date int NOT NULL,
    PRIMARY KEY (owner_id, blocked_id)
);
CREATE INDEX stories_blocked_blocked ON stories_blocked (blocked_id);

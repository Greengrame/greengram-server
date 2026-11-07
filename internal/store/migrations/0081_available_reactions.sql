-- messages.getAvailableReactions: Telegram's own reaction artwork, imported
-- with ggstickers -reactions. Without rows the server makes do with the
-- seeded emoji packs.
CREATE TABLE available_reactions (
    reaction text PRIMARY KEY,
    position int NOT NULL,
    title text NOT NULL,
    inactive boolean NOT NULL DEFAULT false,
    premium boolean NOT NULL DEFAULT false,
    static_icon bigint NOT NULL REFERENCES documents(id),
    appear_animation bigint NOT NULL REFERENCES documents(id),
    select_animation bigint NOT NULL REFERENCES documents(id),
    activate_animation bigint NOT NULL REFERENCES documents(id),
    effect_animation bigint NOT NULL REFERENCES documents(id),
    around_animation bigint REFERENCES documents(id),
    center_icon bigint REFERENCES documents(id)
);

-- More gifts, artwork picked by emoji from the imported packs. A title that's
-- already in the catalog stays as an admin left it.
INSERT INTO gift_catalog(title, emoji, stars, convert_stars)
SELECT g.title, g.emoji, g.stars, g.convert_stars
FROM (VALUES
    ('Cookie', '🍪', 10::bigint, 8::bigint),
    ('Lollipop', '🍭', 10, 8),
    ('Ladybug', '🐞', 15, 13),
    ('Tulip', '🌷', 20, 17),
    ('Cherry Blossom', '🌸', 20, 17),
    ('Coffee', '☕️', 25, 21),
    ('Chocolate', '🍫', 25, 21),
    ('Four-Leaf Clover', '🍀', 25, 21),
    ('Sunflower', '🌻', 30, 25),
    ('Pizza', '🍕', 30, 25),
    ('Butterfly', '🦋', 50, 43),
    ('Puppy', '🐶', 50, 43),
    ('Bunny', '🐰', 50, 43),
    ('Fox', '🦊', 50, 43),
    ('Confetti', '🎊', 50, 43),
    ('Party Popper', '🎉', 75, 64),
    ('Star', '⭐️', 75, 64),
    ('Bullseye', '🎯', 75, 64),
    ('Birthday Cake', '🎂', 100, 85),
    ('Champagne', '🥂', 100, 85),
    ('Rainbow', '🌈', 100, 85),
    ('Robot', '🤖', 100, 85),
    ('Glowing Star', '🌟', 150, 128),
    ('Guitar', '🎸', 150, 128),
    ('Disco Ball', '🪩', 150, 128),
    ('Medal', '🏅', 200, 170),
    ('Graduation Cap', '🎓', 200, 170),
    ('Shark', '🦈', 200, 170),
    ('Dinosaur', '🦖', 300, 255),
    ('Trophy', '🏆', 350, 298),
    ('Ring', '💍', 1000, 850),
    ('Race Car', '🏎', 1500, 1275)
) AS g(title, emoji, stars, convert_stars)
WHERE NOT EXISTS (SELECT 1 FROM gift_catalog c WHERE lower(c.title) = lower(g.title));

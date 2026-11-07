-- Nothing fills gift_catalog: the import skips it and the admin panel has no
-- page for it, so "Send a Gift" had nothing to sell. A catalog someone
-- already filled by hand stays as it is.
INSERT INTO gift_catalog(title, emoji, stars, convert_stars)
SELECT title, emoji, stars, convert_stars
FROM (VALUES ('Heart', '❤️', 15::bigint, 13::bigint), ('Present', '🎁', 25, 21), ('Bouquet', '💐', 50, 43),
    ('Teddy Bear', '🧸', 100, 85), ('Diamond', '💎', 250, 213), ('Crown', '👑', 500, 425)) AS g(title, emoji, stars, convert_stars)
WHERE NOT EXISTS (SELECT 1 FROM gift_catalog);
